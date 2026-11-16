package handlers

import (
	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type NoteHandler struct {
	repo repository.NoteRepository
}

func NewNoteHandler() *NoteHandler {
	return &NoteHandler{
		repo: repository.NewNoteRepository(),
	}
}

// GetMeetingNote returns the user's personal note for a meeting
func (h *NoteHandler) GetMeetingNote(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, _ := uuid.Parse(userIDStr)

	meetingID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	note, err := h.repo.GetMeetingNote(c.Context(), meetingID, userID)
	if err != nil {
		return c.JSON(fiber.Map{"content": nil}) // No note yet
	}

	return c.JSON(fiber.Map{
		"id":         note.ID,
		"meeting_id": note.MeetingID,
		"content":    note.Content,
		"created_at": note.CreatedAt,
		"updated_at": note.UpdatedAt,
	})
}

// UpsertMeetingNote creates or updates a personal note for a meeting
func (h *NoteHandler) UpsertMeetingNote(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, _ := uuid.Parse(userIDStr)

	meetingID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	note, err := h.repo.UpsertMeetingNote(c.Context(), meetingID, userID, body.Content)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save note"})
	}

	return c.JSON(fiber.Map{
		"id":         note.ID,
		"meeting_id": note.MeetingID,
		"content":    note.Content,
		"updated_at": note.UpdatedAt,
	})
}
