// Package services — calendar_upcoming.go serves the desktop recorder's
// candidate queue feed. Phase 2.4 B6 introduces the
// `GET /api/calendar/upcoming` endpoint that returns events in the next
// 6 h with per-event `effective_method` (resolved via the existing B5
// override → profile → default chain) plus brief readiness so the tray
// can offer a "View brief" affordance the moment it lands.
//
// This file owns the *response shape* — the handler in
// internal/handlers/calendar.go just parses auth + delegates here.

package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// UpcomingEventDTO is the per-event shape consumed by the recorder's
// `UpcomingEvent` (src-tauri/src/calendar/mod.rs). Field names use
// snake_case to match the recorder's serde::Deserialize tags.
type UpcomingEventDTO struct {
	EventID         string    `json:"event_id"`
	Provider        string    `json:"provider"`
	Title           string    `json:"title"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	MeetingURL      *string   `json:"meeting_url"`
	BriefReady      bool      `json:"brief_ready"`
	BriefURL        *string   `json:"brief_url"`
	EffectiveMethod string    `json:"effective_method"`
}

// UpcomingResponseDTO matches the recorder's `UpcomingResponse`. Provider
// status keys are the lowercase strings `"google"` and `"microsoft"`;
// values are `"connected"` or `"not_connected"`.
type UpcomingResponseDTO struct {
	Events         []UpcomingEventDTO `json:"events"`
	RefreshedAt    time.Time          `json:"refreshed_at"`
	ProviderStatus map[string]string  `json:"provider_status"`
}

// upcomingHorizon defines the candidate-queue window per RECORDER_ROADMAP
// D-04: the recorder shows up to 6 h of upcoming events in its tray queue,
// so the backend serves the same window. Anything beyond the horizon is
// not actionable for tray UX yet.
const upcomingHorizon = 6 * time.Hour

// upcomingMeetingsLister narrows MeetingRepository to just the method we
// need so unit tests can pass small fakes without stubbing the full
// repo surface.
type upcomingMeetingsLister interface {
	GetUpcomingMeetings(ctx context.Context, userID uuid.UUID, userEmail string) ([]models.Meeting, error)
}

// upcomingBriefLookup narrows BriefRepository similarly.
type upcomingBriefLookup interface {
	GetBriefByMeetingAndUser(ctx context.Context, meetingID, userID uuid.UUID) (*models.MeetingBrief, error)
}

// upcomingProfileStatus narrows ProfileService — we only need the
// connection check for the provider_status payload.
type upcomingProfileStatus interface {
	IsCalendarConnected(userID uuid.UUID) (bool, error)
}

// CalendarUpcomingService is a tiny façade that composes the existing
// pieces (meetings repo + brief repo + recording method resolver +
// profile service) into the response the recorder needs.
type CalendarUpcomingService struct {
	meetings upcomingMeetingsLister
	briefs   upcomingBriefLookup
	resolver RecordingMethodResolver
	profiles upcomingProfileStatus
	now      func() time.Time
}

// NewCalendarUpcomingService wires production defaults.
func NewCalendarUpcomingService(
	meetings repository.MeetingRepository,
	briefs repository.BriefRepository,
	resolver RecordingMethodResolver,
	profiles *ProfileService,
) *CalendarUpcomingService {
	return &CalendarUpcomingService{
		meetings: meetings,
		briefs:   briefs,
		resolver: resolver,
		profiles: profiles,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetNowFunc lets tests freeze the clock.
func (s *CalendarUpcomingService) SetNowFunc(f func() time.Time) {
	if f != nil {
		s.now = f
	}
}

// GetUpcomingForRecorder returns the next-`upcomingHorizon` candidate
// queue for the user. Order: meetings sorted by start_time ascending,
// only `start ≤ now + horizon` AND `end ≥ now`. Each event carries the
// resolved `effective_method` so the recorder can ignore events that
// resolve to `bot` or `skip` without making a second round-trip.
func (s *CalendarUpcomingService) GetUpcomingForRecorder(
	ctx context.Context,
	userID uuid.UUID,
	userEmail string,
) (UpcomingResponseDTO, error) {
	now := s.now()
	horizonEnd := now.Add(upcomingHorizon)

	all, err := s.meetings.GetUpcomingMeetings(ctx, userID, userEmail)
	if err != nil {
		return UpcomingResponseDTO{}, fmt.Errorf("calendar upcoming: load meetings: %w", err)
	}

	frontendURL := strings.TrimRight(
		getEnvOrDefault("FRONTEND_URL", "https://console.getmia.live"),
		"/",
	)

	events := make([]UpcomingEventDTO, 0, len(all))
	for _, m := range all {
		// Bound the window strictly: anything starting beyond the
		// horizon or already over is filtered out. The repo query
		// already drops `end_time < now` for non-recording rows, but
		// we re-check defensively.
		if m.StartTime.After(horizonEnd) {
			continue
		}
		if !m.EndTime.After(now) {
			continue
		}

		// Per-event effective method. A failure here shouldn't blank
		// the whole response — log + fall back to "bot" so the
		// recorder treats it as a backend-managed event.
		method, mErr := s.resolver.GetEffectiveRecordingMethod(ctx, m.ID)
		if mErr != nil {
			method = models.RecordingMethodBot
		}

		// Brief lookup is cheap (single PK-style row read). Missing
		// brief → BriefReady=false + nil URL.
		var briefReady bool
		var briefURL *string
		if brief, bErr := s.briefs.GetBriefByMeetingAndUser(ctx, m.ID, userID); bErr == nil && brief != nil {
			briefReady = true
			signed := fmt.Sprintf(
				"%s/meetings/%s/review",
				frontendURL,
				m.ID.String(),
			)
			briefURL = &signed
		}

		// Provider classification: desktop synthetic meetings have a
		// `desktop_<id>` google_event_id (see migration 008 / Phase 1
		// idempotency); the recorder's tray shouldn't list those as
		// "google" calendar events. We surface them as `provider="desktop"`
		// so the candidate queue can filter them out cleanly.
		provider := "google"
		if strings.HasPrefix(m.GoogleEventID, "desktop_") {
			provider = "desktop"
		}

		events = append(events, UpcomingEventDTO{
			EventID:         m.ID.String(),
			Provider:        provider,
			Title:           m.Title,
			Start:           m.StartTime.UTC(),
			End:             m.EndTime.UTC(),
			MeetingURL:      m.MeetingURL,
			BriefReady:      briefReady,
			BriefURL:        briefURL,
			EffectiveMethod: method,
		})
	}

	// Provider status. Microsoft is not yet wired backend-side; surface
	// as "not_connected" so the tray's "Connect Outlook" affordance
	// stays visible. Google reads the existing profile flag. `profiles`
	// is nilable for tests that don't care about provider_status.
	googleStatus := "not_connected"
	if s.profiles != nil {
		if connected, gErr := s.profiles.IsCalendarConnected(userID); gErr == nil && connected {
			googleStatus = "connected"
		}
	}

	return UpcomingResponseDTO{
		Events:      events,
		RefreshedAt: now,
		ProviderStatus: map[string]string{
			"google":    googleStatus,
			"microsoft": "not_connected",
		},
	}, nil
}

// FRONTEND_URL is read via the package-level `getEnvOrDefault` helper
// already defined in this package (see brief_scheduler.go).
