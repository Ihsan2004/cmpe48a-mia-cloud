package handlers

import (
	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type BriefHandler struct {
	repo repository.BriefRepository
}

func NewBriefHandler() *BriefHandler {
	return &BriefHandler{
		repo: repository.NewBriefRepository(),
	}
}

// GetUpcomingBriefs returns briefs for upcoming meetings (next 24h)
func (h *BriefHandler) GetUpcomingBriefs(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, _ := uuid.Parse(userIDStr)

	briefs, err := h.repo.GetUpcomingBriefs(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch briefs"})
	}

	result := []fiber.Map{}
	for _, brief := range briefs {
		result = append(result, fiber.Map{
			"id":                brief.ID,
			"target_meeting_id": brief.TargetMeetingID,
			"chain_id":          brief.ChainID,
			"content":           brief.Content,
			"generation_method": brief.GenerationMethod,
			"opened_at":         brief.OpenedAt,
			"created_at":        brief.CreatedAt,
		})
	}

	return c.JSON(fiber.Map{"briefs": result})
}

// GetBriefDetail returns a specific brief and marks it as opened (first view)
func (h *BriefHandler) GetBriefDetail(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, _ := uuid.Parse(userIDStr)

	meetingID, err := uuid.Parse(c.Params("meeting_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	brief, err := h.repo.GetBriefByMeetingAndUser(c.Context(), meetingID, userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Brief not found"})
	}

	// Mark as opened on first view (idempotent)
	if brief.OpenedAt == nil {
		h.repo.SetBriefOpened(c.Context(), brief.ID)
	}

	return c.JSON(fiber.Map{
		"id":                brief.ID,
		"target_meeting_id": brief.TargetMeetingID,
		"chain_id":          brief.ChainID,
		"content":           brief.Content,
		"generation_method": brief.GenerationMethod,
		"opened_at":         brief.OpenedAt,
		"created_at":        brief.CreatedAt,
	})
}

// SubmitFeedback handles brief feedback (upsert — one feedback per user per meeting)
func (h *BriefHandler) SubmitFeedback(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, _ := uuid.Parse(userIDStr)

	meetingID, err := uuid.Parse(c.Params("meeting_id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	var body struct {
		Feedback string  `json:"feedback"`
		Note     *string `json:"note"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	if body.Feedback != "useful" && body.Feedback != "not_useful" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Feedback must be 'useful' or 'not_useful'"})
	}

	// Get brief ID for foreign key
	brief, _ := h.repo.GetBriefByMeetingAndUser(c.Context(), meetingID, userID)
	var briefID *uuid.UUID
	if brief != nil {
		briefID = &brief.ID
	}

	if err := h.repo.UpsertFeedback(c.Context(), userID, meetingID, briefID, body.Feedback, body.Note); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save feedback"})
	}

	return c.JSON(fiber.Map{"message": "Feedback saved"})
}
