package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Phase 5 fix 2026-04-20 regression: UpdateProfile must reject unknown
// recording_method values with a 400. The roadmap locks the enum to
// {bot, desktop}; accepting arbitrary strings would let a bad client
// (or a typo) silently poison the profile.

func TestUpdateProfile_RecordingMethodEnumValidation(t *testing.T) {
	// We only need to exercise the validation branch — the handler
	// returns 400 before calling the service, so no DB is involved.
	// Just register the handler with a fake userID middleware and hit
	// it with Fiber's httptest integration.
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("userID", "00000000-0000-0000-0000-000000000001")
		return c.Next()
	})
	app.Put("/profile", func(c *fiber.Ctx) error {
		// Construct the handler with a nil service — validation runs
		// before any service call, so a nil pointer is safe here.
		h := &ProfileHandler{profileService: nil}
		return h.UpdateProfile(c)
	})

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantCode   string // expected error code in JSON (empty = don't check)
	}{
		{
			name: "unknown value rejected",
			body: map[string]any{
				"full_name":        "Ali",
				"recording_method": "smoke-signal",
			},
			wantStatus: fiber.StatusBadRequest,
			wantCode:   "INVALID_RECORDING_METHOD",
		},
		{
			name: "case-sensitive: 'Bot' rejected",
			body: map[string]any{
				"full_name":        "Ali",
				"recording_method": "Bot",
			},
			wantStatus: fiber.StatusBadRequest,
			wantCode:   "INVALID_RECORDING_METHOD",
		},
		{
			name: "empty full_name blocks everything",
			body: map[string]any{
				"full_name":        "",
				"recording_method": "bot",
			},
			wantStatus: fiber.StatusBadRequest,
			// full_name check fires first; no specific code
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("PUT", "/profile", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantCode != "" {
				raw, _ := io.ReadAll(resp.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				if body["code"] != tc.wantCode {
					t.Errorf("code = %v, want %q (body=%s)", body["code"], tc.wantCode, raw)
				}
			}
		})
	}
}
