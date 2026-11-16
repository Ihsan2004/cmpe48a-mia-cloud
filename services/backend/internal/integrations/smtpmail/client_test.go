package smtpmail

import (
	"context"
	"errors"
	"net/textproto"
	"testing"

	"mia-website/backend/internal/services/briefnotify"
)

// ==================== Config.Validate ====================

func TestConfig_Validate_Success(t *testing.T) {
	t.Parallel()
	cfg := Config{Host: "smtp.example.com", Port: 587, AuthUser: "a@example.com", AuthPassword: "x"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfig_Validate_MissingFields(t *testing.T) {
	t.Parallel()
	cases := map[string]Config{
		"host":     {Port: 587, AuthUser: "a@b", AuthPassword: "x"},
		"port":     {Host: "h", AuthUser: "a@b", AuthPassword: "x"},
		"authUser": {Host: "h", Port: 587, AuthPassword: "x"},
		"authPwd":  {Host: "h", Port: 587, AuthUser: "a@b"},
	}
	for name, cfg := range cases {
		cfg := cfg
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Errorf("expected validation error for missing %s", name)
			}
		})
	}
}

// ==================== Client.Send validation ====================

func TestClient_Send_ValidatesPayload(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Host: "h", Port: 587, AuthUser: "a@b", AuthPassword: "x"})
	if err != nil {
		t.Fatal(err)
	}
	c.WithSender(func(ctx context.Context, _ Config, _ Payload) error { return nil })

	if _, err := c.Send(context.Background(), Payload{}); err == nil {
		t.Error("expected error on empty From")
	}
	if _, err := c.Send(context.Background(), Payload{From: "a@b"}); err == nil {
		t.Error("expected error on empty To")
	}
	if _, err := c.Send(context.Background(), Payload{From: "a@b", To: []string{"c@d"}}); err == nil {
		t.Error("expected error on empty Message")
	}
}

// ==================== Client.Send happy path ====================

func TestClient_Send_ReturnsMessageIDOnSuccess(t *testing.T) {
	t.Parallel()

	called := false
	c, _ := New(Config{Host: "h", Port: 587, AuthUser: "a@b", AuthPassword: "x"})
	c.WithSender(func(ctx context.Context, cfg Config, p Payload) error {
		called = true
		if p.From != "sender@x.com" {
			t.Errorf("From not propagated: %s", p.From)
		}
		if len(p.To) != 1 || p.To[0] != "rcpt@x.com" {
			t.Errorf("To not propagated: %v", p.To)
		}
		return nil
	})

	msgID, err := c.Send(context.Background(), Payload{
		From:      "sender@x.com",
		To:        []string{"rcpt@x.com"},
		Message:   []byte("Subject: hi\r\n\r\nbody"),
		MessageID: "<abc@syncrun.net>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("sender was not invoked")
	}
	if msgID != "<abc@syncrun.net>" {
		t.Errorf("expected MessageID passthrough, got %q", msgID)
	}
}

// ==================== Error classification ====================

func TestClassifyError_Nil(t *testing.T) {
	t.Parallel()
	if got := ClassifyError(nil); got != nil {
		t.Errorf("ClassifyError(nil) = %v", got)
	}
}

func TestClassifyError_4xx_IsRetryable(t *testing.T) {
	t.Parallel()
	// Typical 4xx codes we want retried: 421 (service not available),
	// 450 (mailbox busy), 451 (greylisted), 452 (over quota).
	codes := []int{421, 450, 451, 452}
	for _, code := range codes {
		code := code
		t.Run("code", func(t *testing.T) {
			err := ClassifyError(&textproto.Error{Code: code, Msg: "try later"})
			if !briefnotify.IsRetryable(err) {
				t.Errorf("4xx=%d must be retryable, got %v", code, err)
			}
		})
	}
}

func TestClassifyError_5xx_NotRetryable(t *testing.T) {
	t.Parallel()
	// 550 (no such user), 552 (message too large), 553 (bad address),
	// 554 (transaction failed) — all permanent.
	codes := []int{550, 552, 553, 554}
	for _, code := range codes {
		code := code
		t.Run("code", func(t *testing.T) {
			err := ClassifyError(&textproto.Error{Code: code, Msg: "nope"})
			if err == nil {
				t.Fatal("5xx must surface as a non-nil error")
			}
			if briefnotify.IsRetryable(err) {
				t.Errorf("5xx=%d must NOT be retryable, got %v", code, err)
			}
		})
	}
}

func TestClassifyError_NetworkError_Retryable(t *testing.T) {
	t.Parallel()
	// Wrapped network errors (dial failed, TLS handshake glitch) are
	// transient — always retry once. The %w chain must be preserved so
	// the worker can still log the root cause.
	root := errors.New("dial tcp: i/o timeout")
	err := ClassifyError(root)
	if !briefnotify.IsRetryable(err) {
		t.Errorf("network error must be retryable: %v", err)
	}
	if !errors.Is(err, root) {
		t.Error("Retryable(err) must preserve errors.Is chain")
	}
}

// ==================== Send -> ClassifyError integration ====================

func TestClient_Send_Wraps4xxAsRetryable(t *testing.T) {
	t.Parallel()
	c, _ := New(Config{Host: "h", Port: 587, AuthUser: "a@b", AuthPassword: "x"})
	c.WithSender(func(ctx context.Context, _ Config, _ Payload) error {
		return &textproto.Error{Code: 421, Msg: "greylisted"}
	})

	_, err := c.Send(context.Background(), Payload{
		From:    "a@b",
		To:      []string{"c@d"},
		Message: []byte("subj\r\n\r\nbody"),
	})
	if err == nil || !briefnotify.IsRetryable(err) {
		t.Fatalf("expected retryable error from 421, got %v", err)
	}
}

func TestClient_Send_Wraps5xxAsPermanent(t *testing.T) {
	t.Parallel()
	c, _ := New(Config{Host: "h", Port: 587, AuthUser: "a@b", AuthPassword: "x"})
	c.WithSender(func(ctx context.Context, _ Config, _ Payload) error {
		return &textproto.Error{Code: 550, Msg: "no such user"}
	})

	_, err := c.Send(context.Background(), Payload{
		From:    "a@b",
		To:      []string{"c@d"},
		Message: []byte("subj\r\n\r\nbody"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if briefnotify.IsRetryable(err) {
		t.Errorf("5xx must not be retryable: %v", err)
	}
}

// ==================== Helo helper ====================

func TestClientHello_PrefersAuthUserDomain(t *testing.T) {
	t.Parallel()
	if got := clientHello(Config{AuthUser: "admin@syncrun.net"}); got != "syncrun.net" {
		t.Errorf("clientHello admin@syncrun.net -> %q, want syncrun.net", got)
	}
	if got := clientHello(Config{AuthUser: ""}); got != "localhost" {
		t.Errorf("clientHello '' -> %q, want localhost", got)
	}
}
