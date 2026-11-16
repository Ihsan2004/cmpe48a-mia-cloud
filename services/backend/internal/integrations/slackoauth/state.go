package slackoauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/google/uuid"
)

// State payload layout (56 bytes):
//
//	bytes  0..15 — user UUID (raw, the initiating Syncrun user)
//	bytes 16..23 — issued-at Unix seconds (uint64 BE)
//	bytes 24..55 — HMAC-SHA256(uuid || issuedAt, secret)
//
// Encoded base64url (75 chars, no padding). Slack echoes the state back
// in the callback query string — validating the signature there is what
// stops an attacker from sending an install URL to someone else and
// silently adding that user's Slack workspace to our app.
//
// 15-minute expiry: plenty of headroom for the user to click through
// Slack consent, but short enough that a stolen install URL from a
// browser-history leak can't be replayed the next day.

const (
	stateUUIDLen   = 16
	stateTsLen     = 8
	stateMACLen    = 32
	stateTotalLen  = stateUUIDLen + stateTsLen + stateMACLen
	stateMaxAgeSec = 15 * 60
)

// Sentinel errors so the handler can return distinct HTTP codes for
// "stale state" (user took too long) vs "tampered state" (likely
// attack, lightly paranoid logging warranted).
var (
	ErrStateInvalid = errors.New("slackoauth: OAuth state is malformed or signature invalid")
	ErrStateExpired = errors.New("slackoauth: OAuth state expired (>15 minutes)")
)

// StateSigner issues + verifies the OAuth install state parameter.
// Uses the same HMAC secret as the unsubscribe signer would (but pass a
// distinct secret in main.go — reusing the same key across purposes is
// a minor security smell even though the payload format prevents
// cross-protocol forgery).
type StateSigner struct {
	secret []byte
	now    func() time.Time
}

// NewStateSigner demands a >= 32-byte secret — matches the unsubscribe
// signer's floor so ops doesn't have to juggle two secret-length rules.
func NewStateSigner(secret string) (*StateSigner, error) {
	if len(secret) < 32 {
		return nil, errors.New("slackoauth: state signing secret must be >= 32 bytes")
	}
	return &StateSigner{
		secret: []byte(secret),
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// Generate returns a signed state bound to userID at the current time.
func (s *StateSigner) Generate(userID uuid.UUID) string {
	if userID == uuid.Nil {
		return ""
	}
	payload := make([]byte, stateUUIDLen+stateTsLen)
	copy(payload[:stateUUIDLen], userID[:])
	binary.BigEndian.PutUint64(payload[stateUUIDLen:], uint64(s.now().Unix()))

	mac := hmacSum(s.secret, payload)

	full := make([]byte, 0, stateTotalLen)
	full = append(full, payload...)
	full = append(full, mac...)
	return base64.RawURLEncoding.EncodeToString(full)
}

// Validate verifies signature + expiry and returns the embedded user.
func (s *StateSigner) Validate(token string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != stateTotalLen {
		return uuid.Nil, ErrStateInvalid
	}

	payload := raw[:stateUUIDLen+stateTsLen]
	gotMAC := raw[stateUUIDLen+stateTsLen:]
	wantMAC := hmacSum(s.secret, payload)
	if !hmac.Equal(gotMAC, wantMAC) {
		return uuid.Nil, ErrStateInvalid
	}

	var userID uuid.UUID
	copy(userID[:], payload[:stateUUIDLen])
	if userID == uuid.Nil {
		return uuid.Nil, ErrStateInvalid
	}

	issuedAt := time.Unix(int64(binary.BigEndian.Uint64(payload[stateUUIDLen:])), 0).UTC()
	if s.now().Sub(issuedAt) > time.Duration(stateMaxAgeSec)*time.Second {
		return uuid.Nil, ErrStateExpired
	}
	if issuedAt.After(s.now().Add(1 * time.Minute)) {
		return uuid.Nil, ErrStateInvalid
	}
	return userID, nil
}

func hmacSum(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
