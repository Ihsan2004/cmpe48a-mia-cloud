package handlers

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type UsageHandler struct {
	repo        repository.UsageLogRepository
	meetingRepo repository.MeetingRepository
	quota       quota.Service
}

func NewUsageHandler() *UsageHandler {
	return &UsageHandler{
		repo:        repository.NewUsageLogRepository(),
		meetingRepo: repository.NewMeetingRepository(),
		quota:       quota.NewService(repository.NewUserQuotaRepository()),
	}
}

// GetMyQuota handles GET /api/usage/me — the user-facing quota status
// view. Dollar figures are deliberately omitted; only used_pct + reset
// timestamp + tier ship in the payload (Pivot UX: kullanıcı dolar
// görmez). Admin views (Faz 5) keep the dollar payload separately
// behind ADMIN_API_KEY.
func (h *UsageHandler) GetMyQuota(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user ID"})
	}

	status, err := h.quota.Check(c.Context(), userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserQuotaNotFound) {
			// Trigger + backfill make this an anomaly; respond as if
			// the user is on the default free tier so the UI can still
			// render something sensible.
			slog.Error("GetMyQuota: user_quotas row missing", "user_id", userID)
			return c.JSON(fiber.Map{
				"tier":            "free",
				"used_pct":        0,
				"cycle_starts_at": nil,
				"cycle_resets_at": nil,
			})
		}
		slog.Error("GetMyQuota: quota check failed", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Kotanın durumu alınamadı."})
	}

	resp := fiber.Map{
		"tier":     status.Tier,
		"used_pct": status.UsedPct,
	}
	if !status.CycleStartAt.IsZero() {
		resp["cycle_starts_at"] = status.CycleStartAt.UTC().Format(time.RFC3339)
	} else {
		resp["cycle_starts_at"] = nil
	}
	if !status.CycleEndAt.IsZero() {
		resp["cycle_resets_at"] = status.CycleEndAt.UTC().Format(time.RFC3339)
	} else {
		resp["cycle_resets_at"] = nil
	}
	return c.JSON(resp)
}

// GetUserUsageSummary handles GET /api/usage/summary?month=2026-03
func (h *UsageHandler) GetUserUsageSummary(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user ID"})
	}

	year, month, err := parseMonthParam(c.Query("month"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	features, err := h.repo.GetUserUsageSummary(c.Context(), userID, year, month)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Kullanım bilgisi alınamadı."})
	}

	return c.JSON(buildUsageSummaryResponse(year, month, features))
}

// GetMeetingUsage handles GET /api/meetings/:id/usage
func (h *UsageHandler) GetMeetingUsage(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user ID"})
	}

	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	if err := h.meetingRepo.VerifyMeetingOwnership(c.Context(), userID, meetingID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Toplantı bulunamadı."})
	}

	logs, err := h.repo.GetMeetingUsage(c.Context(), meetingID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Kullanım bilgisi alınamadı."})
	}

	return c.JSON(buildMeetingUsageResponse(meetingID, logs))
}

func buildMeetingUsageResponse(meetingID uuid.UUID, logs []repository.UsageLog) fiber.Map {
	items := make([]fiber.Map, 0, len(logs))
	for _, log := range logs {
		items = append(items, fiber.Map{
			"feature":       log.Feature,
			"provider":      log.Provider,
			"model":         log.Model,
			"input_tokens":  log.InputTokens,
			"output_tokens": log.OutputTokens,
			"audio_seconds": log.AudioSeconds,
			"metadata":      log.Metadata,
			"created_at":    log.CreatedAt.Format(time.RFC3339),
		})
	}

	return fiber.Map{
		"meeting_id": meetingID.String(),
		"usage":      items,
	}
}

// parseMonthParam parses "2026-03" format, defaults to current month
func parseMonthParam(raw string) (int, time.Month, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		now := time.Now()
		return now.Year(), now.Month(), nil
	}

	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fiber.NewError(fiber.StatusBadRequest, "month format must be YYYY-MM")
	}

	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 2020 || year > 2100 {
		return 0, 0, fiber.NewError(fiber.StatusBadRequest, "invalid year in month parameter")
	}

	monthNum, err := strconv.Atoi(parts[1])
	if err != nil || monthNum < 1 || monthNum > 12 {
		return 0, 0, fiber.NewError(fiber.StatusBadRequest, "invalid month in month parameter")
	}

	return year, time.Month(monthNum), nil
}

// buildUsageSummaryResponse creates the standard summary response shape
func buildUsageSummaryResponse(year int, month time.Month, features []repository.FeatureUsage) fiber.Map {
	featureMap := make(fiber.Map, len(features))

	for _, f := range features {
		featureMap[f.Feature] = fiber.Map{
			"total_input_tokens":  f.TotalInput,
			"total_output_tokens": f.TotalOutput,
			"total_audio_seconds": f.TotalAudio,
			"count":               f.RequestCount,
		}
	}

	return fiber.Map{
		"month":    time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).Format("2006-01"),
		"features": featureMap,
	}
}
