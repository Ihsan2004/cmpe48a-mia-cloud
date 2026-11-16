package handlers

import (
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

type MetricsHandler struct {
	svc *services.MetricsService
}

func NewMetricsHandler() *MetricsHandler {
	return &MetricsHandler{
		svc: services.NewMetricsService(),
	}
}

// GetMVPMetrics returns all MVP success metrics (internal use)
func (h *MetricsHandler) GetMVPMetrics(c *fiber.Ctx) error {
	metrics, err := h.svc.CalculateMetrics(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to calculate metrics",
		})
	}

	return c.JSON(fiber.Map{
		"metrics": metrics,
		"summary": services.FormatMetricsSummary(metrics),
		"thresholds": fiber.Map{
			"review_completion_target":  ">50%",
			"brief_eligibility_target":  ">40%",
			"brief_open_rate_target":    ">40%",
			"usefulness_target":         ">60%",
			"link_accuracy_target":      ">90% (manual audit)",
		},
	})
}
