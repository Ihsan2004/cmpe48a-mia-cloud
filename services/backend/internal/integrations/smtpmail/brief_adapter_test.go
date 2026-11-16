package smtpmail

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"mia-website/backend/internal/services/briefnotify"
)

// ==================== MIME composition ====================

func TestComposeMIME_StructureAndHeaders(t *testing.T) {
	t.Parallel()

	payload := briefnotify.EmailPayload{
		From:      `"Syncrun" <brief@syncrun.net>`,
		To:        []string{"user@example.com"},
		Subject:   "Test subject",
		HTML:      "<p>merhaba dünya</p>",
		Text:      "merhaba dünya",
		MessageID: "<abc@syncrun.net>",
		ExtraHeaders: map[string]string{
			"Reply-To":         "brief@syncrun.net",
			"List-Unsubscribe": "<https://api.syncrun.net/api/unsubscribe?token=x>",
		},
	}

	msg, err := composeMIME(payload)
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)

	// Core RFC 5322 headers.
	checks := []string{
		"From: \"Syncrun\" <brief@syncrun.net>",
		"To: user@example.com",
		"Subject: Test subject",
		"Message-Id: <abc@syncrun.net>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative",
		"Reply-To: brief@syncrun.net",
		"List-Unsubscribe: <https://api.syncrun.net/api/unsubscribe?token=x>",
	}
	for _, want := range checks {
		if !strings.Contains(s, want) {
			t.Errorf("message missing header: %q", want)
		}
	}

	// Both body parts present.
	if !strings.Contains(s, "Content-Type: text/plain; charset=UTF-8") {
		t.Error("missing text/plain part")
	}
	if !strings.Contains(s, "Content-Type: text/html; charset=UTF-8") {
		t.Error("missing text/html part")
	}
	if !strings.Contains(s, "Content-Transfer-Encoding: quoted-printable") {
		t.Error("missing QP encoding")
	}

	// Turkish character must be quoted-printable encoded (ü = =C3=BC).
	if !strings.Contains(s, "=C3=BC") {
		t.Errorf("expected Turkish char to be QP-encoded (=C3=BC), got: %s", s)
	}

	// Closing boundary present.
	if !strings.Contains(s, "--====syncrun-") || !strings.HasSuffix(strings.TrimSpace(s), "====--") {
		t.Error("missing closing multipart boundary")
	}

	// CRLF only (no bare LF) — spot-check first 200 chars.
	head := msg[:200]
	if bytes.Contains(head, []byte("\n")) && !bytes.Contains(head, []byte("\r\n")) {
		t.Error("headers must use CRLF line endings")
	}
}

func TestComposeMIME_DropsHijackedContentType(t *testing.T) {
	t.Parallel()
	// If the caller tries to override Content-Type via ExtraHeaders
	// we ignore it — the envelope CT is ours to own (boundary is tied
	// to the body we compose).
	msg, err := composeMIME(briefnotify.EmailPayload{
		From: "a@b", To: []string{"c@d"}, Subject: "x", HTML: "<p>hi</p>", Text: "hi",
		ExtraHeaders: map[string]string{"Content-Type": "text/plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	if !strings.Contains(s, "multipart/alternative") {
		t.Error("outer Content-Type must stay multipart/alternative")
	}
	if strings.Count(s, "Content-Type: ") != 3 {
		// expect exactly: outer multipart + text part + html part = 3
		t.Errorf("expected 3 Content-Type lines, got %d", strings.Count(s, "Content-Type: "))
	}
}

// ==================== Envelope address extraction ====================

func TestExtractAddress_StripsDisplayName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`"Syncrun" <brief@syncrun.net>`: "brief@syncrun.net",
		`brief@syncrun.net`:             "brief@syncrun.net",
		`Brief <brief@syncrun.net>`:     "brief@syncrun.net",
	}
	for in, want := range cases {
		if got := extractAddress(in); got != want {
			t.Errorf("extractAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

// ==================== BriefAdapter integration ====================

func TestBriefAdapter_Send_DelegatesToClient(t *testing.T) {
	t.Parallel()

	client, err := New(Config{Host: "h", Port: 587, AuthUser: "a@b", AuthPassword: "x"})
	if err != nil {
		t.Fatal(err)
	}

	var captured Payload
	client.WithSender(func(ctx context.Context, cfg Config, p Payload) error {
		captured = p
		return nil
	})

	adapter := NewBriefAdapter(client)
	msgID, err := adapter.Send(context.Background(), briefnotify.EmailPayload{
		From:      `"Syncrun" <brief@syncrun.net>`,
		To:        []string{"user@example.com"},
		Subject:   "Hello",
		HTML:      "<p>hi</p>",
		Text:      "hi",
		MessageID: "<abc@syncrun.net>",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msgID != "<abc@syncrun.net>" {
		t.Errorf("msgID = %q, want <abc@syncrun.net>", msgID)
	}

	// Envelope from must be the bare address, not the display-name form.
	if captured.From != "brief@syncrun.net" {
		t.Errorf("envelope From = %q, want bare address", captured.From)
	}
	if len(captured.To) != 1 || captured.To[0] != "user@example.com" {
		t.Errorf("envelope To = %v", captured.To)
	}
	if len(captured.Message) == 0 {
		t.Error("message bytes empty")
	}
}

func TestBriefAdapter_Send_ValidatesPayload(t *testing.T) {
	t.Parallel()
	client, _ := New(Config{Host: "h", Port: 587, AuthUser: "a@b", AuthPassword: "x"})
	client.WithSender(func(ctx context.Context, _ Config, _ Payload) error { return nil })
	adapter := NewBriefAdapter(client)

	if _, err := adapter.Send(context.Background(), briefnotify.EmailPayload{To: []string{"a@b"}}); err == nil {
		t.Error("expected error when From is empty")
	}
	if _, err := adapter.Send(context.Background(), briefnotify.EmailPayload{From: "a@b"}); err == nil {
		t.Error("expected error when To is empty")
	}
}

func TestNewBriefAdapter_NilClientStillConstructable(t *testing.T) {
	t.Parallel()
	// Constructor accepts nil so tests can exercise the nil-guard path;
	// Send itself rejects the nil client.
	adapter := NewBriefAdapter(nil)
	if _, err := adapter.Send(context.Background(), briefnotify.EmailPayload{From: "a@b", To: []string{"c@d"}}); err == nil {
		t.Error("expected error when adapter has nil client")
	}
}
