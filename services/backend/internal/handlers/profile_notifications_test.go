package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// These tests exercise the PATCH body parser + auth guard for the
// /profile/notifications endpoint. They do NOT hit the DB — we stop at
// the h.profileService call by registering the handler on an app where
// the service layer is pre-wired but the DB isn't connected. That only
// works for paths that reject before reaching the service (missing
// auth, empty body, invalid json). Full DB-backed behaviour is covered
// by integration tests.

// ==================== Auth guard ====================

func TestUpdateNotificationPreferences_Unauthenticated_401(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	h := &ProfileHandler{} // no service needed — we reject on missing userID
	app.Patch("/api/profile/notifications", h.UpdateNotificationPreferences)

	req, _ := http.NewRequest(http.MethodPatch, "/api/profile/notifications",
		strings.NewReader(`{"brief_deliver_via_email":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ==================== Body parsing ====================

func TestUpdateNotificationPreferences_EmptyBody_400(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	h := &ProfileHandler{}
	uid := uuid.New().String()

	// Inject userID for the handler so we fall through the auth guard.
	app.Patch("/api/profile/notifications", func(c *fiber.Ctx) error {
		c.Locals("userID", uid)
		return h.UpdateNotificationPreferences(c)
	})

	// Body has neither email nor slack field → 400.
	req, _ := http.NewRequest(http.MethodPatch, "/api/profile/notifications",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if errMsg, _ := parsed["error"].(string); !strings.Contains(errMsg, "tercih") {
		t.Errorf("expected Turkish error copy about preference, got %q", errMsg)
	}
}

func TestUpdateNotificationPreferences_InvalidJSON_400(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	h := &ProfileHandler{}
	uid := uuid.New().String()

	app.Patch("/api/profile/notifications", func(c *fiber.Ctx) error {
		c.Locals("userID", uid)
		return h.UpdateNotificationPreferences(c)
	})

	req, _ := http.NewRequest(http.MethodPatch, "/api/profile/notifications",
		strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUpdateNotificationPreferences_InvalidUUID_400(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	h := &ProfileHandler{}
	app.Patch("/api/profile/notifications", func(c *fiber.Ctx) error {
		c.Locals("userID", "not-a-uuid")
		return h.UpdateNotificationPreferences(c)
	})

	req, _ := http.NewRequest(http.MethodPatch, "/api/profile/notifications",
		strings.NewReader(`{"brief_deliver_via_email":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
