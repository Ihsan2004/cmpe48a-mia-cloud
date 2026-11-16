package briefnotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// PreferenceLoader reads a user's brief-delivery toggles + unsubscribe
// state. Exposed as an interface so unit tests can stub it without a DB.
// The production implementation in services.ProfileService satisfies this
// shape.
type PreferenceLoader interface {
	LoadBriefDeliveryPreferences(ctx context.Context, userID uuid.UUID) (BriefDeliveryPreferences, error)
}

// BriefDeliveryPreferences is the narrow view of a profile the dispatcher
// actually needs. Keeping this struct small (instead of passing full
// Profile) makes it obvious which flags drive routing.
type BriefDeliveryPreferences struct {
	EmailEnabled        bool
	SlackEnabled        bool
	EmailUnsubscribedAt *time.Time
}

// BriefJobEnqueuer abstracts the background_jobs table write so we can
// swap in a fake for unit tests. The concrete impl in Phase 1 lives on
// repository.BackgroundJobRepository.EnqueueNotifyBrief.
type BriefJobEnqueuer interface {
	EnqueueNotifyBrief(ctx context.Context, deliveryID uuid.UUID, scheduledAt time.Time, maxAttempts int) (uuid.UUID, error)
}

// Dispatcher owns the "schedule a delivery, then execute it" flow for
// the brief notification pipeline. It is channel-agnostic — concrete
// deliverers register themselves via Register.
//
// Usage:
//
//	d := briefnotify.NewDispatcher(repo, prefs, jobs)
//	d.Register(emailDeliverer)
//	d.Register(slackDeliverer)
//	_ = d.ScheduleDeliveries(ctx, meetingID, userID, scheduledFor)
//	// later, from the worker:
//	_ = d.Execute(ctx, deliveryID, contextLoader)
type Dispatcher struct {
	repo  repository.BriefDeliveryRepository
	prefs PreferenceLoader
	jobs  BriefJobEnqueuer

	// deliverers keyed by channel name. Nil entries mean "no transport
	// configured" — we never insert delivery rows for those channels
	// (would enqueue jobs that can never succeed, just adding noise).
	deliverers map[string]BriefDeliverer
}

// NewDispatcher returns a fresh dispatcher with no deliverers registered.
// Wiring the concrete deliverers happens in cmd/server/main.go once the
// env config is parsed — Phase 1 leaves the map empty so ScheduleDeliveries
// creates zero rows and the foundation stays dormant behind feature flags.
func NewDispatcher(repo repository.BriefDeliveryRepository, prefs PreferenceLoader, jobs BriefJobEnqueuer) *Dispatcher {
	return &Dispatcher{
		repo:       repo,
		prefs:      prefs,
		jobs:       jobs,
		deliverers: map[string]BriefDeliverer{},
	}
}

// Register adds a transport. Safe to call at process boot; not safe to
// call concurrently with ScheduleDeliveries/Execute (not intended).
func (d *Dispatcher) Register(dv BriefDeliverer) {
	if dv == nil || dv.Name() == "" {
		return
	}
	d.deliverers[dv.Name()] = dv
}

// HasDeliverer reports whether a concrete transport has been registered
// for the given channel. Used by ScheduleDeliveries to skip creating
// delivery rows that can't be serviced; exposed for tests.
func (d *Dispatcher) HasDeliverer(channel string) bool {
	_, ok := d.deliverers[channel]
	return ok
}

// DelivererFor returns the registered transport for channel, or nil
// when none. Tests use this; production callers should just call
// Execute and let the dispatcher route.
func (d *Dispatcher) DelivererFor(channel string) BriefDeliverer {
	return d.deliverers[channel]
}

// DefaultMaxAttempts for notify_brief jobs. Roadmap Karar #14 allows
// three attempts with exponential backoff (30s/2min/10min). Worker
// will compute the actual delay.
const DefaultMaxAttempts = 3

// ErrNoActiveIntegration is returned by Slack deliverer when the user
// has no non-revoked slack_integrations row. Dispatcher converts it
// into status='skipped'.
var ErrNoActiveIntegration = errors.New("no active Slack integration for user")

// ErrIntegrationPendingEmail is returned when the Slack integration was
// installed but matched_email is still NULL (user hasn't completed the
// alternative-email flow). Dispatcher treats as skipped, not failed.
var ErrIntegrationPendingEmail = errors.New("slack integration pending email resolution")

// ErrEmailUnsubscribed is returned when profiles.email_unsubscribed_at
// is set. Worker converts to status='skipped'.
var ErrEmailUnsubscribed = errors.New("user has unsubscribed from email")

// ScheduleDeliveries is called after a brief has been generated. For
// each channel the user opted into AND for which a deliverer is
// registered, we insert a pending brief_deliveries row (UNIQUE(meeting,
// channel) keeps it idempotent) and enqueue a notify_brief job whose
// scheduled_at matches the delivery's scheduled_for. The worker will
// only pick the job up once that time has elapsed.
//
// Duplicates from the UNIQUE constraint are swallowed — a repeat tick
// in the same generate window must be a no-op, not an error.
func (d *Dispatcher) ScheduleDeliveries(ctx context.Context, meetingID, userID uuid.UUID, scheduledFor time.Time) error {
	if meetingID == uuid.Nil {
		return fmt.Errorf("meetingID cannot be nil")
	}
	if userID == uuid.Nil {
		return fmt.Errorf("userID cannot be nil")
	}
	if scheduledFor.IsZero() {
		return fmt.Errorf("scheduledFor cannot be zero")
	}
	if len(d.deliverers) == 0 {
		// Phase 1: no deliverers wired yet. We return nil so the
		// scheduler can call us unconditionally without guarding.
		return nil
	}

	prefs, err := d.prefs.LoadBriefDeliveryPreferences(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to load delivery preferences: %w", err)
	}

	channels := d.resolveActiveChannels(prefs)
	if len(channels) == 0 {
		return nil
	}

	var firstErr error
	for _, channel := range channels {
		delivery := &models.BriefDelivery{
			MeetingID:    meetingID,
			UserID:       userID,
			Channel:      channel,
			Status:       models.BriefDeliveryStatusPending,
			ScheduledFor: scheduledFor,
		}
		if err := d.repo.Create(ctx, delivery); err != nil {
			if errors.Is(err, repository.ErrBriefDeliveryDuplicate) {
				// Idempotent retry — the prior row already owns
				// the scheduled send, nothing to enqueue here.
				slog.Debug("brief delivery already exists; skipping",
					"meeting_id", meetingID, "channel", channel)
				continue
			}
			slog.Warn("failed to create brief delivery row",
				"meeting_id", meetingID, "channel", channel, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		if _, err := d.jobs.EnqueueNotifyBrief(ctx, delivery.ID, scheduledFor, DefaultMaxAttempts); err != nil {
			slog.Warn("failed to enqueue notify_brief job",
				"delivery_id", delivery.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// resolveActiveChannels walks the user's preference flags and returns
// only those channels that (1) the user opted into and (2) have a
// deliverer registered. Email is additionally gated on unsubscribe —
// if the user clicked the unsubscribe link, we don't enqueue email
// rows at all (saves worker cycles).
func (d *Dispatcher) resolveActiveChannels(prefs BriefDeliveryPreferences) []string {
	var out []string
	if prefs.EmailEnabled && prefs.EmailUnsubscribedAt == nil && d.HasDeliverer(models.BriefDeliveryChannelEmail) {
		out = append(out, models.BriefDeliveryChannelEmail)
	}
	if prefs.SlackEnabled && d.HasDeliverer(models.BriefDeliveryChannelSlack) {
		out = append(out, models.BriefDeliveryChannelSlack)
	}
	return out
}

// ContextLoader resolves the full BriefDeliverContext for a given
// delivery row. The worker owns the loader implementation so the
// dispatcher package stays free of meeting/brief repository imports.
type ContextLoader interface {
	Load(ctx context.Context, delivery *models.BriefDelivery) (BriefDeliverContext, error)
}

// Execute is called by the background_job worker for a notify_brief
// job. It loads the delivery, finds the registered deliverer for its
// channel, materialises the full send context via the loader, then
// calls Deliver and persists the terminal state.
//
// Returned error semantics match the worker's retry expectations:
//   - nil                           → worker marks job completed
//   - errors.Is(err, RetryableError) → worker schedules retry (attempts++)
//   - any other error               → worker moves to DLQ; delivery is
//                                     already marked status='failed' here
func (d *Dispatcher) Execute(ctx context.Context, deliveryID uuid.UUID, loader ContextLoader) error {
	if deliveryID == uuid.Nil {
		return fmt.Errorf("deliveryID cannot be nil")
	}
	delivery, err := d.repo.GetByID(ctx, deliveryID)
	if err != nil {
		return fmt.Errorf("failed to load delivery: %w", err)
	}
	if delivery == nil {
		// Row deleted mid-flight (e.g. meeting cancelled) — not a
		// retryable error, just a no-op from the worker's POV.
		return nil
	}

	// Terminal states — another worker already finished this delivery.
	switch delivery.Status {
	case models.BriefDeliveryStatusSent, models.BriefDeliveryStatusSkipped, models.BriefDeliveryStatusFailed:
		return nil
	}

	transport := d.DelivererFor(delivery.Channel)
	if transport == nil {
		// Phase 1 guardrail: no deliverer registered for this channel.
		// We mark skipped + log so ops sees the misconfiguration.
		slog.Warn("no deliverer registered for channel; marking skipped",
			"delivery_id", delivery.ID, "channel", delivery.Channel)
		_ = d.repo.UpdateStatus(ctx, delivery.ID, models.BriefDeliveryStatusSkipped, "", "no deliverer registered")
		metrics.BriefDeliveryAttempted.WithLabelValues(delivery.Channel, metrics.BriefStatusSkipped).Inc()
		return nil
	}

	if loader == nil {
		return fmt.Errorf("briefnotify: Execute requires a non-nil ContextLoader")
	}
	dc, err := loader.Load(ctx, delivery)
	if err != nil {
		_ = d.repo.IncrementAttempts(ctx, delivery.ID)
		_ = d.repo.UpdateStatus(ctx, delivery.ID, models.BriefDeliveryStatusFailed, "", "context load failed: "+err.Error())
		metrics.BriefDeliveryAttempted.WithLabelValues(delivery.Channel, metrics.BriefStatusFailed).Inc()
		return fmt.Errorf("failed to load delivery context: %w", err)
	}
	if dc.DispatchedAt.IsZero() {
		dc.DispatchedAt = time.Now().UTC()
	}

	if err := d.repo.IncrementAttempts(ctx, delivery.ID); err != nil {
		slog.Warn("failed to increment attempts", "delivery_id", delivery.ID, "error", err)
	}

	// Measure wall-clock time from transport.Deliver entry to terminal
	// state write. Uses the dispatched_at captured above so loader latency
	// is excluded (loader is often DB-bound and outside the deliverer's
	// control — keeping it out makes the metric actionable).
	startedAt := time.Now()
	msgID, err := transport.Deliver(ctx, dc)
	metrics.BriefDeliveryDurationSeconds.WithLabelValues(delivery.Channel).Observe(time.Since(startedAt).Seconds())

	if err == nil {
		metrics.BriefDeliveryAttempted.WithLabelValues(delivery.Channel, metrics.BriefStatusSent).Inc()
		return d.repo.UpdateStatus(ctx, delivery.ID, models.BriefDeliveryStatusSent, msgID, "")
	}

	switch {
	case errors.Is(err, ErrDelivererSkip),
		errors.Is(err, ErrNoActiveIntegration),
		errors.Is(err, ErrIntegrationPendingEmail),
		errors.Is(err, ErrEmailUnsubscribed):
		_ = d.repo.UpdateStatus(ctx, delivery.ID, models.BriefDeliveryStatusSkipped, "", err.Error())
		metrics.BriefDeliveryAttempted.WithLabelValues(delivery.Channel, metrics.BriefStatusSkipped).Inc()
		return nil
	case IsRetryable(err):
		// Leave status pending so worker retry picks it up again.
		// Retryable failures aren't counted toward attempted_total
		// because they haven't reached terminal state — would double-
		// count once the next attempt resolves.
		_ = d.repo.UpdateStatus(ctx, delivery.ID, models.BriefDeliveryStatusPending, "", err.Error())
		return err
	default:
		_ = d.repo.UpdateStatus(ctx, delivery.ID, models.BriefDeliveryStatusFailed, "", err.Error())
		metrics.BriefDeliveryAttempted.WithLabelValues(delivery.Channel, metrics.BriefStatusFailed).Inc()
		return err
	}
}
