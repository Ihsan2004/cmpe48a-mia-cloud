// Package briefnotify coordinates the fan-out of a pre-meeting brief across
// the email + Slack delivery channels. Concrete transports (smtp client,
// slack client) live in sibling sub-packages and register themselves with
// the Dispatcher; this package stays free of HTTP/SMTP concerns so it can
// be exercised with fake BriefDeliverer implementations in unit tests.
package briefnotify

import (
	"context"
	"errors"
	"time"

	"mia-website/backend/internal/models"
)

// BriefDeliverContext bundles everything a concrete BriefDeliverer needs
// to render + send a brief. The dispatcher loads these dependencies once
// per delivery — individual deliverers should NOT re-query the DB for
// the same data.
type BriefDeliverContext struct {
	// Delivery is the audit row this attempt belongs to. IDs in here
	// are what concrete implementations log / use for idempotency.
	Delivery *models.BriefDelivery

	// Meeting and Brief are the user-facing content the message will
	// render. Both are required — the dispatcher refuses to call
	// Deliver with either missing.
	Meeting *models.Meeting
	Brief   *models.MeetingBrief

	// User email + profile preferences (timezone, language). The email
	// deliverer addresses User.Email; the Slack deliverer resolves the
	// bot_token via the user's slack_integrations row.
	User    *models.User
	Profile *models.Profile

	// DispatchedAt is set by the worker on pick-up for latency metrics.
	DispatchedAt time.Time
}

// BriefDeliverer is the channel-agnostic transport contract. Concrete
// implementations (email, slack) MUST be safe to call concurrently; the
// worker pool may dispatch multiple deliveries in parallel.
//
// Return semantics:
//   - nil err            → caller writes status=sent + providerMessageID
//   - err + retryable    → caller increments attempts; next run is another Deliver
//   - err + !retryable   → caller writes status=failed and never retries
//   - ErrDelivererSkip   → caller writes status=skipped with Error() as last_error
type BriefDeliverer interface {
	// Name returns one of models.BriefDeliveryChannelEmail /
	// BriefDeliveryChannelSlack. Used only for registration/logging —
	// the dispatcher already knows which channel it dispatched on.
	Name() string

	// Deliver performs the actual send. providerMessageID is optional
	// and persisted verbatim (Message-ID for email, ts for Slack).
	Deliver(ctx context.Context, dc BriefDeliverContext) (providerMessageID string, err error)
}

// ErrDelivererSkip signals that the delivery should be recorded as
// status='skipped' without a retry. Used e.g. when an email address is
// unsubscribed, or a Slack integration is revoked / pending-email.
// Compose it with errors.Is() at the call site:
//
//	if errors.Is(err, briefnotify.ErrDelivererSkip) { ... }
var ErrDelivererSkip = errors.New("delivery intentionally skipped")

// RetryableError marks a transport failure the worker should retry.
// Concrete deliverers wrap transient errors (SMTP 4xx, Slack 429) in
// this; anything else is treated as permanent by the worker.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string {
	if e == nil || e.Err == nil {
		return "retryable error"
	}
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Retryable wraps err so the worker recognises it as transient. Returns
// nil if err is nil, so call sites can do `return "", Retryable(err)`.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err}
}

// IsRetryable reports whether the error should trigger another attempt.
// Used by the worker to decide between MarkForRetry vs status='failed'.
func IsRetryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}
