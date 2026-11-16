package briefnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mia-website/backend/internal/integrations/slackoauth"
	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
)

// SlackChatTransport is the narrow dependency the Slack deliverer has
// on the slackoauth package. Declared as an interface so unit tests
// substitute a fake without dialing Slack — and so the deliverer can
// exercise the 429 / invalid_auth branches deterministically.
type SlackChatTransport interface {
	PostMessage(ctx context.Context, botToken, channelID, text string, blocks json.RawMessage) (ts string, err error)
}

// SlackRateLimiter is the narrow dependency on slackoauth's rate
// limiter. Same rationale as SlackChatTransport — keeps tests fast
// and decoupled.
type SlackRateLimiter interface {
	Wait(ctx context.Context, teamID string) error
}

// TokenDecrypter unwraps the AES-GCM ciphertext stored in
// slack_integrations.bot_token_encrypted. Interface so Phase-5 tests
// can provide a fake decrypter without wiring a real TokenCipher.
type TokenDecrypter interface {
	Decrypt(ciphertext string) (string, error)
}

// SlackBriefDelivererConfig carries the static identity + URL config.
// DashboardBaseURL defaults to https://console.syncrun.net when empty.
type SlackBriefDelivererConfig struct {
	DashboardBaseURL string
}

// SlackBriefDeliverer renders a pre-meeting brief as Block Kit and
// DMs it via chat.postMessage. Implements the BriefDeliverer interface
// so the dispatcher can fan out to it alongside the email channel.
//
// Error semantics (bubbled up to dispatcher):
//
//	no active integration            → ErrNoActiveIntegration (skip)
//	matched_email or DMChannel nil   → ErrIntegrationPendingEmail (skip)
//	ciphertext corrupt / key rotated → ErrNoActiveIntegration (skip, after revoke)
//	chat.postMessage invalid_auth    → ErrNoActiveIntegration (skip, after revoke)
//	chat.postMessage 429 / transport → Retryable (worker retries)
//	any other Slack error            → plain error (permanent)
type SlackBriefDeliverer struct {
	cfg     SlackBriefDelivererConfig
	repo    repository.SlackIntegrationRepository
	chat    SlackChatTransport
	limiter SlackRateLimiter
	cipher  TokenDecrypter
	clock   func() time.Time
}

// NewSlackBriefDeliverer validates its dependencies — all four are
// required. Defaulting-in-constructor matches EmailBriefDeliverer's
// style so main.go wires both the same way.
func NewSlackBriefDeliverer(
	cfg SlackBriefDelivererConfig,
	repo repository.SlackIntegrationRepository,
	chat SlackChatTransport,
	limiter SlackRateLimiter,
	cipher TokenDecrypter,
) (*SlackBriefDeliverer, error) {
	if repo == nil {
		return nil, errors.New("briefnotify: SlackBriefDeliverer requires a non-nil repo")
	}
	if chat == nil {
		return nil, errors.New("briefnotify: SlackBriefDeliverer requires a non-nil chat transport")
	}
	if cipher == nil {
		return nil, errors.New("briefnotify: SlackBriefDeliverer requires a non-nil cipher")
	}
	if limiter == nil {
		// Degenerate default — no-op limiter. Works but lets you hit
		// the Slack rate limit in prod. Make this explicit in the
		// wiring by passing NewWorkspaceRateLimiter() from main.go.
		limiter = noopLimiter{}
	}
	if cfg.DashboardBaseURL == "" {
		cfg.DashboardBaseURL = "https://console.syncrun.net"
	}
	return &SlackBriefDeliverer{
		cfg:     cfg,
		repo:    repo,
		chat:    chat,
		limiter: limiter,
		cipher:  cipher,
		clock:   func() time.Time { return time.Now().UTC() },
	}, nil
}

func (d *SlackBriefDeliverer) Name() string { return models.BriefDeliveryChannelSlack }

// Deliver is the main entry point invoked by the dispatcher.
func (d *SlackBriefDeliverer) Deliver(ctx context.Context, dc BriefDeliverContext) (string, error) {
	if dc.Meeting == nil || dc.Brief == nil || dc.User == nil {
		return "", fmt.Errorf("SlackBriefDeliverer: BriefDeliverContext missing Meeting/Brief/User")
	}

	integ, err := d.repo.GetActiveForUser(ctx, dc.User.ID)
	if err != nil {
		return "", fmt.Errorf("SlackBriefDeliverer: load integration: %w", err)
	}
	if integ == nil {
		return "", ErrNoActiveIntegration
	}
	if integ.MatchedEmail == nil || integ.DMChannelID == nil || *integ.DMChannelID == "" {
		return "", ErrIntegrationPendingEmail
	}

	token, err := d.cipher.Decrypt(integ.BotTokenEncrypted)
	if err != nil {
		// Ciphertext won't open — key rotation or tampered row. Safest
		// move: revoke the integration so the SPA shows "reconnect" and
		// we don't hammer /chat.postMessage with a doomed token.
		_ = d.repo.Revoke(ctx, integ.ID)
		return "", ErrNoActiveIntegration
	}

	blocks, fallback, err := renderBriefSlackPayload(dc, d.cfg.DashboardBaseURL)
	if err != nil {
		return "", fmt.Errorf("SlackBriefDeliverer: render blocks: %w", err)
	}

	if err := d.limiter.Wait(ctx, integ.TeamID); err != nil {
		// Context cancellation surfaces here as a transient error so
		// the worker retries when the shutdown isn't actually for us.
		return "", Retryable(err)
	}

	ts, err := d.chat.PostMessage(ctx, token, *integ.DMChannelID, fallback, blocks)
	if err == nil {
		return ts, nil
	}

	switch {
	case errors.Is(err, slackoauth.ErrSlackInvalidAuth):
		// Roadmap Karar #18: auto-revoke on invalid_auth. User
		// uninstalled the app or the workspace admin disabled it —
		// either way the token can't be used again. Skip rather than
		// fail so the delivery row lands on status='skipped'.
		_ = d.repo.Revoke(ctx, integ.ID)
		return "", ErrNoActiveIntegration
	case errors.Is(err, slackoauth.ErrSlackRateLimited),
		errors.Is(err, slackoauth.ErrSlackTransport):
		// Transient classes: 429 (Slack throttle) + network/decode
		// errors. Wrap for the worker retry loop.
		return "", Retryable(err)
	default:
		// Anything else (e.g. channel_not_found, msg_too_long) is a
		// permanent failure — worker skips retry and moves on.
		return "", err
	}
}

// ============================================================
// Block Kit payload construction
// ============================================================

const (
	// Slack section text fields cap out at 3000 characters. We leave
	// a safety margin + append an ellipsis when we trim so the reader
	// knows content was clipped.
	slackSectionTextMax = 2900
)

// renderBriefSlackPayload builds the Block Kit JSON + the plain-text
// fallback used for desktop/mobile notifications. Keeping this in code
// rather than a template keeps JSON escaping safe and type-checkable.
func renderBriefSlackPayload(dc BriefDeliverContext, dashboardBaseURL string) (json.RawMessage, string, error) {
	meetingTitle := strings.TrimSpace(dc.Meeting.Title)
	if meetingTitle == "" {
		meetingTitle = "Toplantı"
	}

	tz, _ := time.LoadLocation("Europe/Istanbul")
	if dc.Profile != nil && dc.Profile.Timezone != "" {
		if loc, err := time.LoadLocation(dc.Profile.Timezone); err == nil {
			tz = loc
		}
	}
	startLocal := dc.Meeting.StartTime.In(tz).Format("2 January 2006 15:04 MST")

	briefContent := strings.TrimSpace(dc.Brief.Content)
	if briefContent == "" {
		briefContent = "_(brief içeriği oluşturulamadı)_"
	}
	if len(briefContent) > slackSectionTextMax {
		briefContent = briefContent[:slackSectionTextMax] + "…"
	}

	dashboardURL := strings.TrimRight(dashboardBaseURL, "/") +
		"/meetings/" + dc.Meeting.ID.String() + "/brief"

	// Block Kit structure — keep the field ordering deterministic so
	// golden tests can assert on a stable marshalled byte sequence.
	type textObj struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type block struct {
		Type     string    `json:"type"`
		Text     any       `json:"text,omitempty"`
		Fields   []textObj `json:"fields,omitempty"`
		Elements []any     `json:"elements,omitempty"`
	}
	type buttonElement struct {
		Type string  `json:"type"`
		Text textObj `json:"text"`
		URL  string  `json:"url"`
	}

	blocks := []block{
		{
			Type: "header",
			Text: textObj{Type: "plain_text", Text: "📋 Toplantı briefiniz hazır"},
		},
		{
			Type: "section",
			Fields: []textObj{
				{Type: "mrkdwn", Text: "*Toplantı:*\n" + escapeSlackMrkdwn(meetingTitle)},
				{Type: "mrkdwn", Text: "*Başlangıç:*\n" + startLocal},
			},
		},
		{
			Type: "section",
			Text: textObj{Type: "mrkdwn", Text: briefContent},
		},
		{
			Type: "actions",
			Elements: []any{
				buttonElement{
					Type: "button",
					Text: textObj{Type: "plain_text", Text: "Dashboard'da aç"},
					URL:  dashboardURL,
				},
			},
		},
	}

	raw, err := json.Marshal(blocks)
	if err != nil {
		return nil, "", fmt.Errorf("marshal block kit: %w", err)
	}

	// Plain-text fallback — shown in notification previews + clients
	// that don't render blocks. Keep it short but informative.
	fallback := fmt.Sprintf("📋 %s — toplantı öncesi briefiniz hazır (%s)", meetingTitle, startLocal)
	return raw, fallback, nil
}

// escapeSlackMrkdwn handles the three characters Slack's mrkdwn format
// treats specially: &, <, >. Slack uses its own flavour (not standard
// Markdown), so a bare `<` starts what looks like a link/mention and
// breaks rendering when the title legitimately contains one. Bold /
// italic markers are intentionally not escaped — if a user puts them
// in their meeting title we honour the formatting.
func escapeSlackMrkdwn(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}

// ==================== noop rate limiter ====================

type noopLimiter struct{}

func (noopLimiter) Wait(ctx context.Context, _ string) error {
	if ctx == nil {
		return nil
	}
	// Honour context cancellation even in no-op mode so shutdown
	// propagates uniformly across delivery paths.
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// compile-time interface adherence. Pin BriefDeliverer so a future
// signature tweak on the interface breaks this file first.
var _ BriefDeliverer = (*SlackBriefDeliverer)(nil)
