package handlers

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"mia-website/backend/internal/services/briefnotify"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ==================== Fake store ====================

type fakeUnsubscribeStore struct {
	unsubscribedIDs map[uuid.UUID]bool
	setErr          error
	clearErr        error
	setCalls        []uuid.UUID
	clearCalls      []uuid.UUID
}

func newFakeStore() *fakeUnsubscribeStore {
	return &fakeUnsubscribeStore{unsubscribedIDs: map[uuid.UUID]bool{}}
}

func (f *fakeUnsubscribeStore) SetEmailUnsubscribed(userID uuid.UUID) (bool, error) {
	f.setCalls = append(f.setCalls, userID)
	if f.setErr != nil {
		return false, f.setErr
	}
	if f.unsubscribedIDs[userID] {
		return false, nil // idempotent re-click, no state change
	}
	f.unsubscribedIDs[userID] = true
	return true, nil
}

func (f *fakeUnsubscribeStore) ClearEmailUnsubscribe(userID uuid.UUID) error {
	f.clearCalls = append(f.clearCalls, userID)
	if f.clearErr != nil {
		return f.clearErr
	}
	delete(f.unsubscribedIDs, userID)
	return nil
}

// ==================== Test helpers ====================

const testUnsubSecret = "handler-test-unsubscribe-secret-32-bytes-long"

func newTestApp(h *UnsubscribeHandler) *fiber.App {
	app := fiber.New()
	app.Get("/api/unsubscribe", h.Unsubscribe)
	app.Post("/api/unsubscribe/restore", func(c *fiber.Ctx) error {
		// Simulate the middleware injecting userID into locals.
		if id := c.Get("X-Test-UserID"); id != "" {
			c.Locals("userID", id)
		}
		return h.Restore(c)
	})
	return app
}

// ==================== GET /api/unsubscribe ====================

func TestUnsubscribe_ValidToken_MarksUserUnsubscribed(t *testing.T) {
	t.Parallel()

	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	store := newFakeStore()
	app := newTestApp(NewUnsubscribeHandler(signer, store))

	userID := uuid.New()
	token := signer.GenerateToken(userID)

	req, _ := http.NewRequest("GET", "/api/unsubscribe?token="+token, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	if len(store.setCalls) != 1 || store.setCalls[0] != userID {
		t.Errorf("expected SetEmailUnsubscribed called once with %s, got %v", userID, store.setCalls)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "iptal edildi") {
		t.Errorf("success copy missing: %s", string(body))
	}
}

func TestUnsubscribe_IdempotentReclick_StaysSuccess(t *testing.T) {
	t.Parallel()

	// Second click on the same link still lands on 200, but the copy
	// acknowledges the state was already set. This matches the user
	// mental model ("I clicked, it says I'm unsubscribed — good").
	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	store := newFakeStore()
	userID := uuid.New()
	store.unsubscribedIDs[userID] = true // pretend first click already landed
	app := newTestApp(NewUnsubscribeHandler(signer, store))

	token := signer.GenerateToken(userID)
	req, _ := http.NewRequest("GET", "/api/unsubscribe?token="+token, nil)
	resp, _ := app.Test(req, -1)

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("idempotent re-click should be 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Zaten iptal") {
		t.Errorf("idempotent copy missing: %s", string(body))
	}
}

func TestUnsubscribe_MissingToken_400(t *testing.T) {
	t.Parallel()

	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	app := newTestApp(NewUnsubscribeHandler(signer, newFakeStore()))

	req, _ := http.NewRequest("GET", "/api/unsubscribe", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnsubscribe_InvalidToken_400(t *testing.T) {
	t.Parallel()

	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	app := newTestApp(NewUnsubscribeHandler(signer, newFakeStore()))

	req, _ := http.NewRequest("GET", "/api/unsubscribe?token=not-a-real-token", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnsubscribe_TokenSignedWithDifferentSecret_400(t *testing.T) {
	t.Parallel()

	attacker, _ := briefnotify.NewUnsubscribeSigner("attacker-secret-not-same-as-production-32")
	honest, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	store := newFakeStore()
	app := newTestApp(NewUnsubscribeHandler(honest, store))

	token := attacker.GenerateToken(uuid.New())
	req, _ := http.NewRequest("GET", "/api/unsubscribe?token="+token, nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if len(store.setCalls) != 0 {
		t.Error("store must not be touched when token signature is wrong")
	}
}

func TestUnsubscribe_NoSigner_503(t *testing.T) {
	t.Parallel()

	// Ops misconfiguration: UNSUBSCRIBE_SIGNING_SECRET not set. We
	// return 503 so the operator notices rather than silently failing.
	app := newTestApp(NewUnsubscribeHandler(nil, newFakeStore()))

	req, _ := http.NewRequest("GET", "/api/unsubscribe?token=whatever", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestUnsubscribe_StoreFailure_500(t *testing.T) {
	t.Parallel()

	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	store := newFakeStore()
	store.setErr = errors.New("db down")
	app := newTestApp(NewUnsubscribeHandler(signer, store))

	token := signer.GenerateToken(uuid.New())
	req, _ := http.NewRequest("GET", "/api/unsubscribe?token="+token, nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

// ==================== POST /api/unsubscribe/restore ====================

func TestRestore_AuthenticatedUser_ClearsUnsubscribe(t *testing.T) {
	t.Parallel()

	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	store := newFakeStore()
	userID := uuid.New()
	store.unsubscribedIDs[userID] = true
	app := newTestApp(NewUnsubscribeHandler(signer, store))

	req, _ := http.NewRequest("POST", "/api/unsubscribe/restore", nil)
	req.Header.Set("X-Test-UserID", userID.String())
	resp, _ := app.Test(req, -1)

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if len(store.clearCalls) != 1 || store.clearCalls[0] != userID {
		t.Errorf("expected Clear called once, got %v", store.clearCalls)
	}
}

func TestRestore_MissingAuth_401(t *testing.T) {
	t.Parallel()

	signer, _ := briefnotify.NewUnsubscribeSigner(testUnsubSecret)
	app := newTestApp(NewUnsubscribeHandler(signer, newFakeStore()))

	req, _ := http.NewRequest("POST", "/api/unsubscribe/restore", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ==================== HTML escape guard ====================

func TestHTMLEscape_ReplacesXMLChars(t *testing.T) {
	t.Parallel()
	got := htmlEscape(`<script>alert("xss")</script>`)
	if strings.Contains(got, "<script>") || strings.Contains(got, "\"xss\"") {
		t.Errorf("htmlEscape failed to escape: %q", got)
	}
}
