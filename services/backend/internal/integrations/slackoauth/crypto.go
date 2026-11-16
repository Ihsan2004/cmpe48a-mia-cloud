// Package slackoauth handles the install / OAuth-code-exchange / email-
// resolution flow for the Syncrun Slack app. Responsibilities:
//
//  1. Encrypting bot tokens at rest (AES-GCM) so slack_integrations
//     rows can't leak plaintext tokens even with a DB snapshot.
//  2. Signing the OAuth `state` param so the callback can't be forged
//     into opting a third party into our app.
//  3. Calling Slack's oauth.v2.access + users.lookupByEmail +
//     conversations.open endpoints with injectable HTTP clients so
//     tests can run against httptest.
//
// The package does NOT persist anything; the handler layer drives the
// slack_integrations repository. Keeping crypto + Slack-protocol code
// isolated keeps the handler focused on request/response shaping and
// lets us unit-test the protocol layer without a Fiber app.
package slackoauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ErrInvalidEncKey + ErrInvalidCiphertext are returned for integrity
// problems that the caller can present differently (config issue vs.
// tampered DB row).
var (
	ErrInvalidEncKey     = errors.New("slackoauth: SLACK_TOKEN_ENC_KEY must decode to 32 bytes of hex")
	ErrInvalidCiphertext = errors.New("slackoauth: ciphertext is malformed or has been tampered with")
)

// TokenCipher wraps an AES-256-GCM AEAD. Instantiated once at boot
// from SLACK_TOKEN_ENC_KEY (32-byte hex → 32 raw bytes). Key rotation
// is out of scope for the POC (roadmap Karar #19) — a leaked key
// means re-issuing the enc key + invalidating every stored token.
type TokenCipher struct {
	aead cipher.AEAD
}

// NewTokenCipher parses a hex-encoded 32-byte key and returns a cipher
// ready for Encrypt/Decrypt calls. Validating the key at construction
// means a typo in the env var crashes the deploy, not the first OAuth
// callback in prod.
func NewTokenCipher(hexKey string) (*TokenCipher, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEncKey, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("%w: got %d bytes", ErrInvalidEncKey, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		// aes.NewCipher only rejects wrong-length keys — and we just
		// validated length — so this is a genuinely unexpected error.
		return nil, fmt.Errorf("slackoauth: aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("slackoauth: cipher.NewGCM: %w", err)
	}
	return &TokenCipher{aead: aead}, nil
}

// Encrypt returns base64-standard-encoded nonce||ciphertext||tag. Base64
// keeps the text column happy and makes the DB payload grep-safe.
func (c *TokenCipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", errors.New("slackoauth: refusing to encrypt empty plaintext")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("slackoauth: rand nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt inverts Encrypt. Returns ErrInvalidCiphertext for anything
// that decodes or opens cleanly — same sentinel regardless of the
// underlying cause so the caller can present a single user-visible
// message ("please reconnect Slack").
func (c *TokenCipher) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize+c.aead.Overhead() {
		return "", ErrInvalidCiphertext
	}
	nonce, sealed := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	return string(plaintext), nil
}
