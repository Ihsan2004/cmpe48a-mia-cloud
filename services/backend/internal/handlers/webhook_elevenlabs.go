package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

// ElevenLabsWebhookHandler is /api/webhooks/elevenlabs. ElevenLabs posts the
// transcript here after batch STT completes. The handler verifies the HMAC
// signature, enforces a freshness window + replay cache, and delegates to
// TranscriptionService.
type ElevenLabsWebhookHandler struct {
	svc *services.TranscriptionService

	// seenDeliveries guards against replay within the freshness window.
	// Keys are the per-delivery id from the provider header (preferred) or
	// the raw signature hex (fallback). Values are the timestamp of first
	// acceptance. Entries older than replayWindow are reaped on each call.
	mu             sync.Mutex
	seenDeliveries map[string]time.Time
}

// replayWindow is how far back a valid signature is accepted. Requests
// outside this window are rejected as stale. Matches the roadmap's
// "freshness window" success criterion.
const replayWindow = 5 * time.Minute

// NewElevenLabsWebhookHandler returns the handler. Returns nil when the
// service is not wired (API key missing) — routes.go uses this to skip
// registering the endpoint so we don't silently accept unauthenticated
// traffic in misconfigured environments.
func NewElevenLabsWebhookHandler(svc *services.TranscriptionService) *ElevenLabsWebhookHandler {
	if svc == nil {
		return nil
	}
	return &ElevenLabsWebhookHandler{
		svc:            svc,
		seenDeliveries: make(map[string]time.Time),
	}
}

// HandleWebhook processes a POST /api/webhooks/elevenlabs request.
//
// Signature header — accepts any of the following, first non-empty wins
// (Phase 3 fix 2026-04-20; the roadmap originally specified X-Signature,
// ElevenLabs itself uses ElevenLabs-Signature; we accept both plus the
// lowercase variants some SDKs generate):
//   ElevenLabs-Signature
//   X-Signature
//   (and lowercase equivalents)
//
// Freshness + replay protection (Phase 3 fix 2026-04-20):
//   - ElevenLabs-Webhook-Timestamp (unix seconds) must be within
//     ±replayWindow of server now. Missing timestamps fall back to the
//     signature hex as the replay key (degraded but still detects repeats).
//   - ElevenLabs-Webhook-Id or the signature hex is cached as a seen-id
//     for replayWindow. Duplicate delivery → 200 OK, no re-ingest.
//
// Other behaviour:
//   - secret not configured     → 503
//   - missing/invalid signature → 401
//   - stale timestamp           → 401
//   - malformed JSON            → 400
//   - service error             → 500 (ElevenLabs retries on 5xx)
//   - success (fresh or replay) → 200
func (h *ElevenLabsWebhookHandler) HandleWebhook(c *fiber.Ctx) error {
	secret := os.Getenv("ELEVENLABS_WEBHOOK_SECRET")
	if secret == "" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "ElevenLabs webhook secret not configured",
			"code":  "WEBHOOK_UNCONFIGURED",
		})
	}

	body := c.Body()

	// ElevenLabs uses its own Stripe-style signature scheme, not Svix.
	// Header `ElevenLabs-Signature` carries `t=<unix>,v0=<hex>[,v0=<hex>]`
	// (multiple v0 entries during secret rotation). The HMAC key is the
	// dashboard secret as-is (the `wsec_` prefix is retained verbatim,
	// no base64 decode), the signed payload is `<timestamp>.<body>`, and
	// the digest is hex-encoded SHA-256.
	sigHeader := firstNonEmptyHeader(c, "ElevenLabs-Signature", "elevenlabs-signature")
	t, sigs, parseErr := parseElevenLabsSignatureHeader(sigHeader)
	if parseErr != nil || !verifyElevenLabsSignature(secret, t, body, sigs) {
		slog.Warn("elevenlabs webhook: signature rejected",
			"reason", reasonOrEmpty(parseErr),
		)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid signature",
			"code":  "WEBHOOK_AUTH_INVALID",
		})
	}

	// Use the parsed `t=` value as the freshness anchor — it's already the
	// signed timestamp, no need to read a separate header.
	tsRaw := t

	// Freshness: the timestamp header (if present) must be within the
	// replay window. Without this a captured signature is valid forever.
	now := time.Now()
	if tsRaw != "" {
		ts, err := strconv.ParseInt(tsRaw, 10, 64)
		if err != nil {
			slog.Warn("elevenlabs webhook: unparseable timestamp", "raw", tsRaw)
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid timestamp",
				"code":  "WEBHOOK_TIMESTAMP_INVALID",
			})
		}
		signed := time.Unix(ts, 0)
		if now.Sub(signed).Abs() > replayWindow {
			slog.Warn("elevenlabs webhook: stale timestamp",
				"signed_at", signed, "skew_seconds", int(now.Sub(signed).Seconds()))
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "timestamp outside freshness window",
				"code":  "WEBHOOK_TIMESTAMP_STALE",
			})
		}
	}

	// Replay cache: key on delivery id when available, signature otherwise.
	// Duplicate inside the window → respond 200 but do not re-ingest.
	deliveryID := firstNonEmptyHeader(c,
		"ElevenLabs-Webhook-Id",
		"elevenlabs-webhook-id",
		"X-Webhook-Id",
	)
	replayKey := deliveryID
	if replayKey == "" {
		// No delivery-id header from the provider — fall back to the raw
		// signature header, which is unique per delivery (timestamp + sig).
		replayKey = sigHeader
	}
	if h.markSeenOrReject(replayKey, now) {
		slog.Info("elevenlabs webhook: duplicate delivery ignored",
			"delivery_id", deliveryID)
		return c.SendStatus(fiber.StatusOK)
	}

	// ElevenLabs Scribe v2 deliveries always wrap the transcript payload
	// in an envelope: `{type, event_timestamp, data:{...}}`. Decode into
	// the envelope shape and use the inner data object. The flat shape
	// (no envelope) was a hypothesised earlier-API variant that never
	// shipped — the live payload always has type/data, so the unwrap is
	// the only path.
	var envelope struct {
		Type string                  `json:"type"`
		Data services.WebhookPayload `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid json body",
		})
	}
	payload := envelope.Data

	if err := h.svc.IngestWebhookResult(c.Context(), payload); err != nil {
		slog.Error("elevenlabs webhook: ingest failed",
			"request_id", payload.RequestID,
			"error", err)
		// Return 500 so ElevenLabs retries. Duplicate ingest is safe:
		// IngestWebhookResult clears segments before inserting and
		// UpdateMeetingFields is idempotent.
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "ingest failed",
		})
	}
	return c.SendStatus(fiber.StatusOK)
}

// markSeenOrReject returns true if `key` has already been accepted within
// the replay window (caller should treat as duplicate). Reaps expired
// entries as a side-effect. Empty key is never considered seen — caller
// should not route unsigned requests here anyway.
func (h *ElevenLabsWebhookHandler) markSeenOrReject(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	// Reap stale entries so the map doesn't grow without bound.
	for k, t := range h.seenDeliveries {
		if now.Sub(t) > replayWindow {
			delete(h.seenDeliveries, k)
		}
	}

	if _, ok := h.seenDeliveries[key]; ok {
		return true
	}
	h.seenDeliveries[key] = now
	return false
}

// firstNonEmptyHeader walks the candidate names and returns the first
// non-empty value. Useful because Fiber's c.Get is already case-insensitive
// but provider header names vary (ElevenLabs-*, X-*, lowercase variants).
func firstNonEmptyHeader(c *fiber.Ctx, names ...string) string {
	for _, n := range names {
		if v := c.Get(n); v != "" {
			return v
		}
	}
	return ""
}

// parseElevenLabsSignatureHeader splits the `ElevenLabs-Signature` header
// value of the form `t=<unix_seconds>,v0=<hex>[,v0=<hex>]` into its
// timestamp and (one or more, during secret rotation) signatures.
// Returns an error string suitable for log diagnostics when a required
// component is missing or malformed.
func parseElevenLabsSignatureHeader(header string) (timestamp string, sigs []string, err error) {
	if header == "" {
		return "", nil, errors.New("missing")
	}
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v0":
			sigs = append(sigs, kv[1])
		}
	}
	if timestamp == "" {
		return "", nil, errors.New("no t=")
	}
	if len(sigs) == 0 {
		return "", nil, errors.New("no v0=")
	}
	return timestamp, sigs, nil
}

// verifyElevenLabsSignature checks any of the supplied v0 signatures
// against `HMAC-SHA256(secret, "<timestamp>.<raw_body>")` rendered as
// lowercase hex. The secret is used verbatim — `wsec_` prefix included
// — per ElevenLabs' documented scheme. Constant-time comparison.
func verifyElevenLabsSignature(secret, timestamp string, body []byte, sigs []string) bool {
	if secret == "" || timestamp == "" || len(sigs) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(expected)) {
			return true
		}
	}
	return false
}

// reasonOrEmpty returns the error's message or an empty string. Used in
// the rejection-path diagnostic log so the field is always present.
func reasonOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// verifyHMACSignature uses constant-time compare. Both raw hex and
// "sha256=<hex>" prefixes are accepted to be tolerant of delivery format.
func verifyHMACSignature(secret string, body []byte, signature string) bool {
	if signature == "" {
		return false
	}
	// Strip any "sha256=" prefix some webhook frameworks prepend.
	if len(signature) > 7 && signature[:7] == "sha256=" {
		signature = signature[7:]
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	// hex.DecodeString is constant-time-safe for length check, and
	// hmac.Equal handles the byte comparison.
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	expBytes, _ := hex.DecodeString(expected)
	return hmac.Equal(sigBytes, expBytes)
}

// bytesReader is a tiny helper to avoid importing bytes twice in the file.
func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{b: b}
}

type byteSliceReader struct {
	b   []byte
	pos int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
