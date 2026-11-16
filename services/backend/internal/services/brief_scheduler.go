package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/briefnotify"

	"github.com/google/uuid"
)

// briefGenerateBufferMinutes is the LLM-generation buffer between brief
// creation and the earliest allowed delivery. Roadmap Karar #17 (two-phase
// scheduler): generation fires at start-20min, delivery at start-LeadTime
// (default 10). The 5-10min gap covers LLM retries + template fallback.
const briefGenerateBufferMinutes = 20

// briefDispatchQueryLimit caps how many pending deliveries a single tick
// converts into background jobs. Prevents a stuck cron from fan-out-
// bombarding the queue when it catches up.
const briefDispatchQueryLimit = 100

// BriefScheduler periodically scans for upcoming meetings and generates briefs.
// Phase-1 delivery roadmap: after generating the brief, the scheduler also
// schedules per-channel brief_deliveries rows (idempotent, gated on user prefs
// + registered deliverers) and enqueues any dispatch-window-eligible rows as
// notify_brief background jobs. When no deliverers are wired, both hooks are
// safe no-ops so the legacy inline email path keeps working unchanged.
type BriefScheduler struct {
	briefGen         *BriefGenerator
	briefRepo        repository.BriefRepository
	deliveryRepo     repository.BriefDeliveryRepository
	jobRepo          repository.BackgroundJobRepository
	dispatcher       *briefnotify.Dispatcher // nil → delivery scheduling + dispatch are no-ops
	clock            func() time.Time
}

func NewBriefScheduler() *BriefScheduler {
	return &BriefScheduler{
		briefGen:     NewBriefGenerator(),
		briefRepo:    repository.NewBriefRepository(),
		deliveryRepo: repository.NewBriefDeliveryRepository(),
		jobRepo:      repository.NewBackgroundJobRepository(),
		clock:        func() time.Time { return time.Now().UTC() },
	}
}

// WithDispatcher wires the briefnotify dispatcher used for the new
// per-channel delivery pipeline. Call from main.go after the dispatcher
// is constructed (once the concrete deliverers for Phase 2+ are
// registered). Before this wiring, only the legacy inline email path
// runs — nothing writes to brief_deliveries.
func (s *BriefScheduler) WithDispatcher(d *briefnotify.Dispatcher) *BriefScheduler {
	s.dispatcher = d
	return s
}

// Start runs the brief scheduler in the background
func (s *BriefScheduler) Start(ctx context.Context) {
	slog.Info("Starting Brief Scheduler (checking every 5 minutes)")

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run both passes immediately on startup so a fresh boot doesn't
	// wait a full tick before catching up.
	s.Tick(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Brief Scheduler stopped")
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Tick runs the two-phase scheduler once. Exported for tests and for
// ad-hoc manual invocation. The two phases are:
//
//  1. Generate window — find meetings that are close enough to start
//     that we need a brief, produce one if absent, and schedule per-
//     channel delivery rows for the user's enabled channels.
//  2. Dispatch window — scan brief_deliveries for rows whose
//     scheduled_for has elapsed and enqueue notify_brief jobs so the
//     worker pool can send them.
//
// Each phase is isolated: a failure in one does not abort the other.
func (s *BriefScheduler) Tick(ctx context.Context) {
	s.checkAndGenerateBriefs(ctx)
	s.dispatchDueDeliveries(ctx)
}

// checkAndGenerateBriefs finds upcoming meetings that need briefs.
//
// Generate window: start_time − briefGenerateBufferMinutes (20m) ≤ now
// AND start_time > now AND brief doesn't exist yet. Using the full
// "still in the future and within the buffer" range (instead of a tight
// 5-minute slice) makes the scheduler idempotent against missed ticks:
// a delayed cron still catches the meeting as long as the buffer hasn't
// elapsed, and the "brief doesn't already exist" dedupe prevents
// duplicate generation on re-runs.
func (s *BriefScheduler) checkAndGenerateBriefs(ctx context.Context) {
	// NOTE: earlier Phase 1 version concatenated ($1 || ' minutes')::INTERVAL
	// which pgx refused to encode (int → text OID 25). Multiplying an
	// INTERVAL literal by an int arg is the pgx-friendly equivalent and
	// emits the same SQL semantics ("now + N minutes").
	query := `
		SELECT m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url,
		       m.chain_id, m.attendees, m.organizer_email, m.google_event_id,
		       m.created_at, m.agenda_items, m.expected_outcomes,
		       m.calendar_series_id, m.processed_at,
		       COALESCE(p.brief_lead_time_minutes, 10) as lead_time
		FROM meetings m
		JOIN profiles p ON p.id = m.user_id
		WHERE m.chain_id IS NOT NULL
		  AND m.start_time > NOW()
		  AND m.start_time <= NOW() + (INTERVAL '1 minute' * $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM meeting_briefs mb
		      WHERE mb.target_meeting_id = m.id AND mb.user_id = m.user_id
		  )
		ORDER BY m.start_time ASC
		LIMIT 20
	`

	rows, err := repository.DB.Query(ctx, query, briefGenerateBufferMinutes)
	if err != nil {
		slog.Error("Brief scheduler query failed", "error", err)
		return
	}
	defer rows.Close()

	type meetingWithLead struct {
		Meeting  models.Meeting
		LeadTime int
	}

	var meetings []meetingWithLead
	for rows.Next() {
		var m models.Meeting
		var attendeesJSON []byte
		var leadTime int

		if err := rows.Scan(
			&m.ID, &m.UserID, &m.Title, &m.StartTime, &m.EndTime, &m.MeetingURL,
			&m.ChainID, &attendeesJSON, &m.OrganizerEmail, &m.GoogleEventID,
			&m.CreatedAt, &m.AgendaItems, &m.ExpectedOutcomes,
			&m.CalendarSeriesID, &m.ProcessedAt,
			&leadTime,
		); err != nil {
			slog.Error("Failed to scan meeting for brief", "error", err)
			continue
		}

		if len(attendeesJSON) > 0 {
			if err := json.Unmarshal(attendeesJSON, &m.Attendees); err != nil {
				slog.Warn("Failed to unmarshal attendees for brief", "meeting_id", m.ID, "error", err)
			}
		}

		meetings = append(meetings, meetingWithLead{Meeting: m, LeadTime: leadTime})
	}

	if len(meetings) == 0 {
		return
	}

	slog.Info("Found meetings needing briefs", "count", len(meetings))

	for _, mwl := range meetings {
		m := mwl.Meeting
		if m.ChainID == nil {
			continue
		}

		brief, err := s.briefGen.GenerateBrief(ctx, m.UserID, &m, *m.ChainID)
		if err != nil {
			slog.Warn("Failed to generate brief", "meeting_id", m.ID, "error", err)
			continue
		}

		// Schedule per-channel deliveries (no-op in Phase 1 — the
		// dispatcher has no deliverers registered yet so nothing gets
		// written to brief_deliveries). scheduled_for honours the
		// user's lead time so the worker only sends at that mark.
		s.schedulePerChannelDeliveries(ctx, &m, mwl.LeadTime)

		// Legacy inline email path. Runs until Phase 3 migrates email
		// onto the briefnotify pipeline. Safe to coexist: legacy email
		// marks meeting_briefs.email_sent_at which the new pipeline
		// doesn't touch, and the new pipeline only activates when the
		// user explicitly opts in via Phase 6 UI toggles.
		s.sendBriefEmail(ctx, m.UserID, &m, brief)
	}
}

// schedulePerChannelDeliveries fans the newly-generated brief out onto
// the briefnotify pipeline. It is a no-op when:
//   - the dispatcher hasn't been wired (Phase 1 default)
//   - the user hasn't opted into any channel
//   - a deliverer isn't registered for an opted-in channel
//
// scheduledFor = meeting.start_time − lead_time so the worker doesn't
// pick the job up earlier than the user wants their brief to arrive.
func (s *BriefScheduler) schedulePerChannelDeliveries(ctx context.Context, meeting *models.Meeting, leadTimeMinutes int) {
	if s.dispatcher == nil {
		return
	}
	if meeting == nil || meeting.UserID == uuid.Nil {
		return
	}
	if leadTimeMinutes <= 0 {
		leadTimeMinutes = 10
	}
	scheduledFor := meeting.StartTime.Add(-time.Duration(leadTimeMinutes) * time.Minute)
	if err := s.dispatcher.ScheduleDeliveries(ctx, meeting.ID, meeting.UserID, scheduledFor); err != nil {
		if !errors.Is(err, repository.ErrBriefDeliveryDuplicate) {
			slog.Warn("failed to schedule brief deliveries",
				"meeting_id", meeting.ID, "user_id", meeting.UserID, "error", err)
		}
	}
}

// dispatchDueDeliveries scans brief_deliveries for pending rows whose
// scheduled_for has elapsed and enqueues a notify_brief background job
// for each. Uses background_jobs as the durable task queue so a crash
// between row creation and job enqueue is recoverable on the next tick
// (the row stays pending and will be re-seen).
//
// No-op when the dispatcher isn't wired or no pending rows exist.
func (s *BriefScheduler) dispatchDueDeliveries(ctx context.Context) {
	if s.dispatcher == nil {
		return
	}
	now := s.clock()
	pending, err := s.deliveryRepo.ListPendingDue(ctx, now, briefDispatchQueryLimit)
	if err != nil {
		slog.Error("brief scheduler: failed to list pending deliveries", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}

	for i := range pending {
		d := pending[i]
		// scheduledAt=now (not d.ScheduledFor) so the worker can pick
		// the job up immediately — scheduled_for was the *delivery*
		// due time, which has already passed by the time we're here.
		if _, err := s.jobRepo.EnqueueNotifyBrief(ctx, d.ID, now, briefnotify.DefaultMaxAttempts); err != nil {
			slog.Warn("failed to enqueue notify_brief job",
				"delivery_id", d.ID, "error", err)
		}
	}
	slog.Info("brief scheduler: dispatched pending deliveries", "count", len(pending))
}

// sendBriefEmail sends a brief notification email
func (s *BriefScheduler) sendBriefEmail(ctx context.Context, userID uuid.UUID, meeting *models.Meeting, brief *models.MeetingBrief) {
	// Get user email
	var email string
	err := repository.DB.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
	if err != nil || email == "" {
		return
	}

	emailService := NewEmailService()
	if !emailService.IsConfigured() {
		slog.Warn("Email not configured, skipping brief email", "meeting_id", meeting.ID)
		return
	}

	subject := "📋 Toplantı Hazırlığı: " + meeting.Title
	body := fmt.Sprintf(`<div style="font-family: sans-serif; max-width: 600px; margin: 0 auto;">
<h2>📋 %s</h2>
<p style="color: #666;">%s</p>
<div style="background: #f5f5f5; padding: 16px; border-radius: 8px; white-space: pre-wrap;">%s</div>
<br>
<a href="%s/meetings/%s/review" style="background: #7c3aed; color: white; padding: 12px 24px; border-radius: 8px; text-decoration: none; display: inline-block;">Brief'i Görüntüle</a>
</div>`,
		meeting.Title,
		meeting.StartTime.Format("2 January 2006 15:04"),
		brief.Content,
		getEnvOrDefault("FRONTEND_URL", "https://console.getmia.live"),
		meeting.ID.String(),
	)

	if err := emailService.sendEmail(email, subject, body); err != nil {
		slog.Error("Failed to send brief email", "meeting_id", meeting.ID, "error", err)
		return
	}

	// Mark email sent
	s.briefRepo.SetEmailSent(ctx, brief.ID)
	slog.Info("Brief email sent", "meeting_id", meeting.ID, "user_email", email)
}
