// Package services — desktop_scheduler.go drives the Phase 6 command
// channel. A ticker polls meetings every 30s; for each meeting the user
// has opted into desktop capture (recording_method resolution), it decides
// whether the current moment falls into a start_recording / stop_recording
// / force_stop window and, if so, sends the command via the hub.
//
// Idempotency lives in desktop_command_log: UNIQUE (meeting_id, command_type)
// means a repeat tick in the same window is a no-op.
package services

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"
)

// Time-window bounds for each command.
//
// Phase 2.4 B4: per RECORDER_ROADMAP §2.4, the backend NO LONGER sends
// `stop_recording` at the scheduled end — silence/process fusion on the
// recorder owns that decision. The only stop the backend dispatches is
// `force_stop` as a safety cap at `scheduled_end + 30 min`, with a hard
// ceiling at 4 hours total recording (the recorder enforces the ceiling
// in R9b — backend just keeps re-emitting force_stop past +30 min and
// the recorder ignores duplicates via command_log idempotency).
//
// Visual:
//
//	start-2m ---- start ---- start+5m                    <- start_recording
//	                                        end+30m ...  <- force_stop
//
// `stopRecordingTrail` is retained as 0 sentinel for backward-compat
// in tests that reference it; new code paths must not use it.
const (
	startRecordingLead  = 2 * time.Minute
	startRecordingTrail = 5 * time.Minute
	forceStopAfter      = 30 * time.Minute
	desktopDefaultTick  = 30 * time.Second
)

// DesktopMeetingScheduler polls upcoming/active meetings and pushes
// start/stop/force_stop commands to desktop clients. Designed to run as a
// background goroutine alongside the existing SchedulerService.
type DesktopMeetingScheduler struct {
	hub      *DesktopHub
	repo     repository.DesktopRepository
	meetings repository.MeetingRepository
	resolver RecordingMethodResolver
	profiles *ProfileService
	quotaSvc quota.Service // issue #39: nil ⇒ check disabled (legacy tests)
	interval time.Duration
	now      func() time.Time // injected for deterministic time tests
}

// NewDesktopMeetingScheduler wires defaults. interval can be overridden via
// SetInterval for tests that don't want to wait 30s.
func NewDesktopMeetingScheduler(
	hub *DesktopHub,
	repo repository.DesktopRepository,
	meetings repository.MeetingRepository,
	resolver RecordingMethodResolver,
	profiles *ProfileService,
) *DesktopMeetingScheduler {
	return &DesktopMeetingScheduler{
		hub:      hub,
		repo:     repo,
		meetings: meetings,
		resolver: resolver,
		profiles: profiles,
		interval: desktopDefaultTick,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetQuotaService wires the per-user soft-cap gate (issue #39). Nil
// disables the check — keeps existing legacy tests green without touching
// their wiring.
func (s *DesktopMeetingScheduler) SetQuotaService(q quota.Service) {
	s.quotaSvc = q
}

// SetInterval overrides the tick interval. For tests only.
func (s *DesktopMeetingScheduler) SetInterval(d time.Duration) {
	if d > 0 {
		s.interval = d
	}
}

// SetNowFunc injects a clock. For tests only.
func (s *DesktopMeetingScheduler) SetNowFunc(f func() time.Time) {
	if f != nil {
		s.now = f
	}
}

// Start runs the tick loop until ctx is cancelled. Blocks — call from a
// goroutine.
func (s *DesktopMeetingScheduler) Start(ctx context.Context) {
	slog.Info("DesktopMeetingScheduler starting",
		"tick_interval", s.interval)

	// Immediate tick so a freshly-started process doesn't wait a full
	// interval before emitting its first command.
	s.tick(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("DesktopMeetingScheduler stopping")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick does a single scan over desktop-relevant meetings and dispatches
// commands that land inside the current time windows.
func (s *DesktopMeetingScheduler) tick(ctx context.Context) {
	meetings, err := s.meetings.GetDesktopRelevantMeetings(ctx)
	if err != nil {
		slog.Warn("desktop scheduler: load meetings failed", "error", err)
		return
	}
	if len(meetings) == 0 {
		return
	}

	now := s.now()
	for _, m := range meetings {
		method, err := s.resolver.GetEffectiveRecordingMethod(ctx, m.ID)
		if err != nil {
			slog.Warn("desktop scheduler: resolve method failed",
				"meeting_id", m.ID, "error", err)
			continue
		}
		if method != models.RecordingMethodDesktop {
			continue
		}

		cmdType, payload, ok := s.pickCommand(m, now)
		if !ok {
			continue
		}

		// Issue #39 — desktop start is the cost-bearing command (STT +
		// downstream pipeline). force_stop must always run regardless of
		// quota: we don't want a quota-exceeded user to be unable to
		// terminate a runaway recording. start_recording is the only
		// quota-gated command here.
		if cmdType == models.DesktopCommandTypeStartRecording && s.quotaSvc != nil {
			qCtx, qCancel := context.WithTimeout(ctx, 5*time.Second)
			status, err := s.quotaSvc.Check(qCtx, m.UserID)
			qCancel()
			if err != nil {
				slog.Error("desktop scheduler: quota check failed — proceeding (fail-open)",
					"meeting_id", m.ID, "user_id", m.UserID, "error", err)
			} else if status.Exceeded {
				metrics.QuotaRejections.WithLabelValues(status.Tier, "scheduler:desktop").Inc()
				slog.Info("desktop scheduler: quota exceeded — skipping start_recording",
					"meeting_id", m.ID,
					"user_id", m.UserID,
					"tier", status.Tier,
					"cycle_resets_at", status.CycleEndAt)
				// Cross-channel visible status: meetings.bot_status doubles
				// as the quota sentinel for desktop too. UI surfaces this
				// instead of a silent no-op. Idempotent UPDATE.
				if dbErr := s.meetings.UpdateMeetingFields(ctx, m.ID, map[string]interface{}{
					"bot_status": "quota_exceeded",
				}); dbErr != nil {
					slog.Error("desktop scheduler: mark quota_exceeded failed",
						"meeting_id", m.ID, "error", dbErr)
				}
				continue
			}
		}

		inserted, err := s.repo.InsertCommandIfMissing(ctx, m.ID, m.UserID, cmdType)
		if err != nil {
			slog.Warn("desktop scheduler: insert command failed",
				"meeting_id", m.ID, "command", cmdType, "error", err)
			continue
		}
		if !inserted {
			// Previous tick already emitted this command — idempotent skip.
			continue
		}

		if err := s.hub.Send(m.UserID, payload); err != nil {
			// Phase 6 fix 2026-04-20 — eagerly persist no_ack so ops can
			// distinguish three states in SQL without reading logs:
			//   - NULL          → sent, waiting for client ack
			//   - no_ack        → never delivered (offline) or send failed
			//   - accepted/declined → client responded
			if errors.Is(err, ErrUserNotConnected) {
				slog.Info("desktop scheduler: user offline, recording no_ack",
					"meeting_id", m.ID, "user_id", m.UserID, "command", cmdType)
			} else {
				slog.Warn("desktop scheduler: send failed, recording no_ack",
					"meeting_id", m.ID, "command", cmdType, "error", err)
			}
			if ackErr := s.repo.MarkCommandAck(ctx, m.UserID, m.ID, cmdType, "no_ack"); ackErr != nil {
				slog.Warn("desktop scheduler: persist no_ack failed",
					"meeting_id", m.ID, "command", cmdType, "error", ackErr)
			}
			continue
		}

		slog.Info("desktop scheduler: command sent",
			"meeting_id", m.ID,
			"user_id", m.UserID,
			"command", cmdType,
		)
	}
}

// pickCommand decides which command (if any) applies for this meeting at
// `now`. Phase 2.4 B4: only two windows fire commands.
//
//   - force_stop   when now ≥ end + 30 min  (safety cap; recorder R9b
//     decides whether to extend or terminate)
//   - start_recording  when now ∈ [start - 2 min, start + 5 min]
//
// Earliest-wins order: force_stop beats start_recording on overlap (they
// can't overlap with the roadmap bounds, but the ordering is defensive
// for fixed-time tests). The previous `stop_recording at scheduled end`
// branch is removed — silence/process fusion on the recorder owns the
// "meeting ended naturally" decision per the unified Phase 2 design.
func (s *DesktopMeetingScheduler) pickCommand(
	m models.Meeting,
	now time.Time,
) (string, map[string]interface{}, bool) {
	forceStopBoundary := m.EndTime.Add(forceStopAfter)
	startWindowStart := m.StartTime.Add(-startRecordingLead)
	startWindowEnd := m.StartTime.Add(startRecordingTrail)

	switch {
	case !now.Before(forceStopBoundary):
		return models.DesktopCommandTypeForceStop, map[string]interface{}{
			"type":       "force_stop",
			"meeting_id": m.ID.String(),
			"reason":     "hard_cap_exceeded",
		}, true

	case !now.Before(startWindowStart) && !now.After(startWindowEnd):
		payload := map[string]interface{}{
			"type":            "start_recording",
			"meeting_id":      m.ID.String(),
			"meeting_title":   m.Title,
			"scheduled_start": m.StartTime.UTC().Format(time.RFC3339),
			"scheduled_end":   m.EndTime.UTC().Format(time.RFC3339),
			"request_confirm": true,
		}
		return models.DesktopCommandTypeStartRecording, payload, true
	}

	return "", nil, false
}
