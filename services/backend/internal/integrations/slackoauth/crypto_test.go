package slackoauth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// testKey returns a fresh 32-byte hex key for deterministic-length
// round-trip tests without baking a constant into the source.
func testKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ==================== Constructor ====================

func TestNewTokenCipher_RejectsBadLength(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"too short": hex.EncodeToString(make([]byte, 16)),
		"too long":  hex.EncodeToString(make([]byte, 48)),
		"not hex":   "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg",
		"empty":     "",
	}
	for name, key := range cases {
		key := key
		t.Run(name, func(t *testing.T) {
			_, err := NewTokenCipher(key)
			if !errors.Is(err, ErrInvalidEncKey) {
				t.Errorf("expected ErrInvalidEncKey for %s, got %v", name, err)
			}
		})
	}
}

func TestNewTokenCipher_Success(t *testing.T) {
	t.Parallel()
	if _, err := NewTokenCipher(testKey(t)); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

// ==================== Round-trip ====================

func TestTokenCipher_EncryptDecrypt_RoundTrip(t *testing.T) {
	t.Parallel()
	c, _ := NewTokenCipher(testKey(t))
	plaintext := "xoxb-1234-5678-abcdefghijklmnop"

	ct, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ct == plaintext {
		t.Error("ciphertext must not equal plaintext")
	}
	// No token fragments leak.
	if strings.Contains(ct, "xoxb-") {
		t.Error("ciphertext appears to contain raw token prefix")
	}

	got, err := c.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plaintext {
		t.Errorf("round-trip mismatch: got %q", got)
	}
}

func TestTokenCipher_Encrypt_EmptyPlaintextRejected(t *testing.T) {
	t.Parallel()
	c, _ := NewTokenCipher(testKey(t))
	if _, err := c.Encrypt(""); err == nil {
		t.Error("expected error on empty plaintext")
	}
}

// ==================== Integrity + cross-key isolation ====================

func TestTokenCipher_TamperedCiphertext_Rejected(t *testing.T) {
	t.Parallel()
	c, _ := NewTokenCipher(testKey(t))
	ct, _ := c.Encrypt("xoxb-test")

	// Swap a character in the middle of the base64 payload.
	bad := []byte(ct)
	mid := len(bad) / 2
	if bad[mid] == 'A' {
		bad[mid] = 'B'
	} else {
		bad[mid] = 'A'
	}
	if _, err := c.Decrypt(string(bad)); !errors.Is(err, ErrInvalidCiphertext) {
		t.Errorf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestTokenCipher_DifferentKey_CannotDecrypt(t *testing.T) {
	t.Parallel()
	a, _ := NewTokenCipher(testKey(t))
	b, _ := NewTokenCipher(testKey(t))
	ct, _ := a.Encrypt("xoxb-test")

	if _, err := b.Decrypt(ct); !errors.Is(err, ErrInvalidCiphertext) {
		t.Errorf("key B must not decrypt A's ciphertext, got %v", err)
	}
}

func TestTokenCipher_MalformedInput_Rejected(t *testing.T) {
	t.Parallel()
	c, _ := NewTokenCipher(testKey(t))
	cases := []string{"", "not-base64!!", "QUFB"} // empty / bad b64 / too-short
	for _, in := range cases {
		in := in
		t.Run(in, func(t *testing.T) {
			if _, err := c.Decrypt(in); !errors.Is(err, ErrInvalidCiphertext) {
				t.Errorf("expected ErrInvalidCiphertext for %q, got %v", in, err)
			}
		})
	}
}

// ==================== Nonce randomness ====================

func TestTokenCipher_EncryptProducesDistinctCiphertexts(t *testing.T) {
	t.Parallel()
	// Same plaintext, same key → MUST yield different ciphertexts
	// because AES-GCM uses a random nonce. If this ever fails, it
	// means the nonce RNG is broken (catastrophic for GCM).
	c, _ := NewTokenCipher(testKey(t))
	ct1, _ := c.Encrypt("same plaintext")
	ct2, _ := c.Encrypt("same plaintext")
	if ct1 == ct2 {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext — nonce reuse")
	}
}
