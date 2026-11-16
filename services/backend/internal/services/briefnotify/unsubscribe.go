package briefnotify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Unsubscribe token layout (56 bytes, base64url-encoded into 75 chars):
//
//	bytes  0..15  — user UUID (raw 16 bytes)
//	bytes 16..23  — issued-at Unix seconds, big-endian uint64
//	bytes 24..55  — HMAC-SHA256(uuid || issuedAt, secret)
//
// Roadmap Karar #16: signed so a third party can't unsubscribe another
// user by guessing their id. 30-day expiry means a brief mail from last
// month still lets the user opt out, but a leaked old token from an
// archive doesn't work forever.
const (
	unsubscribeTokenUUIDLen    = 16
	unsubscribeTokenTsLen      = 8
	unsubscribeTokenMACLen     = 32
	unsubscribeTokenTotalLen   = unsubscribeTokenUUIDLen + unsubscribeTokenTsLen + unsubscribeTokenMACLen
	unsubscribeTokenMaxAgeDays = 30
)

// Sentinel errors returned by ValidateToken — kept distinct so the
// handler can pick the user-facing message (invalid vs expired).
var (
	ErrUnsubscribeTokenInvalid = errors.New("unsubscribe token: invalid format or signature")
	ErrUnsubscribeTokenExpired = errors.New("unsubscribe token: expired")
)

// UnsubscribeSigner generates + verifies opt-out tokens. The secret
// MUST be >= 32 bytes and loaded from UNSUBSCRIBE_SIGNING_SECRET — a
// leaked secret lets an attacker forge unsubscribes for any user, so
// it's treated as a production secret even though the endpoint only
// toggles a boolean.
type UnsubscribeSigner struct {
	secret []byte
	now    func() time.Time // overridable for tests
}

// NewUnsubscribeSigner validates the secret and returns a signer.
// Refuses secrets shorter than 32 bytes — below that HMAC-SHA256's
// security margin drops off meaningfully. The caller should already
// have generated the secret with `openssl rand -hex 32`.
func NewUnsubscribeSigner(secret string) (*UnsubscribeSigner, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("briefnotify: UNSUBSCRIBE_SIGNING_SECRET must be >= 32 bytes (got %d)", len(secret))
	}
	return &UnsubscribeSigner{
		secret: []byte(secret),
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

// GenerateToken produces a signed token for userID with the current
// time as the issued-at stamp. Output is base64url without padding
// (URL-safe, no percent-encoding needed).
func (s *UnsubscribeSigner) GenerateToken(userID uuid.UUID) string {
	if userID == uuid.Nil {
		return ""
	}
	payload := make([]byte, unsubscribeTokenUUIDLen+unsubscribeTokenTsLen)
	copy(payload[:unsubscribeTokenUUIDLen], userID[:])
	binary.BigEndian.PutUint64(payload[unsubscribeTokenUUIDLen:], uint64(s.now().Unix()))

	mac := hmacSum(s.secret, payload)

	full := make([]byte, 0, unsubscribeTokenTotalLen)
	full = append(full, payload...)
	full = append(full, mac...)
	return base64.RawURLEncoding.EncodeToString(full)
}

// ValidateToken returns the embedded userID when the signature is
// valid and the token is within maxAge. The MAC check is constant-time
// so token-guessing attacks can't time-probe the secret.
func (s *UnsubscribeSigner) ValidateToken(token string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return uuid.Nil, ErrUnsubscribeTokenInvalid
	}
	if len(raw) != unsubscribeTokenTotalLen {
		return uuid.Nil, ErrUnsubscribeTokenInvalid
	}

	payload := raw[:unsubscribeTokenUUIDLen+unsubscribeTokenTsLen]
	gotMAC := raw[unsubscribeTokenUUIDLen+unsubscribeTokenTsLen:]
	wantMAC := hmacSum(s.secret, payload)

	if !hmac.Equal(gotMAC, wantMAC) {
		return uuid.Nil, ErrUnsubscribeTokenInvalid
	}

	var userID uuid.UUID
	copy(userID[:], payload[:unsubscribeTokenUUIDLen])
	if userID == uuid.Nil {
		return uuid.Nil, ErrUnsubscribeTokenInvalid
	}

	issuedAt := time.Unix(int64(binary.BigEndian.Uint64(payload[unsubscribeTokenUUIDLen:])), 0).UTC()
	if s.now().Sub(issuedAt) > unsubscribeTokenMaxAgeDays*24*time.Hour {
		return uuid.Nil, ErrUnsubscribeTokenExpired
	}
	// Guard against tokens "from the future" (clock skew or tampering).
	if issuedAt.After(s.now().Add(24 * time.Hour)) {
		return uuid.Nil, ErrUnsubscribeTokenInvalid
	}

	return userID, nil
}

func hmacSum(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
