package handlers

import (
	"os"
	"strconv"
	"strings"

	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// AdminBriefDeliveryHandler exposes the read-only audit view for ops.
// Gated by a shared admin-key env var (ADMIN_API_KEY) matched against
// the Authorization: Bearer header — we don't have a role system in
// the user table yet, and rolling a per-user admin flag just for this
// one endpoint is YAGNI for the POC.
type AdminBriefDeliveryHandler struct {
	repo     repository.BriefDeliveryRepository
	adminKey string // ADMIN_API_KEY; empty disables the endpoint
}

// NewAdminBriefDeliveryHandler returns a handler. Caller should skip
// registering the route in routes.Setup when adminKey is empty so the
// endpoint 404s instead of accepting unauthenticated traffic.
func NewAdminBriefDeliveryHandler(repo repository.BriefDeliveryRepository, adminKey string) *AdminBriefDeliveryHandler {
	return &AdminBriefDeliveryHandler{repo: repo, adminKey: adminKey}
}

// NewAdminBriefDeliveryHandlerFromEnv reads ADMIN_API_KEY. Returns nil
// when unset so main.go can branch off a single nil check.
func NewAdminBriefDeliveryHandlerFromEnv(repo repository.BriefDeliveryRepository) *AdminBriefDeliveryHandler {
	key := strings.TrimSpace(os.Getenv("ADMIN_API_KEY"))
	if key == "" {
		return nil
	}
	return NewAdminBriefDeliveryHandler(repo, key)
}

// List handles GET /api/admin/brief-deliveries. Query params (all
// optional):
//   - meeting_id: UUID
//   - user_id:    UUID
//   - channel:    "email" | "slack"
//   - status:     "pending" | "sent" | "failed" | "skipped"
//   - limit:      1..500 (default 100)
//
// Auth: Authorization: Bearer <ADMIN_API_KEY>. No JWT — ops users
// aren't expected to log in through the SPA for this endpoint.
func (h *AdminBriefDeliveryHandler) List(c *fiber.Ctx) error {
	if !h.checkAdminAuth(c) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	filter := repository.BriefDeliveryFilter{}

	if raw := strings.TrimSpace(c.Query("meeting_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting_id"})
		}
		filter.MeetingID = id
	}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user_id"})
		}
		filter.UserID = id
	}
	if raw := strings.TrimSpace(c.Query("channel")); raw != "" {
		if raw != "email" && raw != "slack" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "channel must be email or slack"})
		}
		filter.Channel = raw
	}
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		switch raw {
		case "pending", "sent", "failed", "skipped":
			filter.Status = raw
		default:
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
		}
	}
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "limit must be a positive integer"})
		}
		filter.Limit = n
	}

	rows, err := h.repo.ListRecent(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load deliveries"})
	}

	// Response is a flat array + count — keep the shape simple so ops
	// tooling (curl | jq) doesn't need to walk a nested envelope.
	items := make([]fiber.Map, 0, len(rows))
	for _, d := range rows {
		items = append(items, fiber.Map{
			"id":                  d.ID,
			"meeting_id":          d.MeetingID,
			"user_id":             d.UserID,
			"channel":             d.Channel,
			"status":              d.Status,
			"attempts":            d.Attempts,
			"last_error":          d.LastError,
			"provider_message_id": d.ProviderMessageID,
			"scheduled_for":       d.ScheduledFor,
			"sent_at":             d.SentAt,
			"created_at":          d.CreatedAt,
			"updated_at":          d.UpdatedAt,
		})
	}
	return c.JSON(fiber.Map{"count": len(items), "deliveries": items})
}

// checkAdminAuth matches the Authorization header against the
// configured admin key in constant time. Returns false when the key
// isn't set (handler built for a test or mis-configured deploy) to
// fail closed rather than open.
func (h *AdminBriefDeliveryHandler) checkAdminAuth(c *fiber.Ctx) bool {
	if h.adminKey == "" {
		return false
	}
	got := strings.TrimSpace(c.Get("Authorization"))
	got = strings.TrimPrefix(got, "Bearer ")
	got = strings.TrimSpace(got)
	return constantTimeEquals(got, h.adminKey)
}

// constantTimeEquals prevents timing oracles on the admin key. Bytes
// of unequal length can't match anyway (bail early) but the loop
// below stays constant-time over the common length to avoid leaking
// the shared prefix.
func constantTimeEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
