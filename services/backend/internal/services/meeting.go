package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

type MeetingService struct {
	repo           repository.MeetingRepository
	profileService *ProfileService
	// chainMatcherV3 runs in shadow mode alongside the production
	// matcher (chain_matcher.go) — Phase 2 of the chain detection
	// roadmap. nil-safe: if construction fails (e.g. tests skip the
	// repository wiring), shadow logging is silently skipped instead
	// of nil-panicking.
	chainMatcherV3 *ChainMatcherV3
}

func NewMeetingService() *MeetingService {
	return &MeetingService{
		repo:           repository.NewMeetingRepository(),
		profileService: NewProfileService(),
		chainMatcherV3: NewChainMatcherV3(),
	}
}

// GetMeetingByID fetches a meeting by ID for a user
func (s *MeetingService) GetMeetingByID(userID, meetingID uuid.UUID) (*models.Meeting, error) {
	return s.repo.GetMeetingByID(context.Background(), userID, meetingID)
}

// UpsertDesktopMeeting is the idempotent create path for desktop-recorder
// sessions. Same (user_id, local_session_id) — and therefore same
// "desktop_<local_session_id>" synthetic google_event_id — always
// resolves to the same row.
//
// Backed by `MeetingRepository.UpsertDesktopMeeting`, which uses
// `INSERT … ON CONFLICT (user_id, google_event_id) DO NOTHING RETURNING`
// against the unique index from `sql/migrations/008_desktop_meeting_unique.sql`.
// That makes the upsert atomic at the database level: two concurrent
// callers (orchestrator's Start-time create racing the upload worker's
// retry) cannot both succeed at INSERT — the loser is told the row
// already exists and reads it back instead of writing a duplicate.
//
// Differs from `UpsertMeeting`:
//   - Skips the chain matcher (desktop ad-hoc sessions have no
//     calendar_series_id and no attendee list; matching is wasted work).
//   - Skips the "organizer-first" ownership transfer (desktop sessions
//     are always owned by the calling user; there is no organizer).
//
// Returns (meeting, isNew, err). `isNew=true` lets the handler return 201
// on first create and 200 on idempotent re-submits.
func (s *MeetingService) UpsertDesktopMeeting(meeting *models.Meeting) (*models.Meeting, bool, error) {
	ctx := context.Background()

	if !strings.HasPrefix(meeting.GoogleEventID, "desktop_") {
		return nil, false, fmt.Errorf(
			"UpsertDesktopMeeting requires google_event_id prefix 'desktop_', got %q",
			meeting.GoogleEventID,
		)
	}

	stored, isNew, err := s.repo.UpsertDesktopMeeting(ctx, meeting)
	if err != nil {
		return nil, false, fmt.Errorf("failed to upsert desktop meeting: %w", err)
	}

	if isNew {
		slog.Info("desktop meeting created",
			"meeting_id", stored.ID,
			"user_id", stored.UserID,
			"google_event_id", stored.GoogleEventID,
		)
	} else {
		slog.Info("desktop meeting idempotent re-submit",
			"meeting_id", stored.ID,
			"user_id", stored.UserID,
			"google_event_id", stored.GoogleEventID,
		)
	}
	return stored, isNew, nil
}

// UpsertMeeting inserts or updates a meeting based on google_event_id
func (s *MeetingService) UpsertMeeting(meeting *models.Meeting) error {
	ctx := context.Background()

	// Ownership Logic: Organizer First Strategy
	if meeting.OrganizerEmail != nil && *meeting.OrganizerEmail != "" {
		var organizerUserID uuid.UUID
		errOwner := s.repo.GetUserIDByEmail(ctx, *meeting.OrganizerEmail, &organizerUserID)
		if errOwner == nil {
			slog.Info("organizer found, assigning ownership", "organizer_email", *meeting.OrganizerEmail, "original_user_id", meeting.UserID, "new_user_id", organizerUserID)
			meeting.UserID = organizerUserID
		}
	}

	// Find Existing Record by (UserID, GoogleEventID)
	existing, err := s.repo.GetMeetingBySource(ctx, meeting.UserID, meeting.GoogleEventID)

	isNew := false
	if err != nil || existing == nil {
		if err != nil && !strings.Contains(err.Error(), "no rows") {
			return fmt.Errorf("failed to check existing meeting: %w", err)
		}
		isNew = true
	}

	// Carry over the existing meeting's chain assignment state so the
	// v3 matcher's manual-lock bypass + already-assigned bypass can
	// reason about the FULL prior decision, not just whatever the
	// inbound calendar payload happened to include (which is nothing,
	// for chain fields). Without this an in-place sync of a meeting
	// the user manually re-chained would slip past the manual lock.
	if !isNew && existing != nil {
		meeting.ID = existing.ID
		meeting.CreatedAt = existing.CreatedAt
		meeting.BotStatus = existing.BotStatus
		meeting.BotJoinedAt = existing.BotJoinedAt
		meeting.AutoBotEnabled = existing.AutoBotEnabled
		if meeting.ChainID == nil {
			meeting.ChainID = existing.ChainID
		}
		if meeting.ChainAssignmentMethod == nil {
			meeting.ChainAssignmentMethod = existing.ChainAssignmentMethod
		}
		if meeting.ChainAssignmentLockedAt == nil {
			meeting.ChainAssignmentLockedAt = existing.ChainAssignmentLockedAt
		}
		if meeting.ChainAssignmentScore == nil {
			meeting.ChainAssignmentScore = existing.ChainAssignmentScore
		}
		if meeting.ChainAssignmentReason == nil {
			meeting.ChainAssignmentReason = existing.ChainAssignmentReason
		}
	}

	// Chain detection v3 (Phase 4 cutover) — sole production decider.
	// Decide is read-only; MutateMeetingForDecision stamps the chain
	// fields onto `meeting` so the upcoming CreateMeeting / Update
	// picks them up in the same INSERT/UPDATE. PersistAfterIngest
	// runs the AFTER-INSERT side effects (chain_decisions_v2 row,
	// orphan repoint).
	decision := runProductionMatcher(ctx, s.chainMatcherV3, meeting)

	if isNew {
		slog.Info("ingesting new meeting", "title", meeting.Title)
		if err := s.repo.CreateMeeting(ctx, meeting); err != nil {
			return err
		}
	} else {
		slog.Info("updating metadata for meeting", "title", meeting.Title)
		if err := s.repo.UpdateMeetingIngestion(ctx, meeting); err != nil {
			return err
		}
	}

	// Now that the meeting row exists in DB, write the post-ingest
	// side effects (suggestion row / orphan repoint / applied audit
	// row). Errors are logged-and-continued — a chain_decisions_v2
	// hiccup must not block ingest.
	if err := s.chainMatcherV3.PersistAfterIngest(ctx, meeting, decision); err != nil {
		slog.Warn("chain matcher v3 persist-after-ingest failed",
			"meeting_id", meeting.ID, "action", decision.Action, "error", err)
	}

	return nil
}

// GetActiveMeetings returns currently ongoing meetings for a user
func (s *MeetingService) GetActiveMeetings(userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	return s.repo.GetActiveMeetings(context.Background(), userID, userEmail)
}

// GetUpcomingMeetings returns upcoming meetings for a user
func (s *MeetingService) GetUpcomingMeetings(userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	return s.repo.GetUpcomingMeetings(context.Background(), userID, userEmail)
}

// UpdateAttendeeResponse updates current user's response status inside attendees JSONB
func (s *MeetingService) UpdateAttendeeResponse(userID, meetingID uuid.UUID, userEmail, responseStatus string) error {
	meeting, err := s.GetMeetingByID(userID, meetingID)
	if err != nil {
		return fmt.Errorf("failed to fetch meeting for attendee update: %w", err)
	}

	updated := false
	for i, att := range meeting.Attendees {
		if strings.EqualFold(att.Email, userEmail) {
			meeting.Attendees[i].ResponseStatus = responseStatus
			updated = true
			break
		}
	}
	if !updated {
		meeting.Attendees = append(meeting.Attendees, models.Attendee{
			Email:          userEmail,
			ResponseStatus: responseStatus,
		})
	}

	return s.repo.UpdateAttendeeResponse(context.Background(), userID, meetingID, meeting.Attendees)
}

// GetPastMeetingsLastMonth returns meetings in the last 30 days
func (s *MeetingService) GetPastMeetingsLastMonth(userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	return s.repo.GetPastMeetingsLastMonth(context.Background(), userID, userEmail)
}

// DeleteMeetingsNotInGoogleEventIDs deletes meetings that are not in the provided Google event IDs list
func (s *MeetingService) DeleteMeetingsNotInGoogleEventIDs(userID uuid.UUID, googleEventIDs []string, windowStart, windowEnd time.Time) (int, error) {
	return s.repo.DeleteMeetingsNotInGoogleEventIDs(context.Background(), userID, googleEventIDs, windowStart, windowEnd)
}

// DeleteMeeting deletes a meeting by ID for a user
func (s *MeetingService) DeleteMeeting(userID, meetingID uuid.UUID) error {
	return s.repo.DeleteMeeting(context.Background(), userID, meetingID)
}

// GetGoogleEventIDsForUser returns all google_event_ids for a user's meetings
func (s *MeetingService) GetGoogleEventIDsForUser(userID uuid.UUID) ([]string, error) {
	return s.repo.GetGoogleEventIDsForUser(context.Background(), userID)
}

// SetRecordingMethodOverride writes the per-meeting override
// (Phase 5). method == nil or "" clears the override and falls back to
// the profile default on the next read. Returns ErrMeetingNotOwned when
// the meeting either doesn't exist or belongs to another user.
func (s *MeetingService) SetRecordingMethodOverride(
	ctx context.Context,
	userID, meetingID uuid.UUID,
	method *string,
) error {
	updated, err := s.repo.UpdateRecordingMethodOverride(ctx, userID, meetingID, method)
	if err != nil {
		return err
	}
	if !updated {
		return ErrMeetingNotOwned
	}
	return nil
}

// ErrMeetingNotOwned is returned when an action is scoped to a meeting
// the caller does not own (or the meeting doesn't exist). Handlers
// translate this to 404.
var ErrMeetingNotOwned = errors.New("meeting not found or not owned by caller")

// ErrMeetingNotDesktop is returned when a desktop-only operation is invoked
// on a calendar meeting. Handlers translate this to 422 Unprocessable Entity.
var ErrMeetingNotDesktop = errors.New("operation requires a desktop synthetic meeting")

// SetDesktopEndTime updates `end_time` for a desktop synthetic meeting only.
// Refuses to act on calendar meetings (google_event_id without "desktop_"
// prefix) so a misrouted call cannot accidentally clobber a real Google
// Calendar event's end time. Idempotent: calling repeatedly with the same
// or a later valid timestamp succeeds; the only validation is `end_time >
// start_time`. Does NOT call Google Calendar.
func (s *MeetingService) SetDesktopEndTime(ctx context.Context, userID, meetingID uuid.UUID, endedAt time.Time) error {
	meeting, err := s.repo.GetMeetingByIDSystem(ctx, meetingID)
	if err != nil {
		return ErrMeetingNotOwned
	}
	if meeting.UserID != userID {
		return ErrMeetingNotOwned
	}
	if !strings.HasPrefix(meeting.GoogleEventID, "desktop_") {
		return ErrMeetingNotDesktop
	}
	if !endedAt.After(meeting.StartTime) {
		return fmt.Errorf("ended_at must be after start_time (start=%s, ended=%s)",
			meeting.StartTime.Format(time.RFC3339), endedAt.Format(time.RFC3339))
	}
	return s.repo.UpdateMeetingFields(ctx, meetingID, map[string]any{
		"end_time": endedAt.UTC(),
	})
}

// GetMeetingsForUser aggregates upcoming and past meetings for a user
func (s *MeetingService) GetMeetingsForUser(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	upcoming, err := s.repo.GetUpcomingMeetings(ctx, userID, userEmail)
	if err != nil {
		return nil, err
	}
	past, err := s.repo.GetPastMeetingsLastMonth(ctx, userID, userEmail)
	if err != nil {
		return nil, err
	}
	return append(upcoming, past...), nil
}

// EditMeeting performs a partial update of a meeting.
func (s *MeetingService) EditMeeting(ctx context.Context, userID, meetingID uuid.UUID, fields map[string]interface{}) (*models.Meeting, error) {
	meeting, err := s.repo.GetMeetingByIDSystem(ctx, meetingID)
	if err != nil {
		return nil, fmt.Errorf("meeting not found")
	}

	if meeting.UserID != userID {
		return nil, fmt.Errorf("permission denied: only the meeting owner can edit")
	}

	if meeting.StartTime.Before(time.Now()) {
		return nil, fmt.Errorf("past meetings cannot be edited")
	}

	if err := s.repo.UpdateMeetingFields(ctx, meetingID, fields); err != nil {
		return nil, fmt.Errorf("failed to update meeting: %w", err)
	}

	updated, err := s.repo.GetMeetingByIDSystem(ctx, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch updated meeting: %w", err)
	}

	return updated, nil
}
