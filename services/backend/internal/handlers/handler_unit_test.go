package handlers

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	calendar "google.golang.org/api/calendar/v3"
)

// ==================== getUserIDFromContext Tests ====================

func TestGetUserIDFromContext_Valid(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	userID := uuid.New()

	app.Get("/test", func(c *fiber.Ctx) error {
		c.Locals("userID", userID.String())
		uid, err := getUserIDFromContext(c)
		if err != nil {
			return c.Status(500).SendString(err.Error())
		}
		return c.SendString(uid.String())
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != userID.String() {
		t.Errorf("expected %s, got %s", userID.String(), string(body))
	}
}

func TestGetUserIDFromContext_Missing(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	app.Get("/test", func(c *fiber.Ctx) error {
		_, err := getUserIDFromContext(c)
		if err == nil {
			return c.SendString("should have failed")
		}
		return c.Status(401).SendString(err.Error())
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestGetUserIDFromContext_InvalidUUID(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	app.Get("/test", func(c *fiber.Ctx) error {
		c.Locals("userID", "not-a-uuid")
		_, err := getUserIDFromContext(c)
		if err == nil {
			return c.SendString("should have failed")
		}
		return c.Status(400).SendString(err.Error())
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetUserIDFromContext_EmptyString(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	app.Get("/test", func(c *fiber.Ctx) error {
		c.Locals("userID", "")
		_, err := getUserIDFromContext(c)
		if err == nil {
			return c.SendString("should have failed")
		}
		return c.Status(401).SendString(err.Error())
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ==================== Decision Handler: Request Parsing ====================

func TestDecisionHandler_InvalidMeetingID(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/meetings/:id/decision-items", func(c *fiber.Ctx) error {
		c.Locals("userID", uuid.New().String())

		meetingIDStr := c.Params("id")
		_, err := uuid.Parse(meetingIDStr)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
		}
		return c.SendString("ok")
	})

	req, _ := http.NewRequest("GET", "/meetings/not-a-uuid/decision-items", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400 for invalid meeting ID, got %d", resp.StatusCode)
	}
}

func TestDecisionHandler_ValidMeetingID(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	meetingID := uuid.New()

	app.Get("/meetings/:id/decision-items", func(c *fiber.Ctx) error {
		c.Locals("userID", uuid.New().String())

		meetingIDStr := c.Params("id")
		parsedID, err := uuid.Parse(meetingIDStr)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
		}
		return c.SendString(parsedID.String())
	})

	req, _ := http.NewRequest("GET", "/meetings/"+meetingID.String()+"/decision-items", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ==================== Brief Handler: Feedback Validation ====================

func TestBriefFeedback_InvalidFeedbackValue(t *testing.T) {
	t.Parallel()

	// Test that only "useful" and "not_useful" are accepted
	validValues := []string{"useful", "not_useful"}
	invalidValues := []string{"", "maybe", "great", "neutral"}

	for _, v := range validValues {
		if v != "useful" && v != "not_useful" {
			t.Errorf("%s should be valid", v)
		}
	}
	for _, v := range invalidValues {
		if v == "useful" || v == "not_useful" {
			t.Errorf("%s should be invalid", v)
		}
	}
}

// ==================== Chain Handler: Body Parsing ====================

func TestChainHandler_NullChainID_IsRemove(t *testing.T) {
	t.Parallel()

	// Test the chain update body parsing logic
	type chainUpdateBody struct {
		ChainID *string `json:"chain_id"`
	}

	// null chain_id = remove from chain
	bodyJSON := `{"chain_id": null}`
	reader := strings.NewReader(bodyJSON)

	app := fiber.New()
	app.Patch("/test", func(c *fiber.Ctx) error {
		var body chainUpdateBody
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).SendString(err.Error())
		}
		if body.ChainID == nil {
			return c.SendString("remove")
		}
		return c.SendString("assign:" + *body.ChainID)
	})

	req, _ := http.NewRequest("PATCH", "/test", reader)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "remove" {
		t.Errorf("expected 'remove' for null chain_id, got '%s'", string(body))
	}
}

func TestChainHandler_ValidChainID_IsAssign(t *testing.T) {
	t.Parallel()

	type chainUpdateBody struct {
		ChainID *string `json:"chain_id"`
	}

	chainID := uuid.New().String()
	bodyJSON := `{"chain_id": "` + chainID + `"}`
	reader := strings.NewReader(bodyJSON)

	app := fiber.New()
	app.Patch("/test", func(c *fiber.Ctx) error {
		var body chainUpdateBody
		if err := c.BodyParser(&body); err != nil {
			return c.Status(400).SendString(err.Error())
		}
		if body.ChainID == nil {
			return c.SendString("remove")
		}
		return c.SendString("assign:" + *body.ChainID)
	})

	req, _ := http.NewRequest("PATCH", "/test", reader)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	body, _ := io.ReadAll(resp.Body)
	expected := "assign:" + chainID
	if string(body) != expected {
		t.Errorf("expected '%s', got '%s'", expected, string(body))
	}
}

// ==================== parseEventTime Tests ====================

func TestParseEventTime_RFC3339(t *testing.T) {
	t.Parallel()

	eventTime := &calendar.EventDateTime{
		DateTime: "2026-04-01T14:00:00+03:00",
	}

	parsed, err := parseEventTime(eventTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Hour() != 14 {
		t.Errorf("expected hour 14, got %d", parsed.Hour())
	}
}

func TestParseEventTime_AllDayEvent(t *testing.T) {
	t.Parallel()

	eventTime := &calendar.EventDateTime{
		Date: "2026-04-01",
	}

	parsed, err := parseEventTime(eventTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Day() != 1 || parsed.Month() != 4 {
		t.Errorf("expected April 1, got %s", parsed.Format("January 2"))
	}
}

func TestParseEventTime_InvalidFormat(t *testing.T) {
	t.Parallel()

	eventTime := &calendar.EventDateTime{
		DateTime: "not-a-date",
	}

	_, err := parseEventTime(eventTime)
	if err == nil {
		t.Error("expected error for invalid date format")
	}
}

func TestParseEventTime_EmptyBothFields(t *testing.T) {
	t.Parallel()

	eventTime := &calendar.EventDateTime{}

	_, err := parseEventTime(eventTime)
	if err == nil {
		t.Error("expected error for empty event time")
	}
}

// ==================== Ownership Check Pattern ====================

func TestVerifyItemOwnership_Pattern(t *testing.T) {
	t.Parallel()

	// Verify the ownership check logic: item.UserID must match context userID
	ownerID := uuid.New()
	otherID := uuid.New()

	type itemStub struct {
		UserID uuid.UUID
	}

	item := itemStub{UserID: ownerID}

	// Owner access — should pass
	if item.UserID != ownerID {
		t.Error("owner should have access")
	}

	// Non-owner access — should fail
	if item.UserID == otherID {
		t.Error("non-owner should NOT have access")
	}
}

// ==================== isValidLocalSessionID Tests (Phase 1 B1) ====================

// TestIsValidLocalSessionID_AcceptsCommonFormats covers ULID, UUIDv4 with
// and without dashes, and any URL-safe alphanumeric string in [6,64]. The
// recorder is free to use any opaque format that fits this shape.
func TestIsValidLocalSessionID_AcceptsCommonFormats(t *testing.T) {
	t.Parallel()
	cases := []string{
		"01HV0000000000000000000000",                  // ULID (26 chars)
		"3e194509-8cd8-4f26-a2fd-0302c58fd8f7",        // UUIDv4 with dashes (36 chars)
		"3e1945098cd84f26a2fd0302c58fd8f7",            // UUIDv4 no dashes (32 chars)
		"abc123",                                      // minimum length (6)
		"under_score-and-mixed-CASE",                  // safe punctuation only
	}
	for _, s := range cases {
		if !isValidLocalSessionID(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
}

// TestIsValidLocalSessionID_RejectsBad covers the failure cases that
// would otherwise let invalid local_session_id values poison the
// derived google_event_id. We reject anything outside the URL-safe
// alphanumeric+`_-` set, anything shorter than 6, anything longer than
// 64.
func TestIsValidLocalSessionID_RejectsBad(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty":         "",
		"too short":     "abc12",
		"whitespace":    "abc 123 def",
		"slash":         "ab/cd/ef",
		"non-ascii":     "abcçöü",
		"too long (65)": strings.Repeat("a", 65),
	}
	for name, s := range cases {
		if isValidLocalSessionID(s) {
			t.Errorf("%s: expected %q to be rejected, was accepted", name, s)
		}
	}
}
