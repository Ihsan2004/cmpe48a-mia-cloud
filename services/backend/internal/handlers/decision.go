package handlers

import (
	"fmt"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type DecisionHandler struct {
	repo        repository.DecisionRepository
	meetingRepo repository.MeetingRepository
	svc         *services.DecisionService
	extractor   *services.DecisionExtractor
}

func NewDecisionHandler() *DecisionHandler {
	return &DecisionHandler{
		repo:        repository.NewDecisionRepository(),
		meetingRepo: repository.NewMeetingRepository(),
		svc:         services.NewDecisionService(),
		extractor:   services.NewDecisionExtractor(),
	}
}

// getUserIDFromContext extracts and parses the userID from JWT locals.
func getUserIDFromContext(c *fiber.Ctx) (uuid.UUID, error) {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok || userIDStr == "" {
		return uuid.Nil, fmt.Errorf("unauthorized")
	}
	uid, err := uuid.Parse(userIDStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid user ID")
	}
	return uid, nil
}

// GetMeetingDecisionItems returns decision items for a meeting grouped by type
func (h *DecisionHandler) GetMeetingDecisionItems(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	// Verify meeting belongs to user
	if err := h.meetingRepo.VerifyMeetingOwnership(c.Context(), userID, meetingID); err != nil {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Forbidden"})
	}

	items, err := h.repo.GetDecisionItemsByMeeting(c.Context(), meetingID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch decision items"})
	}

	// Group by type
	decisions := []fiber.Map{}
	actionItems := []fiber.Map{}
	openTopics := []fiber.Map{}

	for _, item := range items {
		itemMap := fiber.Map{
			"id":                       item.ID,
			"meeting_id":               item.MeetingID,
			"chain_id":                 item.ChainID,
			"item_type":                item.ItemType,
			"text":                     item.Text,
			"owner_name":               item.OwnerName,
			"source_quote":             item.SourceQuote,
			"context":                  item.Context,
			"alternatives_considered":  item.AlternativesConsidered,
			"decision_timestamp_start": item.DecisionTimestampStart,
			"decision_timestamp_end":   item.DecisionTimestampEnd,
			"status":                   item.Status,
			"user_edited":              item.UserEdited,
			"confirmed_at":             item.ConfirmedAt,
			"completed_at":             item.CompletedAt,
			"created_at":               item.CreatedAt,
		}

		switch item.ItemType {
		case "decision":
			decisions = append(decisions, itemMap)
		case "action_item":
			actionItems = append(actionItems, itemMap)
		case "open_topic":
			openTopics = append(openTopics, itemMap)
		}
	}

	return c.JSON(fiber.Map{
		"decisions":    decisions,
		"action_items": actionItems,
		"open_topics":  openTopics,
	})
}

// verifyItemOwnership fetches a decision item and checks that the JWT user owns it.
func (h *DecisionHandler) verifyItemOwnership(c *fiber.Ctx, itemID uuid.UUID) (*fiber.Error, uuid.UUID) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "Unauthorized"), uuid.Nil
	}

	item, err := h.repo.GetDecisionItemByID(c.Context(), itemID)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "Item not found"), uuid.Nil
	}

	if item.UserID != userID {
		return fiber.NewError(fiber.StatusForbidden, "Forbidden"), uuid.Nil
	}

	return nil, userID
}

// UpdateItem updates text and owner of a decision item
func (h *DecisionHandler) UpdateItem(c *fiber.Ctx) error {
	itemID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid item ID"})
	}

	if fErr, _ := h.verifyItemOwnership(c, itemID); fErr != nil {
		return c.Status(fErr.Code).JSON(fiber.Map{"error": fErr.Message})
	}

	var body struct {
		Text      string  `json:"text"`
		OwnerName *string `json:"owner_name"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	if err := h.svc.UpdateItem(c.Context(), itemID, body.Text, body.OwnerName); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"message": "Item updated", "id": itemID})
}

// CompleteItem marks a confirmed item as completed
func (h *DecisionHandler) CompleteItem(c *fiber.Ctx) error {
	itemID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid item ID"})
	}

	if fErr, _ := h.verifyItemOwnership(c, itemID); fErr != nil {
		return c.Status(fErr.Code).JSON(fiber.Map{"error": fErr.Message})
	}

	if err := h.svc.CompleteItem(c.Context(), itemID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"message": "Item completed", "id": itemID})
}

// DismissItem dismisses a decision item
func (h *DecisionHandler) DismissItem(c *fiber.Ctx) error {
	itemID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid item ID"})
	}

	if fErr, _ := h.verifyItemOwnership(c, itemID); fErr != nil {
		return c.Status(fErr.Code).JSON(fiber.Map{"error": fErr.Message})
	}

	if err := h.svc.DismissItem(c.Context(), itemID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to dismiss item"})
	}

	return c.JSON(fiber.Map{"message": "Item dismissed", "id": itemID})
}

// DeleteItem deletes a decision item (confirmed only, not completed)
func (h *DecisionHandler) DeleteItem(c *fiber.Ctx) error {
	itemID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid item ID"})
	}

	if fErr, _ := h.verifyItemOwnership(c, itemID); fErr != nil {
		return c.Status(fErr.Code).JSON(fiber.Map{"error": fErr.Message})
	}

	if err := h.svc.DeleteItem(c.Context(), itemID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"message": "Item deleted", "id": itemID})
}

// RetryExtraction triggers decision extraction for a meeting (manual retry)
func (h *DecisionHandler) RetryExtraction(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	meetingID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	// Verify meeting belongs to user
	if err := h.meetingRepo.VerifyMeetingOwnership(c.Context(), userID, meetingID); err != nil {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Forbidden"})
	}

	// Get meeting to retrieve chain_id
	meeting, err := h.meetingRepo.GetMeetingByID(c.Context(), userID, meetingID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Meeting not found"})
	}

	// Run extraction in background
	go h.extractor.ExtractFromMeeting(meetingID, userID, meeting.ChainID)

	return c.JSON(fiber.Map{"message": "Extraction started", "meeting_id": meetingID})
}
