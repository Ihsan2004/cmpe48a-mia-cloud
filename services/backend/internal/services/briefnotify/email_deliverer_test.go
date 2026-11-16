package briefnotify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== Fake transport ====================

type captureTransport struct {
	lastPayload EmailPayload
	err         error
	msgID       string
}

func (c *captureTransport) Send(ctx context.Context, p EmailPayload) (string, error) {
	c.lastPayload = p
	if c.err != nil {
		return "", c.err
	}
	if c.msgID != "" {
		return c.msgID, nil
	}
	return p.MessageID, nil
}

// ==================== Fixture helpers ====================

func sampleContext(t *testing.T) (BriefDeliverContext, *captureTransport, *EmailBriefDeliverer) {
	t.Helper()

	transport := &captureTransport{}
	d, err := NewEmailBriefDeliverer(EmailBriefDelivererConfig{
		FromAddress:        "brief@syncrun.net",
		FromDisplay:        "Syncrun",
		ReplyTo:            "brief@syncrun.net",
		DashboardBaseURL:   "https://console.syncrun.net",
		UnsubscribeBaseURL: "https://api.syncrun.net",
	}, transport)
	if err != nil {
		t.Fatalf("NewEmailBriefDeliverer: %v", err)
	}
	// Deterministic token + clock for stable assertions.
	d.unsubscribeTokenFn = func(userID uuid.UUID) string { return "test-token-" + userID.String()[:8] }
	d.clock = func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }

	meetingID := uuid.New()
	userID := uuid.New()
	dc := BriefDeliverContext{
		Delivery: &models.BriefDelivery{ID: uuid.New(), MeetingID: meetingID, UserID: userID, Channel: models.BriefDeliveryChannelEmail},
		Meeting: &models.Meeting{
			ID:        meetingID,
			UserID:    userID,
			Title:     "Haftalık Product Sync",
			StartTime: time.Date(2026, 5, 2, 14, 30, 0, 0, time.UTC),
			Attendees: []models.Attendee{
				{Email: "alice@example.com", DisplayName: "Alice"},
				{Email: "bob@example.com"},
			},
		},
		Brief: &models.MeetingBrief{
			ID:              uuid.New(),
			UserID:          userID,
			TargetMeetingID: meetingID,
			Content:         "Son toplantıda API v2 kararı alındı.",
		},
		User: &models.User{ID: userID, Email: "user@example.com"},
		Profile: &models.Profile{
			ID:                userID,
			Timezone:          "Europe/Istanbul",
			InterfaceLanguage: "tr",
		},
	}
	return dc, transport, d
}

// ==================== Name ====================

func TestEmailBriefDeliverer_Name(t *testing.T) {
	t.Parallel()
	_, _, d := sampleContext(t)
	if got := d.Name(); got != models.BriefDeliveryChannelEmail {
		t.Errorf("Name() = %q, want %q", got, models.BriefDeliveryChannelEmail)
	}
}

// ==================== Happy path ====================

func TestEmailBriefDeliverer_Deliver_RendersAndSends(t *testing.T) {
	t.Parallel()
	dc, transport, d := sampleContext(t)

	msgID, err := d.Deliver(context.Background(), dc)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// Provider message id echoes the Message-Id we generated.
	if msgID == "" {
		t.Fatal("expected non-empty provider message id")
	}
	if !strings.HasPrefix(msgID, "<") || !strings.HasSuffix(msgID, "@syncrun.net>") {
		t.Errorf("expected <uuid@syncrun.net> message id, got %q", msgID)
	}
	if transport.lastPayload.MessageID != msgID {
		t.Errorf("transport MessageID mismatch: got %q, id %q", transport.lastPayload.MessageID, msgID)
	}

	// From header uses display + alias.
	if !strings.Contains(transport.lastPayload.From, "Syncrun") || !strings.Contains(transport.lastPayload.From, "brief@syncrun.net") {
		t.Errorf("From header wrong: %q", transport.lastPayload.From)
	}

	// Recipient pulled from user row.
	if len(transport.lastPayload.To) != 1 || transport.lastPayload.To[0] != "user@example.com" {
		t.Errorf("To header wrong: %v", transport.lastPayload.To)
	}

	// Subject is RFC-2047 encoded (Turkish em-dash character needs it).
	if !strings.Contains(transport.lastPayload.Subject, "briefiniz hazır") && !strings.Contains(transport.lastPayload.Subject, "=?utf-8?") {
		t.Errorf("Subject not encoded properly: %q", transport.lastPayload.Subject)
	}

	// List-Unsubscribe header is set (roadmap Karar #16).
	if unsub, ok := transport.lastPayload.ExtraHeaders["List-Unsubscribe"]; !ok || !strings.Contains(unsub, "api.syncrun.net") {
		t.Errorf("List-Unsubscribe header missing or wrong: %q (%t)", unsub, ok)
	}

	// HTML body contains meeting title + brief content.
	if !strings.Contains(transport.lastPayload.HTML, "Haftalık Product Sync") {
		t.Error("HTML body missing meeting title")
	}
	if !strings.Contains(transport.lastPayload.HTML, "API v2") {
		t.Error("HTML body missing brief content")
	}
	if !strings.Contains(transport.lastPayload.HTML, "test-token-") {
		t.Error("HTML body missing unsubscribe token")
	}

	// Text body exists in parallel (multipart/alternative plain fallback).
	if !strings.Contains(transport.lastPayload.Text, "API v2") {
		t.Error("Text body missing brief content")
	}

	// Dashboard CTA points to /meetings/<id>/brief (matches roadmap Karar).
	wantCTA := "/meetings/" + dc.Meeting.ID.String() + "/brief"
	if !strings.Contains(transport.lastPayload.HTML, wantCTA) {
		t.Errorf("Dashboard CTA missing %q in HTML", wantCTA)
	}
}

// ==================== Unsubscribed guard ====================

func TestEmailBriefDeliverer_Deliver_UnsubscribedUser_SkipSentinel(t *testing.T) {
	t.Parallel()
	dc, transport, d := sampleContext(t)
	now := time.Now()
	dc.Profile.EmailUnsubscribedAt = &now

	msgID, err := d.Deliver(context.Background(), dc)
	if !errors.Is(err, ErrEmailUnsubscribed) {
		t.Fatalf("expected ErrEmailUnsubscribed, got %v", err)
	}
	if msgID != "" {
		t.Errorf("msgID should be empty on skip, got %q", msgID)
	}
	if transport.lastPayload.Subject != "" {
		t.Error("transport must not be called when user is unsubscribed")
	}
}

// ==================== Missing user email ====================

func TestEmailBriefDeliverer_Deliver_NoEmail_Errors(t *testing.T) {
	t.Parallel()
	dc, _, d := sampleContext(t)
	dc.User.Email = ""

	if _, err := d.Deliver(context.Background(), dc); err == nil {
		t.Fatal("expected error when user.Email is empty")
	}
}

// ==================== Transport error propagation ====================

func TestEmailBriefDeliverer_Deliver_PropagatesTransportError(t *testing.T) {
	t.Parallel()
	dc, transport, d := sampleContext(t)
	transport.err = errors.New("smtp 421 greylisted")

	_, err := d.Deliver(context.Background(), dc)
	if err == nil {
		t.Fatal("expected transport error to bubble up")
	}
	if !strings.Contains(err.Error(), "421") {
		t.Errorf("error should carry transport message: %v", err)
	}
}

// ==================== participantsLine ====================

func TestParticipantsLine_EmptyList(t *testing.T) {
	t.Parallel()
	if got := participantsLine(nil); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestParticipantsLine_UsesDisplayNameFallbackEmail(t *testing.T) {
	t.Parallel()
	got := participantsLine([]models.Attendee{
		{DisplayName: "Alice"},
		{Email: "bob@example.com"},
	})
	if got != "Alice, bob@example.com" {
		t.Errorf("unexpected line: %q", got)
	}
}

func TestParticipantsLine_TruncatesAboveFive(t *testing.T) {
	t.Parallel()
	att := []models.Attendee{
		{DisplayName: "A"}, {DisplayName: "B"}, {DisplayName: "C"},
		{DisplayName: "D"}, {DisplayName: "E"}, {DisplayName: "F"}, {DisplayName: "G"},
	}
	got := participantsLine(att)
	if !strings.Contains(got, "+2 daha") {
		t.Errorf("expected truncation suffix, got %q", got)
	}
	// First 5 names visible.
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		if !strings.Contains(got, n) {
			t.Errorf("expected %q in truncated line: %s", n, got)
		}
	}
}

// ==================== Subject encoding ====================

func TestSubjectFor_EmptyTitle_Fallback(t *testing.T) {
	t.Parallel()
	got := subjectFor("")
	if !strings.Contains(got, "Toplantı") && !strings.Contains(got, "briefiniz") {
		t.Errorf("subject fallback missing: %q", got)
	}
}

// ==================== Message-Id host matches From domain ====================

func TestNewMessageID_UsesFromDomain(t *testing.T) {
	t.Parallel()
	id := newMessageID("brief@syncrun.net")
	if !strings.HasSuffix(id, "@syncrun.net>") {
		t.Errorf("expected Message-Id host=syncrun.net, got %q", id)
	}
	// Malformed input still produces SOME id with fallback host.
	id2 := newMessageID("not-an-address")
	if !strings.Contains(id2, "@syncrun.net>") {
		t.Errorf("expected fallback host for bad From, got %q", id2)
	}
}

// ==================== NewEmailBriefDeliverer validation ====================

func TestNewEmailBriefDeliverer_RequiresFromAddress(t *testing.T) {
	t.Parallel()
	_, err := NewEmailBriefDeliverer(EmailBriefDelivererConfig{}, &captureTransport{})
	if err == nil {
		t.Error("expected error when FromAddress is empty")
	}
}

func TestNewEmailBriefDeliverer_RequiresTransport(t *testing.T) {
	t.Parallel()
	_, err := NewEmailBriefDeliverer(EmailBriefDelivererConfig{FromAddress: "a@b"}, nil)
	if err == nil {
		t.Error("expected error when transport is nil")
	}
}

func TestNewEmailBriefDeliverer_DefaultsDisplayAndReplyTo(t *testing.T) {
	t.Parallel()
	d, err := NewEmailBriefDeliverer(EmailBriefDelivererConfig{FromAddress: "brief@syncrun.net"}, &captureTransport{})
	if err != nil {
		t.Fatal(err)
	}
	if d.cfg.FromDisplay != "Syncrun" {
		t.Errorf("default display name must be Syncrun, got %q", d.cfg.FromDisplay)
	}
	if d.cfg.ReplyTo != "brief@syncrun.net" {
		t.Errorf("default ReplyTo must equal FromAddress, got %q", d.cfg.ReplyTo)
	}
}
