package handlers

import (
	"errors"
	"fmt"

	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// These indirection hooks let tests substitute DB-backed calls with mocks.
// See upload.go for the function variables that reference them.

func defaultLookupMeetingOwner(c *fiber.Ctx, meetingID uuid.UUID) (uuid.UUID, error) {
	repo := repository.NewMeetingRepository()
	m, err := repo.GetMeetingByIDSystem(c.Context(), meetingID)
	if err != nil {
		return uuid.Nil, errors.New("meeting not found")
	}
	return m.UserID, nil
}

func defaultVerifyOwnership(c *fiber.Ctx, userID, meetingID uuid.UUID) error {
	repo := repository.NewMeetingRepository()
	if err := repo.VerifyMeetingOwnership(c.Context(), userID, meetingID); err != nil {
		return errors.New("meeting not found")
	}
	return nil
}

func defaultGetActiveSession(c *fiber.Ctx, meetingID uuid.UUID) (*repository.UploadSession, error) {
	repo := repository.NewUploadRepository()
	s, err := repo.GetActiveByMeeting(c.Context(), meetingID)
	if err != nil {
		if errors.Is(err, repository.ErrUploadSessionNotFound) {
			return nil, fmt.Errorf("no active upload session for meeting %s", meetingID)
		}
		return nil, err
	}
	return s, nil
}
