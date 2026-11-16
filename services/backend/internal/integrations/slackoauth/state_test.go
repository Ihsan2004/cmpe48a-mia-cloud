package slackoauth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const stateTestSecret = "slack-state-secret-32-bytes-long-test-only"

// ==================== Constructor ====================

func TestNewStateSigner_RequiresLongSecret(t *testing.T) {
	t.Parallel()
	if _, err := NewStateSigner("short"); err == nil {
		t.Error("expected error for short secret")
	}
	if _, err := NewStateSigner(stateTestSecret); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

// ==================== Round-trip ====================

func TestStateSigner_RoundTrip(t *testing.T) {
	t.Parallel()
	s, _ := NewStateSigner(stateTestSecret)
	userID := uuid.New()

	token := s.Generate(userID)
	if token == "" {
		t.Fatal("empty token")
	}
	// URL-safe base64 → no characters that'd need query-escaping.
	for _, bad := range []string{"+", "/", "="} {
		if strings.Contains(token, bad) {
			t.Errorf("token contains URL-unsafe char %q: %s", bad, token)
		}
	}

	got, err := s.Validate(token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != userID {
		t.Errorf("uuid mismatch: %s vs %s", got, userID)
	}
}

func TestStateSigner_NilUUID_EmptyToken(t *testing.T) {
	t.Parallel()
	s, _ := NewStateSigner(stateTestSecret)
	if got := s.Generate(uuid.Nil); got != "" {
		t.Errorf("nil uuid should yield empty token, got %q", got)
	}
}

// ==================== Signature validation ====================

func TestStateSigner_TamperedSignature_Rejected(t *testing.T) {
	t.Parallel()
	s, _ := NewStateSigner(stateTestSecret)
	tok := s.Generate(uuid.New())
	bad := []byte(tok)
	bad[len(bad)-3] ^= 0x01
	if _, err := s.Validate(string(bad)); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("expected ErrStateInvalid for tampered MAC, got %v", err)
	}
}

func TestStateSigner_WrongSecret_Rejected(t *testing.T) {
	t.Parallel()
	a, _ := NewStateSigner(stateTestSecret)
	b, _ := NewStateSigner("different-state-secret-32-chars-test-only")
	tok := a.Generate(uuid.New())
	if _, err := b.Validate(tok); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("token from signer A must fail under signer B, got %v", err)
	}
}

func TestStateSigner_Malformed_Rejected(t *testing.T) {
	t.Parallel()
	s, _ := NewStateSigner(stateTestSecret)
	cases := []string{"", "!!!!", "short", strings.Repeat("A", 75)}
	for _, in := range cases {
		in := in
		t.Run(in, func(t *testing.T) {
			if _, err := s.Validate(in); !errors.Is(err, ErrStateInvalid) {
				t.Errorf("expected ErrStateInvalid for %q, got %v", in, err)
			}
		})
	}
}

// ==================== Expiry ====================

func TestStateSigner_ExpiredState_Rejected(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	s, _ := NewStateSigner(stateTestSecret)
	s.now = func() time.Time { return fixed.Add(-20 * time.Minute) }
	tok := s.Generate(uuid.New())

	s.now = func() time.Time { return fixed }
	if _, err := s.Validate(tok); !errors.Is(err, ErrStateExpired) {
		t.Errorf("expected ErrStateExpired for 20-minute-old state, got %v", err)
	}
}

func TestStateSigner_JustInsideExpiry_Accepted(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	s, _ := NewStateSigner(stateTestSecret)
	userID := uuid.New()
	s.now = func() time.Time { return fixed.Add(-14 * time.Minute) }
	tok := s.Generate(userID)

	s.now = func() time.Time { return fixed }
	got, err := s.Validate(tok)
	if err != nil {
		t.Fatalf("14-minute-old state must validate, got %v", err)
	}
	if got != userID {
		t.Error("userID mismatch at expiry boundary")
	}
}

func TestStateSigner_FutureDated_Rejected(t *testing.T) {
	t.Parallel()
	s, _ := NewStateSigner(stateTestSecret)
	s.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	tok := s.Generate(uuid.New())

	s.now = func() time.Time { return time.Now() }
	if _, err := s.Validate(tok); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("future-dated state must fail, got %v", err)
	}
}
