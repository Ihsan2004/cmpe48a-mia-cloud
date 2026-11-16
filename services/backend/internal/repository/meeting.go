package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MeetingRepository defines data access methods for meetings
type MeetingRepository interface {
	GetMeetingByID(ctx context.Context, userID, meetingID uuid.UUID) (*models.Meeting, error)
	GetMeetingByIDSystem(ctx context.Context, meetingID uuid.UUID) (*models.Meeting, error)
	GetMeetingBySource(ctx context.Context, userID uuid.UUID, googleEventID string) (*models.Meeting, error)
	CreateMeeting(ctx context.Context, meeting *models.Meeting) error
	// UpsertDesktopMeeting performs an atomic INSERT … ON CONFLICT
	// (user_id, google_event_id) DO NOTHING and reads the row back. The
	// returned isNew flag is true on the inserting call and false on every
	// subsequent call with the same (user_id, google_event_id) pair, which
	// is exactly the semantics the desktop recorder needs for idempotent
	// open-session create. The unique constraint is provided by
	// `meetings_user_google_event_unique` (sql/migrations/008…). Callers
	// MUST set google_event_id to "desktop_<local_session_id>" before
	// invoking this; the conflict target is global and the path is shared
	// only because the caller funnels through MeetingService anyway.
	UpsertDesktopMeeting(ctx context.Context, meeting *models.Meeting) (stored *models.Meeting, isNew bool, err error)
	UpdateMeetingIngestion(ctx context.Context, meeting *models.Meeting) error
	GetActiveMeetings(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error)
	GetUpcomingMeetings(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error)
	GetPastMeetingsLastMonth(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error)
	DeleteMeeting(ctx context.Context, userID, meetingID uuid.UUID) error
	DeleteMeetingsNotInGoogleEventIDs(ctx context.Context, userID uuid.UUID, googleEventIDs []string, windowStart, windowEnd time.Time) (int, error)
	GetGoogleEventIDsForUser(ctx context.Context, userID uuid.UUID) ([]string, error)
	UpdateAttendeeResponse(ctx context.Context, userID, meetingID uuid.UUID, attendees []models.Attendee) error
	GetUserIDByEmail(ctx context.Context, email string, userID *uuid.UUID) error
	GetBotStatus(ctx context.Context, meetingID uuid.UUID) (string, error)
	UpdateBotStatusConnecting(ctx context.Context, meetingID uuid.UUID) error
	UpdateAutoBotEnabled(ctx context.Context, meetingID uuid.UUID, enabled bool) error
	UpdateMeetingHeartbeat(ctx context.Context, meetingID uuid.UUID) (int64, error)
	UpdateBotEvent(ctx context.Context, meetingID uuid.UUID, event string) (int64, error)
	GetMeetingIDByURL(ctx context.Context, meetingURL string) (uuid.UUID, error)
	VerifyMeetingOwnership(ctx context.Context, userID, meetingID uuid.UUID) error
	GetMeetingURLByID(ctx context.Context, meetingID uuid.UUID) (string, error)
	UpdateMeetingFields(ctx context.Context, meetingID uuid.UUID, fields map[string]interface{}) error
	CountActiveBotMeetings(ctx context.Context) (int, error)
	// GetDesktopRelevantMeetings returns meetings whose time window overlaps
	// the desktop scheduler's interest band (start_time - 10min .. end_time
	// + 35min). Terminal bot_status values are filtered out so we don't
	// re-dispatch for a meeting that's already been cleaned up. Used only
	// by DesktopMeetingScheduler (Phase 6).
	GetDesktopRelevantMeetings(ctx context.Context) ([]models.Meeting, error)
	// UpdateRecordingMethodOverride sets (or clears with *string=nil)
	// meetings.recording_method_override atomically, scoped to the caller
	// via user_id. Returns (updated bool, error). updated=false when the
	// row either doesn't exist or belongs to another user — callers should
	// translate that to 404 (Phase 5 fix 2026-04-20: was raw SQL in handler).
	UpdateRecordingMethodOverride(ctx context.Context, userID, meetingID uuid.UUID, method *string) (updated bool, err error)
	// SetChainAssignment atomically writes the full chain-assignment
	// quintuple (chain_id + method + score + reason + locked_at). Each
	// field is taken at face value — nil means SQL NULL. Scoped by
	// user_id, returns updated=false when the meeting either doesn't
	// exist or doesn't belong to the caller (handlers translate to 404).
	// Phase 3: powers manual move/remove and suggestion accept.
	SetChainAssignment(ctx context.Context, userID, meetingID uuid.UUID, in SetChainAssignmentInput) (updated bool, err error)
	// ClearChainAssignmentLock nulls chain_assignment_method, score,
	// reason, and locked_at without touching chain_id. Phase 3 retry-
	// auto-detect uses this so the matcher can re-evaluate at next
	// ingest while the meeting keeps its current chain row in place
	// (no flicker on the meeting card).
	ClearChainAssignmentLock(ctx context.Context, userID, meetingID uuid.UUID) (updated bool, err error)
	// RepointMeetingChain sets meetings.chain_id directly. Used by
	// the v3 matcher's orphan-promotion path: when an orphan candidate
	// gets folded into a fresh chain alongside the new meeting, the
	// orphan needs its chain_id repointed without touching anything
	// else. UpdateMeetingFields can't do this — its allowlist
	// deliberately excludes chain_id so calendar-sync code can't
	// stomp the v3 matcher's decisions. Returns updated=false when
	// the row doesn't exist.
	RepointMeetingChain(ctx context.Context, meetingID uuid.UUID, chainID uuid.UUID) (updated bool, err error)
	// GetMeetingIDByTranscriptionJob returns the meeting whose
	// transcription_provider_job_id matches the given ElevenLabs request
	// id. Used by the ElevenLabs webhook to resolve the originating
	// meeting (Phase 3 fix 2026-04-20: moved here from TranscriptionService
	// so IngestWebhookResult is fully unit-testable via injected repo).
	GetMeetingIDByTranscriptionJob(ctx context.Context, providerJobID string) (uuid.UUID, error)
	// IncrementTranscriptionAttemptCount bumps the retry counter on the
	// meeting row (Phase 3 fix 2026-04-20: was silently at DEFAULT 0 because
	// only the background_jobs.attempts counter advanced; ops dashboards
	// and the UI need the meeting-side counter to show failed retries).
	IncrementTranscriptionAttemptCount(ctx context.Context, meetingID uuid.UUID) error
}

type postgresMeetingRepository struct {
	// We use the global DB pool from postgres.go for now,
	// but this struct allows for dependency injection in the future
}

func NewMeetingRepository() MeetingRepository {
	return &postgresMeetingRepository{}
}

func (r *postgresMeetingRepository) GetMeetingByID(ctx context.Context, userID, meetingID uuid.UUID) (*models.Meeting, error) {
	// Get user's email for access check
	var userEmail string
	DB.QueryRow(ctx, "SELECT email FROM users WHERE id = $1", userID).Scan(&userEmail)

	// Phase 4 fix 2026-04-20 — include migration 005 audio/transcription
	// columns so the dashboard can render failed-transcription UX and the
	// services downstream see the hydrated values.
	query := `
		SELECT
			m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url, m.description, m.location, m.attendees,
			m.organizer_email, m.google_event_id,
			ms.content as summary,
			msb.content as executive_summary,
			m.auto_bot_enabled, m.bot_status, m.bot_joined_at, m.last_heartbeat_at,
				m.created_at,
				m.agenda_items, m.expected_outcomes,
				m.calendar_series_id, m.chain_id, m.processed_at,
				m.audio_storage_path, m.audio_content_type, m.audio_size_bytes, m.audio_duration_seconds, m.audio_source,
				m.transcription_status, m.transcription_provider_job_id, m.transcription_attempt_count,
				m.transcription_last_error, m.transcription_completed_at, m.audio_deleted_at,
				m.recording_method_override, m.detected_app,
				m.chain_assignment_method, m.chain_assignment_score,
				m.chain_assignment_reason, m.chain_assignment_locked_at
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		LEFT JOIN meeting_summaries msb ON m.id = msb.meeting_id AND msb.summary_type = 'final_brief'
		WHERE m.id = $2
		AND (
			m.user_id = $1
			OR ($3 <> '' AND m.attendees @> ('[{"email": "' || $3 || '"}]')::jsonb)
		)
	`

	var meeting models.Meeting
	var attendeesJSON []byte
	err := DB.QueryRow(ctx, query, userID, meetingID, userEmail).Scan(
		&meeting.ID,
		&meeting.UserID,
		&meeting.Title,
		&meeting.StartTime,
		&meeting.EndTime,
		&meeting.MeetingURL,
		&meeting.Description,
		&meeting.Location,
		&attendeesJSON,
		&meeting.OrganizerEmail,
		&meeting.GoogleEventID,
		&meeting.Summary,
		&meeting.ExecutiveSummary,
		&meeting.AutoBotEnabled,
		&meeting.BotStatus,
		&meeting.BotJoinedAt,
		&meeting.LastHeartbeatAt,
		&meeting.CreatedAt,
		&meeting.AgendaItems,
		&meeting.ExpectedOutcomes,
		&meeting.CalendarSeriesID,
		&meeting.ChainID,
		&meeting.ProcessedAt,
		&meeting.AudioStoragePath,
		&meeting.AudioContentType,
		&meeting.AudioSizeBytes,
		&meeting.AudioDurationSeconds,
		&meeting.AudioSource,
		&meeting.TranscriptionStatus,
		&meeting.TranscriptionProviderJobID,
		&meeting.TranscriptionAttemptCount,
		&meeting.TranscriptionLastError,
		&meeting.TranscriptionCompletedAt,
		&meeting.AudioDeletedAt,
		&meeting.RecordingMethodOverride,
		&meeting.DetectedApp,
		&meeting.ChainAssignmentMethod,
		&meeting.ChainAssignmentScore,
		&meeting.ChainAssignmentReason,
		&meeting.ChainAssignmentLockedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get meeting: %w", err)
	}

	if len(attendeesJSON) > 0 {
		if err := json.Unmarshal(attendeesJSON, &meeting.Attendees); err != nil {
			slog.Warn("Failed to unmarshal attendees", "meeting_id", meeting.ID, "error", err)
		}
	}

	return &meeting, nil
}

// ... existing code ...

func (r *postgresMeetingRepository) GetMeetingByIDSystem(ctx context.Context, meetingID uuid.UUID) (*models.Meeting, error) {
	// Phase 4 fix 2026-04-20 — previously SELECT omitted migration 005 fields,
	// so TranscriptionService.SubmitForMeeting saw AudioStoragePath as nil
	// (transcribe jobs failed with "has no audio path" even after
	// upload-complete wrote the row) and AudioCleanupService.DeleteForMeeting
	// silently no-op'd because the storage path wasn't hydrated.
	//
	// Phase 5 review fix — chain_assignment_* columns added so the Stage 3
	// trigger sees the manual_removed lock. Without this the post-meeting
	// goroutine read meeting.ChainAssignmentMethod as nil even when the
	// user had explicitly removed the meeting from chains, defeating the
	// bypass guard inside ChainMatcherV3Stage3.
	query := `
		SELECT
			m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url, m.description, m.location, m.attendees,
			m.organizer_email, m.google_event_id,
			ms.content as summary,
			msb.content as executive_summary,
			m.auto_bot_enabled, m.bot_status, m.bot_joined_at, m.last_heartbeat_at,
			m.created_at,
			m.calendar_series_id, m.chain_id, m.processed_at,
			m.audio_storage_path, m.audio_content_type, m.audio_size_bytes, m.audio_duration_seconds, m.audio_source,
			m.transcription_status, m.transcription_provider_job_id, m.transcription_attempt_count,
			m.transcription_last_error, m.transcription_completed_at, m.audio_deleted_at,
			m.recording_method_override, m.detected_app,
			m.chain_assignment_method, m.chain_assignment_score,
			m.chain_assignment_reason, m.chain_assignment_locked_at
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		LEFT JOIN meeting_summaries msb ON m.id = msb.meeting_id AND msb.summary_type = 'final_brief'
		WHERE m.id = $1
	`

	var meeting models.Meeting
	var attendeesJSON []byte
	err := DB.QueryRow(ctx, query, meetingID).Scan(
		&meeting.ID,
		&meeting.UserID,
		&meeting.Title,
		&meeting.StartTime,
		&meeting.EndTime,
		&meeting.MeetingURL,
		&meeting.Description,
		&meeting.Location,
		&attendeesJSON,
		&meeting.OrganizerEmail,
		&meeting.GoogleEventID,
		&meeting.Summary,
		&meeting.ExecutiveSummary,
		&meeting.AutoBotEnabled,
		&meeting.BotStatus,
		&meeting.BotJoinedAt,
		&meeting.LastHeartbeatAt,
		&meeting.CreatedAt,
		&meeting.CalendarSeriesID,
		&meeting.ChainID,
		&meeting.ProcessedAt,
		&meeting.AudioStoragePath,
		&meeting.AudioContentType,
		&meeting.AudioSizeBytes,
		&meeting.AudioDurationSeconds,
		&meeting.AudioSource,
		&meeting.TranscriptionStatus,
		&meeting.TranscriptionProviderJobID,
		&meeting.TranscriptionAttemptCount,
		&meeting.TranscriptionLastError,
		&meeting.TranscriptionCompletedAt,
		&meeting.AudioDeletedAt,
		&meeting.RecordingMethodOverride,
		&meeting.DetectedApp,
		&meeting.ChainAssignmentMethod,
		&meeting.ChainAssignmentScore,
		&meeting.ChainAssignmentReason,
		&meeting.ChainAssignmentLockedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get meeting (system): %w", err)
	}

	if len(attendeesJSON) > 0 {
		if err := json.Unmarshal(attendeesJSON, &meeting.Attendees); err != nil {
			slog.Warn("Failed to unmarshal attendees", "meeting_id", meeting.ID, "error", err)
		}
	}

	return &meeting, nil
}

func (r *postgresMeetingRepository) GetMeetingBySource(ctx context.Context, userID uuid.UUID, googleEventID string) (*models.Meeting, error) {
	// Phase 5 review fix — chain_assignment_* columns added so the
	// MeetingService.UpsertMeeting carry-over actually surfaces a prior
	// manual_user / manual_removed lock to the v3 matcher. Without this,
	// the carry-over set meeting.ChainAssignmentMethod to nil (the
	// existing row's field was never scanned), and Decide() couldn't
	// distinguish "user removed this from a chain" from "fresh meeting"
	// — defeating the manual_removed bypass on calendar resync.
	query := `
		SELECT
			m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url, m.description, m.location, m.attendees,
			m.organizer_email, m.google_event_id,
			ms.content as summary,
			msb.content as executive_summary,
			m.auto_bot_enabled, m.bot_status, m.bot_joined_at, m.last_heartbeat_at,
			m.created_at,
			m.calendar_series_id, m.chain_id, m.processed_at,
			m.chain_assignment_method, m.chain_assignment_score,
			m.chain_assignment_reason, m.chain_assignment_locked_at
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		LEFT JOIN meeting_summaries msb ON m.id = msb.meeting_id AND msb.summary_type = 'final_brief'
		WHERE m.user_id = $1 AND m.google_event_id = $2
		LIMIT 1
	`

	var meeting models.Meeting
	var attendeesJSON []byte
	err := DB.QueryRow(ctx, query, userID, googleEventID).Scan(
		&meeting.ID,
		&meeting.UserID,
		&meeting.Title,
		&meeting.StartTime,
		&meeting.EndTime,
		&meeting.MeetingURL,
		&meeting.Description,
		&meeting.Location,
		&attendeesJSON,
		&meeting.OrganizerEmail,
		&meeting.GoogleEventID,
		&meeting.Summary,
		&meeting.ExecutiveSummary,
		&meeting.AutoBotEnabled,
		&meeting.BotStatus,
		&meeting.BotJoinedAt,
		&meeting.LastHeartbeatAt,
		&meeting.CreatedAt,
		&meeting.CalendarSeriesID,
		&meeting.ChainID,
		&meeting.ProcessedAt,
		&meeting.ChainAssignmentMethod,
		&meeting.ChainAssignmentScore,
		&meeting.ChainAssignmentReason,
		&meeting.ChainAssignmentLockedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(attendeesJSON) > 0 {
		if err := json.Unmarshal(attendeesJSON, &meeting.Attendees); err != nil {
			// Log error but continue
		}
	}

	return &meeting, nil
}

// UpsertDesktopMeeting performs an atomic INSERT ... ON CONFLICT
// (user_id, google_event_id) DO NOTHING RETURNING for desktop synthetic
// meetings. The unique constraint is established by
// `sql/migrations/008_desktop_meeting_unique.sql`.
//
// Implementation note: PostgreSQL's `ON CONFLICT ... DO NOTHING RETURNING`
// returns nothing on conflict, so we follow up with a SELECT against the
// same conflict target. Both statements run inside one transaction so a
// concurrent inserter cannot wedge an inconsistent view between the
// INSERT-skip and the SELECT.
func (r *postgresMeetingRepository) UpsertDesktopMeeting(
	ctx context.Context,
	meeting *models.Meeting,
) (*models.Meeting, bool, error) {
	attendeesJSON, err := json.Marshal(meeting.Attendees)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal attendees: %w", err)
	}
	autoBotEnabled := true
	if meeting.AutoBotEnabled != nil {
		autoBotEnabled = *meeting.AutoBotEnabled
	}

	tx, err := DB.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	insertQuery := `
		INSERT INTO meetings (
			id, user_id, title, start_time, end_time,
			meeting_url, description, location, attendees, organizer_email,
			google_event_id, auto_bot_enabled, bot_status, bot_joined_at, created_at,
			agenda_items, expected_outcomes, calendar_series_id, chain_id,
			audio_source, transcription_status, detected_app
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		ON CONFLICT (user_id, google_event_id) DO NOTHING
		RETURNING id
	`

	var insertedID uuid.UUID
	err = tx.QueryRow(
		ctx, insertQuery,
		meeting.ID, meeting.UserID, meeting.Title, meeting.StartTime, meeting.EndTime,
		meeting.MeetingURL, meeting.Description, meeting.Location, attendeesJSON, meeting.OrganizerEmail,
		meeting.GoogleEventID, autoBotEnabled, meeting.BotStatus, meeting.BotJoinedAt, meeting.CreatedAt,
		meeting.AgendaItems, meeting.ExpectedOutcomes, meeting.CalendarSeriesID, meeting.ChainID,
		meeting.AudioSource, meeting.TranscriptionStatus, meeting.DetectedApp,
	).Scan(&insertedID)

	isNew := false
	switch {
	case err == nil:
		isNew = true
	case errors.Is(err, pgx.ErrNoRows):
		// Conflict: another row with the same (user_id, google_event_id)
		// already exists. Fall through to SELECT below.
		isNew = false
	default:
		return nil, false, fmt.Errorf("insert desktop meeting: %w", err)
	}

	// Read the canonical row back regardless of whether we inserted or
	// conflicted. This guarantees the caller always sees the actually-stored
	// state, including the existing row's id when this call lost the race.
	selectQuery := `
		SELECT id, user_id, title, start_time, end_time, meeting_url, description,
		       location, attendees, organizer_email, google_event_id,
		       auto_bot_enabled, bot_status, bot_joined_at, last_heartbeat_at,
		       created_at, calendar_series_id, chain_id, processed_at,
		       audio_source, transcription_status, audio_storage_path,
		       audio_deleted_at, recording_method_override, detected_app
		FROM meetings
		WHERE user_id = $1 AND google_event_id = $2
		LIMIT 1
	`
	var stored models.Meeting
	var attendeesBytes []byte
	row := tx.QueryRow(ctx, selectQuery, meeting.UserID, meeting.GoogleEventID)
	if err := row.Scan(
		&stored.ID, &stored.UserID, &stored.Title, &stored.StartTime, &stored.EndTime,
		&stored.MeetingURL, &stored.Description, &stored.Location, &attendeesBytes, &stored.OrganizerEmail,
		&stored.GoogleEventID, &stored.AutoBotEnabled, &stored.BotStatus, &stored.BotJoinedAt, &stored.LastHeartbeatAt,
		&stored.CreatedAt, &stored.CalendarSeriesID, &stored.ChainID, &stored.ProcessedAt,
		&stored.AudioSource, &stored.TranscriptionStatus, &stored.AudioStoragePath,
		&stored.AudioDeletedAt, &stored.RecordingMethodOverride, &stored.DetectedApp,
	); err != nil {
		return nil, false, fmt.Errorf("select after upsert: %w", err)
	}
	if len(attendeesBytes) > 0 {
		_ = json.Unmarshal(attendeesBytes, &stored.Attendees)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit upsert tx: %w", err)
	}
	return &stored, isNew, nil
}

func (r *postgresMeetingRepository) CreateMeeting(ctx context.Context, meeting *models.Meeting) error {
	attendeesJSON, err := json.Marshal(meeting.Attendees)
	if err != nil {
		return fmt.Errorf("failed to marshal attendees: %w", err)
	}

	autoBotEnabled := true
	if meeting.AutoBotEnabled != nil {
		autoBotEnabled = *meeting.AutoBotEnabled
	}

	query := `
		INSERT INTO meetings (
			id, user_id, title, start_time, end_time, meeting_url, description, location,
			attendees, organizer_email, google_event_id, auto_bot_enabled, bot_status,
			bot_joined_at, created_at, agenda_items, expected_outcomes,
			calendar_series_id, chain_id, chain_assignment_method,
			chain_assignment_score, chain_assignment_reason, chain_assignment_locked_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20, $21, $22, $23
		)
	`

	_, err = DB.Exec(
		ctx,
		query,
		meeting.ID,
		meeting.UserID,
		meeting.Title,
		meeting.StartTime,
		meeting.EndTime,
		meeting.MeetingURL,
		meeting.Description,
		meeting.Location,
		attendeesJSON,
		meeting.OrganizerEmail,
		meeting.GoogleEventID,
		autoBotEnabled,
		meeting.BotStatus,
		meeting.BotJoinedAt,
		meeting.CreatedAt,
		meeting.AgendaItems,
		meeting.ExpectedOutcomes,
		meeting.CalendarSeriesID,
		meeting.ChainID,
		meeting.ChainAssignmentMethod,
		meeting.ChainAssignmentScore,
		meeting.ChainAssignmentReason,
		meeting.ChainAssignmentLockedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to create meeting: %w", err)
	}

	// Handle summary in separate table
	if meeting.Summary != nil && *meeting.Summary != "" {
		_, err = DB.Exec(ctx, "DELETE FROM meeting_summaries WHERE meeting_id = $1 AND summary_type = 'final_detailed'", meeting.ID)
		if err != nil {
			return fmt.Errorf("failed to clear old summary: %w", err)
		}
		_, err = DB.Exec(ctx, "INSERT INTO meeting_summaries (meeting_id, content, summary_type) VALUES ($1, $2, 'final_detailed')", meeting.ID, *meeting.Summary)
		if err != nil {
			return fmt.Errorf("failed to insert new summary: %w", err)
		}
	}

	return nil
}

func (r *postgresMeetingRepository) UpdateMeetingIngestion(ctx context.Context, meeting *models.Meeting) error {
	attendeesJSON, err := json.Marshal(meeting.Attendees)
	if err != nil {
		return fmt.Errorf("failed to marshal attendees: %w", err)
	}

	query := `
		UPDATE meetings SET
			title = $1,
			start_time = $2,
			end_time = $3,
			meeting_url = $4,
			description = $5,
			location = $6,
			attendees = $7,
			organizer_email = $8,
			agenda_items = COALESCE($10, agenda_items),
			expected_outcomes = COALESCE($11, expected_outcomes),
			calendar_series_id = COALESCE($12, calendar_series_id),
			chain_id = COALESCE($13, chain_id),
			chain_assignment_method = COALESCE($14, chain_assignment_method),
			chain_assignment_score = COALESCE($15, chain_assignment_score),
			chain_assignment_reason = COALESCE($16, chain_assignment_reason),
			chain_assignment_locked_at = COALESCE($17, chain_assignment_locked_at)
		WHERE id = $9
	`

	_, err = DB.Exec(
		ctx,
		query,
		meeting.Title,
		meeting.StartTime,
		meeting.EndTime,
		meeting.MeetingURL,
		meeting.Description,
		meeting.Location,
		attendeesJSON,
		meeting.OrganizerEmail,
		meeting.ID,
		meeting.AgendaItems,
		meeting.ExpectedOutcomes,
		meeting.CalendarSeriesID,
		meeting.ChainID,
		meeting.ChainAssignmentMethod,
		meeting.ChainAssignmentScore,
		meeting.ChainAssignmentReason,
		meeting.ChainAssignmentLockedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to update meeting: %w", err)
	}

	// Handle summary in separate table
	if meeting.Summary != nil && *meeting.Summary != "" {
		_, err = DB.Exec(ctx, "DELETE FROM meeting_summaries WHERE meeting_id = $1 AND summary_type = 'final_detailed'", meeting.ID)
		if err != nil {
			return fmt.Errorf("failed to clear old summary: %w", err)
		}
		_, err = DB.Exec(ctx, "INSERT INTO meeting_summaries (meeting_id, content, summary_type) VALUES ($1, $2, 'final_detailed')", meeting.ID, *meeting.Summary)
		if err != nil {
			return fmt.Errorf("failed to insert new summary: %w", err)
		}
	}

	return nil
}

func (r *postgresMeetingRepository) GetActiveMeetings(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	// Phase 2.5 P2 — project m.detected_app so list responses
	// hydrate Meeting.DetectedApp.
	query := `
		SELECT
			m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url, m.description, m.location, m.attendees,
			m.organizer_email, m.google_event_id,
			ms.content as summary,
			msb.content as executive_summary,
			m.auto_bot_enabled, m.bot_status, m.bot_joined_at, m.last_heartbeat_at,
			m.created_at,
			m.calendar_series_id, m.chain_id, m.processed_at, m.detected_app
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		LEFT JOIN meeting_summaries msb ON m.id = msb.meeting_id AND msb.summary_type = 'final_brief'
		WHERE (
			m.user_id = $1
			OR ($2 <> '' AND m.attendees @> ('[{"email": "' || $2 || '"}]')::jsonb)
		)
		  AND m.start_time <= NOW()
		  AND m.end_time >= NOW()
		  AND m.meeting_url IS NOT NULL
		ORDER BY m.start_time ASC
	`

	rows, err := DB.Query(ctx, query, userID, userEmail)
	if err != nil {
		return nil, fmt.Errorf("failed to query active meetings: %w", err)
	}
	defer rows.Close()

	var meetings []models.Meeting
	for rows.Next() {
		var meeting models.Meeting
		var attendeesJSON []byte
		err := rows.Scan(
			&meeting.ID,
			&meeting.UserID,
			&meeting.Title,
			&meeting.StartTime,
			&meeting.EndTime,
			&meeting.MeetingURL,
			&meeting.Description,
			&meeting.Location,
			&attendeesJSON,
			&meeting.OrganizerEmail,
			&meeting.GoogleEventID,
			&meeting.Summary,
			&meeting.ExecutiveSummary,
			&meeting.AutoBotEnabled,
			&meeting.BotStatus,
			&meeting.BotJoinedAt,
			&meeting.LastHeartbeatAt,
			&meeting.CreatedAt,
			&meeting.CalendarSeriesID,
			&meeting.ChainID,
			&meeting.ProcessedAt,
			&meeting.DetectedApp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan active meeting: %w", err)
		}

		if len(attendeesJSON) > 0 {
			if err := json.Unmarshal(attendeesJSON, &meeting.Attendees); err != nil {
				slog.Warn("Failed to unmarshal attendees", "meeting_id", meeting.ID, "error", err)
			}
		}

		meetings = append(meetings, meeting)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating active meetings: %w", err)
	}

	return meetings, nil
}

func (r *postgresMeetingRepository) GetUpcomingMeetings(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	// Phase 2.5 P2 fix — project m.detected_app so list responses
	// hydrate Meeting.DetectedApp instead of nil. Without this the
	// dashboard / debug UI sees the column populate on detail
	// reads but vanish on the list view.
	query := `
		SELECT
			m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url, m.description, m.location, m.attendees,
			m.organizer_email, m.google_event_id,
			ms.content as summary,
			msb.content as executive_summary,
			m.auto_bot_enabled, m.bot_status, m.bot_joined_at, m.last_heartbeat_at,
			m.created_at,
			m.calendar_series_id, m.chain_id, m.processed_at, m.detected_app
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		LEFT JOIN meeting_summaries msb ON m.id = msb.meeting_id AND msb.summary_type = 'final_brief'
		WHERE (
			m.user_id = $1
			OR ($2 <> '' AND m.attendees @> ('[{"email": "' || $2 || '"}]')::jsonb)
		)
		  AND (
		      m.end_time >= NOW()
		      OR (m.bot_status = 'recording' AND m.last_heartbeat_at > NOW() - INTERVAL '5 minutes')
		  )
		  AND m.meeting_url IS NOT NULL
		ORDER BY m.start_time ASC
	`

	rows, err := DB.Query(ctx, query, userID, userEmail)
	if err != nil {
		return nil, fmt.Errorf("failed to query meetings: %w", err)
	}
	defer rows.Close()

	var meetings []models.Meeting
	for rows.Next() {
		var meeting models.Meeting
		var attendeesJSON []byte
		err := rows.Scan(
			&meeting.ID,
			&meeting.UserID,
			&meeting.Title,
			&meeting.StartTime,
			&meeting.EndTime,
			&meeting.MeetingURL,
			&meeting.Description,
			&meeting.Location,
			&attendeesJSON,
			&meeting.OrganizerEmail,
			&meeting.GoogleEventID,
			&meeting.Summary,
			&meeting.ExecutiveSummary,
			&meeting.AutoBotEnabled,
			&meeting.BotStatus,
			&meeting.BotJoinedAt,
			&meeting.LastHeartbeatAt,
			&meeting.CreatedAt,
			&meeting.CalendarSeriesID,
			&meeting.ChainID,
			&meeting.ProcessedAt,
			&meeting.DetectedApp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan meeting: %w", err)
		}

		if len(attendeesJSON) > 0 {
			if err := json.Unmarshal(attendeesJSON, &meeting.Attendees); err != nil {
				slog.Warn("Failed to unmarshal attendees", "meeting_id", meeting.ID, "error", err)
			}
		}

		meetings = append(meetings, meeting)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating meetings: %w", err)
	}

	return meetings, nil
}

func (r *postgresMeetingRepository) GetPastMeetingsLastMonth(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error) {
	// Phase 2.5 P2 — project m.detected_app so list responses
	// hydrate Meeting.DetectedApp.
	query := `
		SELECT
			m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url, m.description, m.location, m.attendees,
			m.organizer_email, m.google_event_id,
			ms.content as summary,
			msb.content as executive_summary,
			m.auto_bot_enabled, m.bot_status, m.bot_joined_at, m.last_heartbeat_at,
			m.created_at,
			m.calendar_series_id, m.chain_id, m.processed_at, m.detected_app
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		LEFT JOIN meeting_summaries msb ON m.id = msb.meeting_id AND msb.summary_type = 'final_brief'
		WHERE (
			m.user_id = $1
			OR ($2 <> '' AND m.attendees @> ('[{"email": "' || $2 || '"}]')::jsonb)
		)
		  AND m.end_time < NOW()
		  AND m.end_time >= NOW() - INTERVAL '30 days'
		  AND m.end_time >= (SELECT created_at FROM users WHERE id = $1)
		  AND m.meeting_url IS NOT NULL
		  AND NOT (m.bot_status = 'recording' AND m.last_heartbeat_at > NOW() - INTERVAL '5 minutes')
		ORDER BY m.end_time DESC
	`

	rows, err := DB.Query(ctx, query, userID, userEmail)
	if err != nil {
		return nil, fmt.Errorf("failed to query past meetings: %w", err)
	}
	defer rows.Close()

	var meetings []models.Meeting
	for rows.Next() {
		var meeting models.Meeting
		var attendeesJSON []byte
		err := rows.Scan(
			&meeting.ID,
			&meeting.UserID,
			&meeting.Title,
			&meeting.StartTime,
			&meeting.EndTime,
			&meeting.MeetingURL,
			&meeting.Description,
			&meeting.Location,
			&attendeesJSON,
			&meeting.OrganizerEmail,
			&meeting.GoogleEventID,
			&meeting.Summary,
			&meeting.ExecutiveSummary,
			&meeting.AutoBotEnabled,
			&meeting.BotStatus,
			&meeting.BotJoinedAt,
			&meeting.LastHeartbeatAt,
			&meeting.CreatedAt,
			&meeting.CalendarSeriesID,
			&meeting.ChainID,
			&meeting.ProcessedAt,
			&meeting.DetectedApp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan past meeting: %w", err)
		}

		if len(attendeesJSON) > 0 {
			if err := json.Unmarshal(attendeesJSON, &meeting.Attendees); err != nil {
				slog.Warn("Failed to unmarshal attendees", "meeting_id", meeting.ID, "error", err)
			}
		}

		meetings = append(meetings, meeting)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating past meetings: %w", err)
	}

	return meetings, nil
}

func (r *postgresMeetingRepository) DeleteMeetingsNotInGoogleEventIDs(ctx context.Context, userID uuid.UUID, googleEventIDs []string, windowStart, windowEnd time.Time) (int, error) {
	// Safety guard:
	// Never perform bulk deletion when Google event IDs are empty.
	// Empty lists can happen during transient sync/parser/upsert issues and may cause accidental data loss.
	if len(googleEventIDs) == 0 {
		return 0, nil
	}

	// Delete meetings NOT in the list, but only if they fall within the window
	query := `
		DELETE FROM meetings
		WHERE user_id = $1
		AND start_time >= $2
		AND end_time <= $3
		AND google_event_id IS NOT NULL
		AND google_event_id != ''
		AND NOT (google_event_id = ANY($4))
	`

	tag, err := DB.Exec(ctx, query, userID, windowStart, windowEnd, googleEventIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to delete removed meetings: %w", err)
	}

	return int(tag.RowsAffected()), nil
}

func (r *postgresMeetingRepository) DeleteMeeting(ctx context.Context, userID, meetingID uuid.UUID) error {
	query := `DELETE FROM meetings WHERE user_id = $1 AND id = $2`
	result, err := DB.Exec(ctx, query, userID, meetingID)
	if err != nil {
		return fmt.Errorf("failed to delete meeting: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("meeting not found")
	}
	return nil
}

func (r *postgresMeetingRepository) GetGoogleEventIDsForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	query := `
		SELECT google_event_id
		FROM meetings
		WHERE user_id = $1
	`

	rows, err := DB.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query google event IDs: %w", err)
	}
	defer rows.Close()

	var eventIDs []string
	for rows.Next() {
		var eventID string
		err := rows.Scan(&eventID)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event ID: %w", err)
		}
		eventIDs = append(eventIDs, eventID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating event IDs: %w", err)
	}

	return eventIDs, nil
}

func (r *postgresMeetingRepository) UpdateAttendeeResponse(ctx context.Context, userID, meetingID uuid.UUID, attendees []models.Attendee) error {
	updatedJSON, err := json.Marshal(attendees)
	if err != nil {
		return fmt.Errorf("failed to marshal attendees: %w", err)
	}

	// NOTE: Removed user_id check - any attendee should be able to update their response
	// The meeting owner check was preventing non-owner attendees from updating responses
	queryUpdate := `
		UPDATE meetings
		SET attendees = $1
		WHERE id = $2
	`
	if _, err := DB.Exec(ctx, queryUpdate, updatedJSON, meetingID); err != nil {
		return fmt.Errorf("failed to update attendees: %w", err)
	}
	return nil
}

func (r *postgresMeetingRepository) GetUserIDByEmail(ctx context.Context, email string, userID *uuid.UUID) error {
	query := "SELECT id FROM users WHERE email = $1"
	return DB.QueryRow(ctx, query, email).Scan(userID)
}

func (r *postgresMeetingRepository) GetBotStatus(ctx context.Context, meetingID uuid.UUID) (string, error) {
	var status *string
	err := DB.QueryRow(ctx, `SELECT bot_status FROM meetings WHERE id = $1`, meetingID).Scan(&status)
	if err != nil {
		return "", err
	}
	if status == nil {
		return "", nil
	}
	return *status, nil
}

func (r *postgresMeetingRepository) UpdateBotStatusConnecting(ctx context.Context, meetingID uuid.UUID) error {
	_, err := DB.Exec(ctx, `UPDATE meetings SET bot_status = 'connecting' WHERE id = $1`, meetingID)
	return err
}

func (r *postgresMeetingRepository) UpdateAutoBotEnabled(ctx context.Context, meetingID uuid.UUID, enabled bool) error {
	_, err := DB.Exec(ctx, `UPDATE meetings SET auto_bot_enabled = $1 WHERE id = $2`, enabled, meetingID)
	return err
}

func (r *postgresMeetingRepository) UpdateMeetingHeartbeat(ctx context.Context, meetingID uuid.UUID) (int64, error) {
	result, err := DB.Exec(ctx, `UPDATE meetings SET last_heartbeat_at = NOW() WHERE id = $1`, meetingID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (r *postgresMeetingRepository) UpdateBotEvent(ctx context.Context, meetingID uuid.UUID, event string) (int64, error) {
	var query string
	switch event {
	case "joined":
		query = `UPDATE meetings SET bot_status = 'recording', bot_joined_at = NOW(), last_heartbeat_at = NOW() WHERE id = $1`
	case "failed":
		// Don't overwrite 'recording' — a stale "failed" event from a force-replaced container
		// should not disrupt the current active recording session.
		query = `UPDATE meetings SET bot_status = 'failed' WHERE id = $1 AND (bot_status IS NULL OR bot_status != 'recording')`
	case "completed":
		query = `
			UPDATE meetings
			SET bot_status = 'completed', end_time = NOW()
			WHERE id = $1 AND (bot_status IS NULL OR bot_status != 'completed')
		`
	default:
		return 0, fmt.Errorf("unknown bot event: %s", event)
	}
	result, err := DB.Exec(ctx, query, meetingID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (r *postgresMeetingRepository) GetMeetingIDByURL(ctx context.Context, meetingURL string) (uuid.UUID, error) {
	var id uuid.UUID
	err := DB.QueryRow(ctx, `SELECT id FROM meetings WHERE meeting_url = $1 ORDER BY start_time DESC LIMIT 1`, meetingURL).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (r *postgresMeetingRepository) VerifyMeetingOwnership(ctx context.Context, userID, meetingID uuid.UUID) error {
	query := `
		SELECT m.id FROM meetings m
		WHERE m.id = $1
		AND m.user_id = $2
	`
	var id uuid.UUID
	return DB.QueryRow(ctx, query, meetingID, userID).Scan(&id)
}

func (r *postgresMeetingRepository) UpdateMeetingFields(ctx context.Context, meetingID uuid.UUID, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}

	// Allowlist: columns that may be updated by handlers and the upload
	// pipeline. Originally narrowed to the user-edit set (title, etc.); the
	// audio_* / transcription_status columns were added when the Phase 8
	// batch path started reusing this method for FinalizeUpload, but the
	// list wasn't widened — silently dropping every audio field meant
	// successful uploads landed with empty audio_storage_path, and the
	// transcribe job then failed with "no audio path" + DLQ.
	allowedColumns := map[string]bool{
		"title": true, "start_time": true, "end_time": true,
		"description": true, "location": true, "attendees": true,
		"agenda_items": true, "expected_outcomes": true,
		"audio_storage_path":            true,
		"audio_content_type":            true,
		"audio_size_bytes":              true,
		"audio_duration_seconds":        true,
		"audio_source":                  true,
		"bot_status":                    true,
		"transcription_status":          true,
		"transcription_provider_job_id": true,
		"transcription_attempt_count":   true,
		"transcription_completed_at":    true,
		"transcription_failure_reason":  true,
		"audio_deleted_at":              true,
	}

	setClauses := []string{}
	args := []interface{}{}
	argIdx := 1

	for col, val := range fields {
		if !allowedColumns[col] {
			continue
		}
		if col == "attendees" {
			attendeesJSON, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("failed to marshal attendees: %w", err)
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, attendeesJSON)
		} else {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, argIdx))
			args = append(args, val)
		}
		argIdx++
	}

	if len(setClauses) == 0 {
		return nil
	}

	query := fmt.Sprintf("UPDATE meetings SET %s WHERE id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	args = append(args, meetingID)

	_, err := DB.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update meeting: %w", err)
	}
	return nil
}

func (r *postgresMeetingRepository) GetMeetingURLByID(ctx context.Context, meetingID uuid.UUID) (string, error) {
	var meetingURL *string
	err := DB.QueryRow(ctx, `SELECT meeting_url FROM meetings WHERE id = $1`, meetingID).Scan(&meetingURL)
	if err != nil || meetingURL == nil {
		return "", err
	}
	return *meetingURL, nil
}

// CountActiveBotMeetings returns the number of meetings that currently
// have a bot execution in flight. Used by Phase 7.10 concurrent-limit
// guard in SchedulerService.joinMeetingWithBot.
//
// "Active" states: joining / connecting / recording. Terminal states
// (completed, failed, cancelled) are excluded.
func (r *postgresMeetingRepository) CountActiveBotMeetings(ctx context.Context) (int, error) {
	var count int
	err := DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM meetings
		WHERE bot_status IN ('joining', 'connecting', 'recording')
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active bot meetings: %w", err)
	}
	return count, nil
}

// GetDesktopRelevantMeetings returns every meeting whose window overlaps
// the desktop scheduler's interest band:
//
//	start_time - 10min  <=  NOW()  <=  end_time + 35min
//
// The 10-min lead lets us fire start_recording at start - 2min with slack;
// the 35-min trailing lets us still emit force_stop at end + 30min. We skip
// terminal bot_status rows (completed / failed / cancelled) — those meetings
// are done even if the user had desktop selected.
//
// Issue #39 — quota_exceeded is NOT a terminal status; it's "blocked at
// the last attempt". When the user's cycle rolls over (or an admin
// patches the override) the next tick must be able to re-pick the
// meeting. The downstream gate inside DesktopMeetingScheduler.tick
// re-runs Check and re-flags if still over.
//
// Any recording method filtering is done in the scheduler (per-row, via
// resolver) because recording_method_override lives on the meeting but
// the profile default needs a JOIN the scheduler already performs
// indirectly.
func (r *postgresMeetingRepository) GetDesktopRelevantMeetings(ctx context.Context) ([]models.Meeting, error) {
	query := `
		SELECT id, user_id, title, start_time, end_time, meeting_url
		  FROM meetings
		 WHERE start_time - INTERVAL '10 minutes' <= NOW()
		   AND NOW() <= end_time + INTERVAL '35 minutes'
		   AND (bot_status IS NULL OR bot_status NOT IN ('completed', 'failed', 'cancelled'))
		 ORDER BY start_time ASC
		 LIMIT 200
	`

	rows, err := DB.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("desktop relevant meetings: %w", err)
	}
	defer rows.Close()

	var out []models.Meeting
	for rows.Next() {
		var m models.Meeting
		if err := rows.Scan(
			&m.ID, &m.UserID, &m.Title,
			&m.StartTime, &m.EndTime, &m.MeetingURL,
		); err != nil {
			return nil, fmt.Errorf("scan desktop meeting: %w", err)
		}
		out = append(out, m)
	}
	return out, nil
}

// UpdateRecordingMethodOverride writes (or clears) the per-meeting
// recording method override, scoped to the caller's user_id. Returns
// updated=false when no row matched (either wrong owner or unknown
// meeting); callers typically render this as 404.
func (r *postgresMeetingRepository) UpdateRecordingMethodOverride(
	ctx context.Context,
	userID uuid.UUID,
	meetingID uuid.UUID,
	method *string,
) (bool, error) {
	// Pass *string as-is — pgx maps Go nil to SQL NULL, which is what we
	// want for "clear override, fall back to profile default".
	var val any
	if method == nil || *method == "" {
		val = nil
	} else {
		val = *method
	}
	tag, err := DB.Exec(ctx,
		`UPDATE meetings
		    SET recording_method_override = $1, updated_at = NOW()
		  WHERE id = $2 AND user_id = $3`,
		val, meetingID, userID,
	)
	if err != nil {
		return false, fmt.Errorf("update recording method override: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetChainAssignmentInput is the wire shape for SetChainAssignment.
// All five chain_assignment columns are written verbatim from this
// struct — the caller composes the right payload per scenario:
//
//	manual move        → {ChainID: &x, Method: ptr("manual_user"),    LockedAt: ptr(now)}
//	manual remove      → {ChainID: nil, Method: ptr("manual_removed"), LockedAt: ptr(now)}
//	suggestion accept  → {ChainID: &x, Method: ptr("llm_suggestion"), Score: ptr(s), Reason: featureScores}
//
// nil == SQL NULL for every pointer/byteslice field. Reason is raw
// JSONB so the matcher can evolve the per-feature payload without a
// schema migration.
type SetChainAssignmentInput struct {
	ChainID  *uuid.UUID
	Method   *string
	Score    *float64
	Reason   []byte
	LockedAt *time.Time
}

// SetChainAssignment writes all five chain-assignment columns
// atomically. See SetChainAssignmentInput for the per-scenario payload.
func (r *postgresMeetingRepository) SetChainAssignment(
	ctx context.Context,
	userID, meetingID uuid.UUID,
	in SetChainAssignmentInput,
) (bool, error) {
	if userID == uuid.Nil || meetingID == uuid.Nil {
		return false, fmt.Errorf("SetChainAssignment: user_id and meeting_id required")
	}
	tag, err := DB.Exec(ctx, `
		UPDATE meetings
		   SET chain_id = $1,
		       chain_assignment_method = $2,
		       chain_assignment_score = $3,
		       chain_assignment_reason = $4,
		       chain_assignment_locked_at = $5
		 WHERE id = $6 AND user_id = $7`,
		in.ChainID, in.Method, in.Score, in.Reason, in.LockedAt,
		meetingID, userID,
	)
	if err != nil {
		return false, fmt.Errorf("set chain assignment: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RepointMeetingChain — see interface doc. Intentionally unscoped by
// user_id: the v3 matcher's orphan-promotion path operates on
// candidates already filtered to the same user via
// GetUserRecentMeetingsForMatching, so a second user_id check would
// be redundant. Returns updated=false when no row matches the id.
func (r *postgresMeetingRepository) RepointMeetingChain(
	ctx context.Context,
	meetingID uuid.UUID,
	chainID uuid.UUID,
) (bool, error) {
	if meetingID == uuid.Nil || chainID == uuid.Nil {
		return false, fmt.Errorf("RepointMeetingChain: meeting_id and chain_id required")
	}
	tag, err := DB.Exec(ctx,
		`UPDATE meetings SET chain_id = $1 WHERE id = $2`,
		chainID, meetingID,
	)
	if err != nil {
		return false, fmt.Errorf("repoint meeting chain: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClearChainAssignmentLock — see interface doc. Single statement,
// scoped by user_id; returns whether any row matched.
func (r *postgresMeetingRepository) ClearChainAssignmentLock(
	ctx context.Context,
	userID, meetingID uuid.UUID,
) (bool, error) {
	if userID == uuid.Nil || meetingID == uuid.Nil {
		return false, fmt.Errorf("ClearChainAssignmentLock: user_id and meeting_id required")
	}
	tag, err := DB.Exec(ctx, `
		UPDATE meetings
		   SET chain_assignment_method = NULL,
		       chain_assignment_score = NULL,
		       chain_assignment_reason = NULL,
		       chain_assignment_locked_at = NULL
		 WHERE id = $1 AND user_id = $2`,
		meetingID, userID,
	)
	if err != nil {
		return false, fmt.Errorf("clear chain assignment lock: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetMeetingIDByTranscriptionJob — Phase 3 resolver used by the
// ElevenLabs webhook to find the originating meeting from a provider
// request_id. Returns pgx.ErrNoRows if no row matches.
func (r *postgresMeetingRepository) GetMeetingIDByTranscriptionJob(
	ctx context.Context,
	providerJobID string,
) (uuid.UUID, error) {
	var id uuid.UUID
	err := DB.QueryRow(ctx,
		`SELECT id FROM meetings WHERE transcription_provider_job_id = $1 LIMIT 1`,
		providerJobID,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("get meeting by transcription job id: %w", err)
	}
	return id, nil
}

// IncrementTranscriptionAttemptCount atomically bumps the retry counter
// on the meeting row. Kept separate from UpdateMeetingFields so the
// increment semantics (current_value + 1) stay SQL-atomic and don't
// race with concurrent readers.
func (r *postgresMeetingRepository) IncrementTranscriptionAttemptCount(
	ctx context.Context,
	meetingID uuid.UUID,
) error {
	_, err := DB.Exec(ctx,
		`UPDATE meetings
		    SET transcription_attempt_count = transcription_attempt_count + 1
		  WHERE id = $1`,
		meetingID,
	)
	if err != nil {
		return fmt.Errorf("increment transcription_attempt_count: %w", err)
	}
	return nil
}
