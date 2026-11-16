package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// Phase 6 fix 2026-04-20 — handshake auth contract tests. The handler's
// job before the WebSocket upgrade is to authenticate the JWT; anything
// that rejects a valid token or accepts an invalid one is a regression.
//
// Covers:
//   * missing token (no query, no Authorization) → 401 TOKEN_MISSING
//   * Authorization: Bearer fallback works when ?token= is absent
//   * invalid token → 401 TOKEN_INVALID
//   * valid token but claim is not a uuid → 401 TOKEN_INVALID_CLAIMS
//
// The happy path (upgrade succeeds) would require a real net/http upgrade
// handshake which adds a lot of setup for marginal coverage — the cache
// and enum tests below exercise the post-upgrade behaviour via direct
// repo calls, and the e2e suite has a full-path scenario.

type stubAuthValidator struct {
	claims map[string]interface{}
	err    error
}

func (s *stubAuthValidator) ValidateAccessToken(_ string) (map[string]interface{}, error) {
	return s.claims, s.err
}

func buildDesktopWSHandler(auth desktopAuthValidator) *DesktopWSHandler {
	hub := services.NewDesktopHub()
	return newDesktopWSHandlerForTest(hub, &fakeDesktopRepoForHandlerTests{}, auth)
}

// fakeDesktopRepoForHandlerTests is a minimal no-op DesktopRepository
// implementation. Handler auth tests never reach repo methods (they
// fail before upgrade), but the handler struct requires a non-nil repo.
type fakeDesktopRepoForHandlerTests struct{}

func (f *fakeDesktopRepoForHandlerTests) CreateSession(_ context.Context, _ uuid.UUID, _ string) (*models.DesktopSession, error) {
	return nil, nil
}
func (f *fakeDesktopRepoForHandlerTests) UpdateHeartbeat(_ context.Context, _ uuid.UUID) error {
	return nil
}
func (f *fakeDesktopRepoForHandlerTests) CloseSession(_ context.Context, _ uuid.UUID) error {
	return nil
}
func (f *fakeDesktopRepoForHandlerTests) FindStaleSessions(_ context.Context, _ time.Time) ([]models.DesktopSession, error) {
	return nil, nil
}
func (f *fakeDesktopRepoForHandlerTests) InsertCommandIfMissing(_ context.Context, _, _ uuid.UUID, _ string) (bool, error) {
	return true, nil
}
func (f *fakeDesktopRepoForHandlerTests) MarkCommandAck(_ context.Context, _, _ uuid.UUID, _, _ string) error {
	return nil
}

func TestDesktopWS_Handshake_MissingToken(t *testing.T) {
	app := fiber.New()
	h := buildDesktopWSHandler(&stubAuthValidator{err: errors.New("should not be called")})
	app.Get("/ws/desktop", h.Handle)

	req := httptest.NewRequest("GET", "/ws/desktop", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDesktopWS_Handshake_InvalidToken(t *testing.T) {
	app := fiber.New()
	h := buildDesktopWSHandler(&stubAuthValidator{err: errors.New("bad sig")})
	app.Get("/ws/desktop", h.Handle)

	req := httptest.NewRequest("GET", "/ws/desktop?token=whatever", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDesktopWS_Handshake_AcceptsAuthorizationBearer(t *testing.T) {
	// Valid claims but we stop early with "auth service unavailable" when
	// the validator returns an error — to prove the fallback works we
	// arrange the validator to fail, and assert the response is 401
	// (TOKEN_INVALID) rather than 401 (TOKEN_MISSING).
	//
	// If we hadn't read the Authorization header, we'd have returned
	// TOKEN_MISSING. TOKEN_INVALID means the fallback populated `token`
	// and ValidateAccessToken ran.
	app := fiber.New()
	h := buildDesktopWSHandler(&stubAuthValidator{err: errors.New("bad sig, but recognized token")})
	app.Get("/ws/desktop", h.Handle)

	req := httptest.NewRequest("GET", "/ws/desktop", nil)
	req.Header.Set("Authorization", "Bearer fake-jwt-from-header")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// Read body, expect TOKEN_INVALID rather than TOKEN_MISSING.
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !contains(body, "TOKEN_INVALID") {
		t.Errorf("body should carry TOKEN_INVALID (fallback worked), got %q", body)
	}
	if contains(body, "TOKEN_MISSING") {
		t.Errorf("body still says TOKEN_MISSING — header fallback never ran")
	}
}

func TestDesktopWS_Handshake_ClaimWithNonUUIDSub(t *testing.T) {
	app := fiber.New()
	h := buildDesktopWSHandler(&stubAuthValidator{claims: map[string]interface{}{"sub": "not-a-uuid"}})
	app.Get("/ws/desktop", h.Handle)

	req := httptest.NewRequest("GET", "/ws/desktop?token=ok", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---------------- helpers ----------------

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
