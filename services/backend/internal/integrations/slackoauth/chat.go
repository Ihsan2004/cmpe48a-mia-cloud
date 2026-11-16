package slackoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mia-website/backend/internal/pkg/metrics"
)

// ErrSlackInvalidAuth is returned when chat.postMessage (or any other
// authenticated call) responds with "invalid_auth" / "not_authed" /
// "token_revoked". The brief deliverer uses this signal to flip the
// slack_integrations row's revoked_at (roadmap Karar #18 — auto-clean
// when the user uninstalled the app from their workspace).
var ErrSlackInvalidAuth = errors.New("slackoauth: slack rejected bot token (invalid_auth/not_authed/token_revoked)")

// ErrSlackRateLimited signals a 429 response — the caller (typically
// the Slack brief deliverer) should wrap this in briefnotify.Retryable
// so the worker retries. Defined here (not as a briefnotify.Retryable
// directly) to keep the slackoauth package free of business-logic
// imports; otherwise we'd have an import cycle briefnotify ↔ slackoauth.
var ErrSlackRateLimited = errors.New("slackoauth: rate limited by slack (HTTP 429)")

// ErrSlackTransport marks network / decode-level failures as transient.
// Same rationale as ErrSlackRateLimited — caller maps to Retryable.
// Inner err preserved so errors.Is(err, ErrSlackTransport) works AND
// the original transport cause can still be unwrapped.
type slackTransportError struct{ err error }

func (e *slackTransportError) Error() string { return "slackoauth: transient transport error: " + e.err.Error() }
func (e *slackTransportError) Unwrap() error { return e.err }
func (e *slackTransportError) Is(target error) bool {
	return target == ErrSlackTransport
}

// ErrSlackTransport is the sentinel callers can match with errors.Is.
var ErrSlackTransport = errors.New("slackoauth: transient transport error")

func wrapTransport(err error) error { return &slackTransportError{err: err} }

// PostMessageResponse is the subset of chat.postMessage we consume.
// Slack returns many more fields (message echo, channel info,
// response_metadata) — we drop them intentionally so noisy payloads
// don't leak into logs.
type PostMessageResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	TS      string `json:"ts"`      // message timestamp, our provider_message_id
	Channel string `json:"channel"` // echoed channel id
}

// PostMessage sends a Block Kit payload to a DM channel. blocks must
// be a valid JSON array (rendered by the caller); text is the plain-
// text fallback shown in notifications / unsupported clients.
//
// Return semantics line up with briefnotify's expectations:
//
//   - 200 ok=true        → (ts, nil)
//   - invalid_auth class → ("", ErrSlackInvalidAuth)   // caller revokes
//   - 429                → ("", briefnotify.Retryable) // worker retries
//   - other slack errors → ("", plain error)           // permanent
//   - transport / decode → ("", briefnotify.Retryable) // worker retries
func (c *Client) PostMessage(ctx context.Context, botToken, channelID, text string, blocks json.RawMessage) (string, error) {
	if botToken == "" {
		return "", errors.New("slackoauth: PostMessage requires botToken")
	}
	if channelID == "" {
		return "", errors.New("slackoauth: PostMessage requires channelID")
	}

	body := map[string]any{
		"channel": channelID,
		"text":    text,
	}
	if len(blocks) > 0 {
		// RawMessage so we don't re-encode already-encoded JSON.
		body["blocks"] = blocks
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("slackoauth: marshal chat.postMessage body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("slackoauth: build chat.postMessage: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", wrapTransport(fmt.Errorf("chat.postMessage call: %w", err))
	}
	defer resp.Body.Close()

	// 429: Slack's documented rate-limit status. Retry-After is in
	// seconds (may be absent — fall back to 1s).
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		metrics.SlackAPIErrors.WithLabelValues("ratelimited").Inc()
		return "", fmt.Errorf("%w (retry-after=%s)", ErrSlackRateLimited, retryAfter)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", wrapTransport(fmt.Errorf("chat.postMessage read: %w", err))
	}

	var out PostMessageResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", wrapTransport(fmt.Errorf("chat.postMessage decode: %w", err))
	}
	if !out.OK {
		code := out.Error
		if code == "" {
			code = "unknown"
		}
		metrics.SlackAPIErrors.WithLabelValues(code).Inc()
		if isInvalidAuthError(out.Error) {
			return "", ErrSlackInvalidAuth
		}
		return "", fmt.Errorf("slackoauth: chat.postMessage refused: %s", out.Error)
	}
	if out.TS == "" {
		return "", errors.New("slackoauth: chat.postMessage returned empty ts")
	}
	return out.TS, nil
}

// isInvalidAuthError matches the set of Slack error codes that all mean
// "this bot token can no longer act on the workspace". Treating them
// uniformly is what makes auto-revoke safe — we can't distinguish
// "user uninstalled" from "workspace admin disabled the app" from
// "Slack rotated the token", and for our purposes we don't need to.
func isInvalidAuthError(code string) bool {
	switch code {
	case "invalid_auth", "not_authed", "token_revoked", "account_inactive":
		return true
	}
	return false
}

// parseRetryAfter returns the Retry-After header duration — Slack
// always sends seconds. Defensive default: 1s, so a malformed header
// doesn't let us hammer the endpoint in a tight retry loop.
func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1 * time.Second
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1 * time.Second
	}
	return time.Duration(n) * time.Second
}

// ============================================================
// Rate limiter — per-workspace 1 req/sec (roadmap 5.1)
// ============================================================

// WorkspaceRateLimiter serialises chat.postMessage calls so two worker
// goroutines delivering to the same Slack workspace stay under the
// 1-req-per-second-per-workspace tier-1 budget. Reservation-style
// throttle: each caller stamps the next allowed time before unlocking,
// which makes concurrent waiters queue up deterministically instead of
// all snapping to the same target instant.
//
// The limiter is safe for concurrent use. Zero value is usable.
type WorkspaceRateLimiter struct {
	mu      sync.Mutex
	nextAt  map[string]time.Time
	// minGap is the per-workspace floor between calls. Configurable so
	// tests can run with a tiny gap (ms) instead of 1s.
	minGap time.Duration
}

// NewWorkspaceRateLimiter returns a limiter with a 1-second gap.
func NewWorkspaceRateLimiter() *WorkspaceRateLimiter {
	return &WorkspaceRateLimiter{
		nextAt: make(map[string]time.Time),
		minGap: 1 * time.Second,
	}
}

// WithGap overrides the per-workspace minimum gap. Test-only.
func (l *WorkspaceRateLimiter) WithGap(d time.Duration) *WorkspaceRateLimiter {
	l.minGap = d
	return l
}

// Wait blocks until it's safe to issue another call for teamID.
// Respects ctx cancellation — partial waits return ctx.Err() so the
// caller can abandon cleanly during shutdown.
func (l *WorkspaceRateLimiter) Wait(ctx context.Context, teamID string) error {
	if teamID == "" {
		return nil
	}
	l.mu.Lock()
	if l.nextAt == nil {
		l.nextAt = map[string]time.Time{}
	}
	now := time.Now()
	target := l.nextAt[teamID]
	if now.After(target) {
		target = now
	}
	l.nextAt[teamID] = target.Add(l.minGap)
	l.mu.Unlock()

	wait := time.Until(target)
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}
