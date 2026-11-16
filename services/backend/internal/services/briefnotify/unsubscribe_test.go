package briefnotify

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const validSecret = "unsubscribe-test-secret-that-is-long-enough-32"

// ==================== Constructor ====================

func TestNewUnsubscribeSigner_RequiresLongSecret(t *testing.T) {
	t.Parallel()
	if _, err := NewUnsubscribeSigner("short"); err == nil {
		t.Error("expected error for short secret")
	}
	if _, err := NewUnsubscribeSigner(validSecret); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

// ==================== Round-trip ====================

func TestUnsubscribeSigner_RoundTrip(t *testing.T) {
	t.Parallel()

	s, _ := NewUnsubscribeSigner(validSecret)
	userID := uuid.New()

	token := s.GenerateToken(userID)
	if token == "" {
		t.Fatal("GenerateToken returned empty string")
	}
	// URL-safe base64 → no +, /, or = characters.
	for _, bad := range []string{"+", "/", "="} {
		if strings.Contains(token, bad) {
			t.Errorf("token contains URL-unsafe char %q: %s", bad, token)
		}
	}

	got, err := s.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if got != userID {
		t.Errorf("round-trip mismatch: got %s, want %s", got, userID)
	}
}

func TestUnsubscribeSigner_NilUUID_EmptyToken(t *testing.T) {
	t.Parallel()
	s, _ := NewUnsubscribeSigner(validSecret)
	if got := s.GenerateToken(uuid.Nil); got != "" {
		t.Errorf("uuid.Nil should produce empty token, got %q", got)
	}
}

// ==================== Tampering ====================

func TestUnsubscribeSigner_TamperedSignature_Rejected(t *testing.T) {
	t.Parallel()

	s, _ := NewUnsubscribeSigner(validSecret)
	token := s.GenerateToken(uuid.New())

	// Flip a character somewhere in the last third (MAC region).
	bad := []byte(token)
	idx := len(bad) - 5
	if bad[idx] == 'A' {
		bad[idx] = 'B'
	} else {
		bad[idx] = 'A'
	}

	if _, err := s.ValidateToken(string(bad)); !errors.Is(err, ErrUnsubscribeTokenInvalid) {
		t.Errorf("expected ErrUnsubscribeTokenInvalid for tampered MAC, got %v", err)
	}
}

func TestUnsubscribeSigner_WrongSecret_Rejected(t *testing.T) {
	t.Parallel()

	s1, _ := NewUnsubscribeSigner(validSecret)
	s2, _ := NewUnsubscribeSigner("different-secret-that-is-long-enough-32!!")

	token := s1.GenerateToken(uuid.New())
	if _, err := s2.ValidateToken(token); !errors.Is(err, ErrUnsubscribeTokenInvalid) {
		t.Errorf("token signed with s1 must fail under s2, got %v", err)
	}
}

func TestUnsubscribeSigner_MalformedInput_Rejected(t *testing.T) {
	t.Parallel()

	s, _ := NewUnsubscribeSigner(validSecret)
	cases := map[string]string{
		"empty":       "",
		"bad base64":  "!!!!!",
		"too short":   "YWJjZGVm",                  // 6 bytes
		"right-ish length but random": strings.Repeat("A", 75),
	}
	for name, token := range cases {
		token := token
		t.Run(name, func(t *testing.T) {
			if _, err := s.ValidateToken(token); !errors.Is(err, ErrUnsubscribeTokenInvalid) {
				t.Errorf("%s must be rejected, got %v", name, err)
			}
		})
	}
}

// ==================== Expiry ====================

func TestUnsubscribeSigner_ExpiredToken_Rejected(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, _ := NewUnsubscribeSigner(validSecret)
	s.now = func() time.Time { return fixedNow.Add(-31 * 24 * time.Hour) }

	token := s.GenerateToken(uuid.New())

	// Validate "today" — token is 31 days old, roadmap allows 30.
	s.now = func() time.Time { return fixedNow }
	if _, err := s.ValidateToken(token); !errors.Is(err, ErrUnsubscribeTokenExpired) {
		t.Errorf("expected ErrUnsubscribeTokenExpired, got %v", err)
	}
}

func TestUnsubscribeSigner_JustInsideExpiry_Accepted(t *testing.T) {
	t.Parallel()

	fixedNow := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s, _ := NewUnsubscribeSigner(validSecret)
	userID := uuid.New()
	s.now = func() time.Time { return fixedNow.Add(-29 * 24 * time.Hour) }
	token := s.GenerateToken(userID)

	s.now = func() time.Time { return fixedNow }
	got, err := s.ValidateToken(token)
	if err != nil {
		t.Fatalf("29-day old token must validate, got %v", err)
	}
	if got != userID {
		t.Errorf("userID mismatch after expiry boundary test")
	}
}

func TestUnsubscribeSigner_FutureIssuedAt_Rejected(t *testing.T) {
	t.Parallel()

	s, _ := NewUnsubscribeSigner(validSecret)
	s.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	token := s.GenerateToken(uuid.New())

	s.now = func() time.Time { return time.Now() }
	if _, err := s.ValidateToken(token); !errors.Is(err, ErrUnsubscribeTokenInvalid) {
		t.Errorf("token from the future must fail, got %v", err)
	}
}
