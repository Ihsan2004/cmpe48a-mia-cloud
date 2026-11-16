package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func newWebhookHandlerForCacheTests() *ElevenLabsWebhookHandler {
	// Cache tests only exercise markSeenOrReject; we don't need a real
	// TranscriptionService wired up. Bypass the constructor to stay
	// service-free.
	return &ElevenLabsWebhookHandler{
		seenDeliveries: map[string]time.Time{},
	}
}

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyHMACSignature_AcceptsValidHex(t *testing.T) {
	secret := "super-secret"
	body := []byte(`{"request_id":"req_1","status":"ok"}`)
	sig := signBody(secret, body)
	if !verifyHMACSignature(secret, body, sig) {
		t.Error("expected valid signature to verify")
	}
}

func TestVerifyHMACSignature_AcceptsSha256Prefix(t *testing.T) {
	secret := "abc"
	body := []byte("payload")
	sig := "sha256=" + signBody(secret, body)
	if !verifyHMACSignature(secret, body, sig) {
		t.Error("sha256= prefixed signature should verify")
	}
}

func TestVerifyHMACSignature_RejectsMismatch(t *testing.T) {
	secret := "abc"
	body := []byte("payload")
	// Correct signature for a DIFFERENT body
	wrongSig := signBody(secret, []byte("tampered"))
	if verifyHMACSignature(secret, body, wrongSig) {
		t.Error("tampered signature should be rejected")
	}
}

func TestVerifyHMACSignature_RejectsWrongSecret(t *testing.T) {
	body := []byte("payload")
	sigFromAttacker := signBody("wrong-secret", body)
	if verifyHMACSignature("real-secret", body, sigFromAttacker) {
		t.Error("signature from wrong secret should be rejected")
	}
}

func TestVerifyHMACSignature_RejectsEmpty(t *testing.T) {
	if verifyHMACSignature("s", []byte("body"), "") {
		t.Error("empty signature should be rejected")
	}
}

func TestVerifyHMACSignature_RejectsNonHex(t *testing.T) {
	if verifyHMACSignature("s", []byte("body"), "not-hex-!!") {
		t.Error("non-hex signature should be rejected")
	}
}

func TestVerifyHMACSignature_RejectsWrongLength(t *testing.T) {
	// A shorter hex string shouldn't be treated as a match.
	if verifyHMACSignature("s", []byte("body"), "abcd") {
		t.Error("wrong-length hex should be rejected")
	}
}

// ---------------- replay cache ----------------

// TestMarkSeenOrReject_DuplicateRejected — same delivery id twice within
// the replay window → second call reports "seen" so handler can 200/skip.
func TestMarkSeenOrReject_DuplicateRejected(t *testing.T) {
	h := newWebhookHandlerForCacheTests()
	now := time.Now()
	if h.markSeenOrReject("delivery-1", now) {
		t.Error("first call should NOT be seen")
	}
	if !h.markSeenOrReject("delivery-1", now) {
		t.Error("second call with same key SHOULD be seen")
	}
}

// TestMarkSeenOrReject_EmptyKeyNeverSeen — empty key should not poison
// the cache or flag subsequent requests.
func TestMarkSeenOrReject_EmptyKeyNeverSeen(t *testing.T) {
	h := newWebhookHandlerForCacheTests()
	now := time.Now()
	if h.markSeenOrReject("", now) {
		t.Error("empty key should never be treated as seen")
	}
	if h.markSeenOrReject("", now) {
		t.Error("repeated empty key should still not be seen")
	}
}

// TestMarkSeenOrReject_ExpiresAfterWindow — an id older than replayWindow
// is reaped so its next appearance is treated as fresh.
func TestMarkSeenOrReject_ExpiresAfterWindow(t *testing.T) {
	h := newWebhookHandlerForCacheTests()
	base := time.Now()
	h.seenDeliveries["old"] = base
	future := base.Add(replayWindow + time.Minute)
	if h.markSeenOrReject("old", future) {
		t.Error("stale entry should be reaped and treated fresh")
	}
	// Newly-accepted — a second call inside the window is the duplicate.
	if !h.markSeenOrReject("old", future.Add(10*time.Second)) {
		t.Error("after re-accept, duplicate in window should be seen")
	}
}

func TestBytesReader_Streams(t *testing.T) {
	src := []byte("hello world")
	r := bytesReader(src)
	buf := make([]byte, 5)
	n, err := r.Read(buf)
	if err != nil || n != 5 || string(buf) != "hello" {
		t.Errorf("first read: n=%d err=%v buf=%q", n, err, string(buf))
	}
	n, err = r.Read(buf)
	if err != nil || n != 5 || string(buf[:n]) != " worl" {
		t.Errorf("second read: n=%d err=%v buf=%q", n, err, string(buf[:n]))
	}
	n, err = r.Read(buf)
	if err != nil || n != 1 {
		t.Errorf("third read: n=%d err=%v", n, err)
	}
	if _, err := r.Read(buf); err == nil {
		t.Error("expected EOF on exhausted reader")
	}
}
