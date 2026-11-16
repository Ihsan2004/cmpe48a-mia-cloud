package briefnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/integrations/slackoauth"
	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== Fakes ====================

type fakeSlackRepo struct {
	integrations map[uuid.UUID]*models.SlackIntegration
	revokedIDs   []uuid.UUID
}

func newFakeSlackRepo() *fakeSlackRepo {
	return &fakeSlackRepo{integrations: map[uuid.UUID]*models.SlackIntegration{}}
}

func (f *fakeSlackRepo) Upsert(ctx context.Context, i *models.SlackIntegration) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	clone := *i
	f.integrations[i.ID] = &clone
	return nil
}

func (f *fakeSlackRepo) GetActiveForUser(ctx context.Context, userID uuid.UUID) (*models.SlackIntegration, error) {
	for _, v := range f.integrations {
		if v.UserID == userID && v.RevokedAt == nil {
			clone := *v
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeSlackRepo) GetByUserAndTeam(ctx context.Context, userID uuid.UUID, teamID string) (*models.SlackIntegration, error) {
	for _, v := range f.integrations {
		if v.UserID == userID && v.TeamID == teamID {
			clone := *v
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeSlackRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	f.revokedIDs = append(f.revokedIDs, id)
	if v, ok := f.integrations[id]; ok {
		now := time.Now().UTC()
		v.RevokedAt = &now
	}
	return nil
}

func (f *fakeSlackRepo) UpdateDMChannel(ctx context.Context, id uuid.UUID, dm, email string) error {
	if v, ok := f.integrations[id]; ok {
		v.DMChannelID = &dm
		v.MatchedEmail = &email
	}
	return nil
}

// ---- Chat transport stub ----

type stubChat struct {
	ts       string
	err      error
	lastArgs chatArgs
	called   int
}

type chatArgs struct {
	botToken, channelID, text string
	blocks                    json.RawMessage
}

func (s *stubChat) PostMessage(ctx context.Context, botToken, channelID, text string, blocks json.RawMessage) (string, error) {
	s.called++
	s.lastArgs = chatArgs{botToken: botToken, channelID: channelID, text: text, blocks: blocks}
	return s.ts, s.err
}

// ---- Decrypter stubs ----

type identityDecrypter struct{}

func (identityDecrypter) Decrypt(ct string) (string, error) { return ct, nil }

type failDecrypter struct{ err error }

func (f failDecrypter) Decrypt(_ string) (string, error) { return "", f.err }

// ---- Rate limiter stub ----

type fakeLimiter struct {
	err   error
	calls []string
}

func (f *fakeLimiter) Wait(ctx context.Context, teamID string) error {
	f.calls = append(f.calls, teamID)
	return f.err
}

// ==================== Fixture ====================

func sampleSlackContext(t *testing.T) (BriefDeliverContext, *SlackBriefDeliverer, *fakeSlackRepo, *stubChat, *fakeLimiter) {
	t.Helper()

	repo := newFakeSlackRepo()
	chat := &stubChat{ts: "1700000000.000001"}
	limiter := &fakeLimiter{}

	d, err := NewSlackBriefDeliverer(
		SlackBriefDelivererConfig{DashboardBaseURL: "https://console.syncrun.net"},
		repo, chat, limiter, identityDecrypter{},
	)
	if err != nil {
		t.Fatal(err)
	}

	userID := uuid.New()
	meetingID := uuid.New()
	dmID := "D1"
	email := "user@example.com"
	_ = repo.Upsert(context.Background(), &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: "xoxb-plaintext-via-identity",
		DMChannelID:       &dmID,
		MatchedEmail:      &email,
		Scope:             "chat:write",
	})

	dc := BriefDeliverContext{
		Delivery: &models.BriefDelivery{
			ID: uuid.New(), MeetingID: meetingID, UserID: userID,
			Channel: models.BriefDeliveryChannelSlack, Status: models.BriefDeliveryStatusPending,
		},
		Meeting: &models.Meeting{
			ID: meetingID, UserID: userID,
			Title:     "Haftalık Product Sync",
			StartTime: time.Date(2026, 5, 2, 14, 30, 0, 0, time.UTC),
		},
		Brief: &models.MeetingBrief{
			ID: uuid.New(), UserID: userID, TargetMeetingID: meetingID,
			Content: "Son toplantıda API v2 kararı alındı.",
		},
		User: &models.User{ID: userID, Email: "user@example.com"},
		Profile: &models.Profile{
			ID: userID, Timezone: "Europe/Istanbul",
		},
	}
	return dc, d, repo, chat, limiter
}

// ==================== Name + Constructor ====================

func TestSlackBriefDeliverer_Name(t *testing.T) {
	t.Parallel()
	_, d, _, _, _ := sampleSlackContext(t)
	if got := d.Name(); got != models.BriefDeliveryChannelSlack {
		t.Errorf("Name() = %q, want %q", got, models.BriefDeliveryChannelSlack)
	}
}

func TestNewSlackBriefDeliverer_RequiresDeps(t *testing.T) {
	t.Parallel()

	cfg := SlackBriefDelivererConfig{DashboardBaseURL: "https://console.syncrun.net"}
	_, err := NewSlackBriefDeliverer(cfg, nil, &stubChat{}, &fakeLimiter{}, identityDecrypter{})
	if err == nil {
		t.Error("expected error when repo is nil")
	}
	_, err = NewSlackBriefDeliverer(cfg, newFakeSlackRepo(), nil, &fakeLimiter{}, identityDecrypter{})
	if err == nil {
		t.Error("expected error when chat transport is nil")
	}
	_, err = NewSlackBriefDeliverer(cfg, newFakeSlackRepo(), &stubChat{}, &fakeLimiter{}, nil)
	if err == nil {
		t.Error("expected error when cipher is nil")
	}
	// limiter nil → defaults in, no error
	if _, err := NewSlackBriefDeliverer(cfg, newFakeSlackRepo(), &stubChat{}, nil, identityDecrypter{}); err != nil {
		t.Errorf("nil limiter should default, got %v", err)
	}
}

// ==================== Happy path ====================

func TestSlackBriefDeliverer_Deliver_HappyPath(t *testing.T) {
	t.Parallel()
	dc, d, _, chat, limiter := sampleSlackContext(t)

	ts, err := d.Deliver(context.Background(), dc)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if ts != "1700000000.000001" {
		t.Errorf("ts = %q", ts)
	}
	if chat.called != 1 {
		t.Errorf("expected 1 PostMessage call, got %d", chat.called)
	}
	if len(limiter.calls) != 1 || limiter.calls[0] != "T1" {
		t.Errorf("rate limiter not called with team id, got %v", limiter.calls)
	}

	// Fallback text includes meeting title.
	if !strings.Contains(chat.lastArgs.text, "Haftalık Product Sync") {
		t.Errorf("fallback text missing title: %q", chat.lastArgs.text)
	}

	// Block Kit JSON carries title + brief content + dashboard URL.
	bk := string(chat.lastArgs.blocks)
	for _, want := range []string{
		"Haftalık Product Sync",
		"API v2",
		dcExpectedDashboardURL(dc),
		"Toplantı briefiniz hazır",
	} {
		if !strings.Contains(bk, want) {
			t.Errorf("Block Kit JSON missing %q\n%s", want, bk)
		}
	}

	// Channel id passed verbatim.
	if chat.lastArgs.channelID != "D1" {
		t.Errorf("channel id mismatch: %q", chat.lastArgs.channelID)
	}
}

func dcExpectedDashboardURL(dc BriefDeliverContext) string {
	return "https://console.syncrun.net/meetings/" + dc.Meeting.ID.String() + "/brief"
}

// ==================== Skip paths ====================

func TestSlackBriefDeliverer_Deliver_NoIntegration_Skipped(t *testing.T) {
	t.Parallel()

	dc, _, _, _, _ := sampleSlackContext(t)
	// Different user id → no integration for them.
	dc.User = &models.User{ID: uuid.New(), Email: "other@example.com"}

	repo := newFakeSlackRepo()
	d, _ := NewSlackBriefDeliverer(
		SlackBriefDelivererConfig{}, repo, &stubChat{}, &fakeLimiter{}, identityDecrypter{},
	)

	_, err := d.Deliver(context.Background(), dc)
	if !errors.Is(err, ErrNoActiveIntegration) {
		t.Errorf("expected ErrNoActiveIntegration, got %v", err)
	}
}

func TestSlackBriefDeliverer_Deliver_PendingEmail_Skipped(t *testing.T) {
	t.Parallel()

	repo := newFakeSlackRepo()
	chat := &stubChat{}
	d, _ := NewSlackBriefDeliverer(
		SlackBriefDelivererConfig{}, repo, chat, &fakeLimiter{}, identityDecrypter{},
	)

	// Integration exists but MatchedEmail is nil — user hasn't
	// completed the alternative-email flow yet.
	userID := uuid.New()
	_ = repo.Upsert(context.Background(), &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: "xoxb", Scope: "s",
	})

	dc := BriefDeliverContext{
		Meeting: &models.Meeting{ID: uuid.New(), Title: "X", StartTime: time.Now()},
		Brief:   &models.MeetingBrief{Content: "x"},
		User:    &models.User{ID: userID, Email: "user@example.com"},
	}
	_, err := d.Deliver(context.Background(), dc)
	if !errors.Is(err, ErrIntegrationPendingEmail) {
		t.Errorf("expected ErrIntegrationPendingEmail, got %v", err)
	}
	if chat.called != 0 {
		t.Error("chat must not be called when integration is pending email")
	}
}

func TestSlackBriefDeliverer_Deliver_CipherFails_RevokesAndSkips(t *testing.T) {
	t.Parallel()

	repo := newFakeSlackRepo()
	userID := uuid.New()
	dmID := "D1"
	email := "user@example.com"
	integ := &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: "corrupt-ciphertext",
		DMChannelID:       &dmID, MatchedEmail: &email, Scope: "chat:write",
	}
	_ = repo.Upsert(context.Background(), integ)

	chat := &stubChat{}
	d, _ := NewSlackBriefDeliverer(
		SlackBriefDelivererConfig{}, repo, chat, &fakeLimiter{},
		failDecrypter{err: slackoauth.ErrInvalidCiphertext},
	)

	dc := BriefDeliverContext{
		Meeting: &models.Meeting{ID: uuid.New(), Title: "X", StartTime: time.Now()},
		Brief:   &models.MeetingBrief{Content: "x"},
		User:    &models.User{ID: userID, Email: "u"},
	}
	_, err := d.Deliver(context.Background(), dc)
	if !errors.Is(err, ErrNoActiveIntegration) {
		t.Errorf("cipher failure should map to ErrNoActiveIntegration, got %v", err)
	}
	if len(repo.revokedIDs) != 1 {
		t.Errorf("expected integration to be revoked, got %d revokes", len(repo.revokedIDs))
	}
	if chat.called != 0 {
		t.Error("chat must not be called after cipher failure")
	}
}

// ==================== Invalid auth → auto-revoke ====================

func TestSlackBriefDeliverer_Deliver_InvalidAuth_AutoRevokes(t *testing.T) {
	t.Parallel()

	dc, d, repo, chat, _ := sampleSlackContext(t)
	chat.err = slackoauth.ErrSlackInvalidAuth

	_, err := d.Deliver(context.Background(), dc)
	if !errors.Is(err, ErrNoActiveIntegration) {
		t.Errorf("invalid_auth should skip with ErrNoActiveIntegration, got %v", err)
	}
	if len(repo.revokedIDs) != 1 {
		t.Errorf("expected auto-revoke, got %d revokes", len(repo.revokedIDs))
	}
}

// ==================== Retryable classifications ====================

func TestSlackBriefDeliverer_Deliver_RateLimited_Retryable(t *testing.T) {
	t.Parallel()

	dc, d, _, chat, _ := sampleSlackContext(t)
	chat.err = slackoauth.ErrSlackRateLimited

	_, err := d.Deliver(context.Background(), dc)
	if !IsRetryable(err) {
		t.Errorf("rate-limit should be retryable, got %v", err)
	}
}

func TestSlackBriefDeliverer_Deliver_TransportError_Retryable(t *testing.T) {
	t.Parallel()

	dc, d, _, chat, _ := sampleSlackContext(t)
	// Wrap in the slackoauth transient chain via fmt.Errorf %w — we
	// simulate this since the real slackTransportError type is package-
	// private. ErrSlackTransport is the public sentinel for matching.
	chat.err = fmt.Errorf("network glitch: %w", slackoauth.ErrSlackTransport)

	_, err := d.Deliver(context.Background(), dc)
	if !IsRetryable(err) {
		t.Errorf("transport error should be retryable, got %v", err)
	}
}

func TestSlackBriefDeliverer_Deliver_RateLimiterContextCanceled_Retryable(t *testing.T) {
	t.Parallel()

	dc, d, _, _, limiter := sampleSlackContext(t)
	limiter.err = context.Canceled

	_, err := d.Deliver(context.Background(), dc)
	if !IsRetryable(err) {
		t.Errorf("limiter ctx cancel should be retryable (deferred send), got %v", err)
	}
}

// ==================== Permanent slack error ====================

func TestSlackBriefDeliverer_Deliver_PermanentSlackError_Surfaces(t *testing.T) {
	t.Parallel()

	dc, d, _, chat, _ := sampleSlackContext(t)
	chat.err = errors.New("slackoauth: chat.postMessage refused: channel_not_found")

	_, err := d.Deliver(context.Background(), dc)
	if err == nil {
		t.Fatal("expected permanent error to bubble up")
	}
	if IsRetryable(err) {
		t.Errorf("channel_not_found must not be retryable, got %v", err)
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("expected Slack reason in error, got %v", err)
	}
}

// ==================== Block Kit rendering ====================

func TestRenderBriefSlackPayload_ContainsExpectedBlocks(t *testing.T) {
	t.Parallel()

	dc := BriefDeliverContext{
		Meeting: &models.Meeting{
			ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
			Title: "<script>alert(1)</script>",
			StartTime: time.Date(2026, 5, 2, 14, 30, 0, 0, time.UTC),
		},
		Brief: &models.MeetingBrief{Content: "Test brief content"},
		Profile: &models.Profile{Timezone: "UTC"},
	}

	raw, fallback, err := renderBriefSlackPayload(dc, "https://console.syncrun.net")
	if err != nil {
		t.Fatal(err)
	}

	// 4 blocks: header, section/fields, section/text, actions.
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("block JSON invalid: %v", err)
	}
	if len(blocks) != 4 {
		t.Errorf("expected 4 blocks, got %d", len(blocks))
	}

	// Mrkdwn escape: after JSON unmarshal we get the raw string content.
	// Go's json.Marshal HTML-escapes < > & when emitting, but after
	// Unmarshal the string is the logical value — so we assert on that.
	fields, _ := blocks[1]["fields"].([]any)
	if len(fields) == 0 {
		t.Fatal("expected fields in second block")
	}
	firstField, _ := fields[0].(map[string]any)
	titleText, _ := firstField["text"].(string)
	if strings.Contains(titleText, "<script>") {
		t.Errorf("unescaped mrkdwn — XSS-equivalent leak: %s", titleText)
	}
	if !strings.Contains(titleText, "&lt;script&gt;") {
		t.Errorf("expected escaped form, got %s", titleText)
	}

	// Dashboard URL present (in the raw JSON this time — URL isn't
	// escape-ambiguous).
	s := string(raw)
	if !strings.Contains(s, "/meetings/11111111-1111-1111-1111-111111111111/brief") {
		t.Error("dashboard URL missing from blocks")
	}

	// Fallback text for notification previews.
	if !strings.Contains(fallback, "briefiniz hazır") {
		t.Errorf("fallback text missing, got %q", fallback)
	}
}

func TestRenderBriefSlackPayload_TruncatesLongContent(t *testing.T) {
	t.Parallel()

	dc := BriefDeliverContext{
		Meeting: &models.Meeting{ID: uuid.New(), Title: "X", StartTime: time.Now()},
		Brief:   &models.MeetingBrief{Content: strings.Repeat("X", slackSectionTextMax+500)},
		Profile: &models.Profile{Timezone: "UTC"},
	}
	raw, _, err := renderBriefSlackPayload(dc, "https://c")
	if err != nil {
		t.Fatal(err)
	}

	// Ellipsis marker present; raw size comfortably under Slack's
	// 3000-char cap per mrkdwn text field.
	if !strings.Contains(string(raw), "…") {
		t.Error("expected ellipsis after truncation")
	}
}

func TestRenderBriefSlackPayload_EmptyTitleFallsBack(t *testing.T) {
	t.Parallel()
	dc := BriefDeliverContext{
		Meeting: &models.Meeting{ID: uuid.New(), StartTime: time.Now()},
		Brief:   &models.MeetingBrief{Content: "x"},
	}
	_, fallback, err := renderBriefSlackPayload(dc, "https://c")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fallback, "Toplantı") {
		t.Errorf("expected fallback title, got %q", fallback)
	}
}

// ==================== escapeSlackMrkdwn ====================

func TestEscapeSlackMrkdwn(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"plain":               "plain",
		"<foo>":               "&lt;foo&gt;",
		"a & b":               "a &amp; b",
		"<a>&<b>":             "&lt;a&gt;&amp;&lt;b&gt;",
		"kept *bold* markers": "kept *bold* markers",
	}
	for in, want := range cases {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			if got := escapeSlackMrkdwn(in); got != want {
				t.Errorf("escapeSlackMrkdwn(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
