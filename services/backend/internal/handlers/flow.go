package handlers

import (
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

// FlowHandler handles HTTP requests for the flow visualization endpoint.
type FlowHandler struct {
	flowService *services.FlowService
}

// NewFlowHandler creates a new FlowHandler with its dependencies.
func NewFlowHandler() *FlowHandler {
	return &FlowHandler{
		flowService: services.NewFlowService(),
	}
}

// GetFlowData returns aggregated meetings, chains, and decisions for flow visualization.
// Query params: start (RFC3339), end (RFC3339), scope ("my"|"team").
func (h *FlowHandler) GetFlowData(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Unauthorized",
		})
	}

	startStr := c.Query("start")
	endStr := c.Query("end")
	scope := c.Query("scope", "my")

	if scope != "my" && scope != "team" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid scope. Use 'my' or 'team'.",
		})
	}

	data, err := h.flowService.GetFlowData(c.Context(), userID, startStr, endStr, scope)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch flow data",
		})
	}

	return c.JSON(data)
}
