package services

import (
	"context"
	"fmt"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// DecisionService handles business logic for decision items
type DecisionService struct {
	repo repository.DecisionRepository
}

func NewDecisionService() *DecisionService {
	return &DecisionService{
		repo: repository.NewDecisionRepository(),
	}
}

func (s *DecisionService) GetItemsByMeeting(ctx context.Context, meetingID uuid.UUID) ([]models.DecisionItem, error) {
	return s.repo.GetDecisionItemsByMeeting(ctx, meetingID)
}

func (s *DecisionService) CompleteItem(ctx context.Context, itemID uuid.UUID) error {
	item, err := s.repo.GetDecisionItemByID(ctx, itemID)
	if err != nil {
		return fmt.Errorf("item not found: %w", err)
	}
	if item.Status != "confirmed" {
		return fmt.Errorf("only confirmed items can be completed")
	}
	if err := s.repo.UpdateDecisionItemStatus(ctx, itemID, "completed"); err != nil {
		return err
	}
	return s.repo.MarkUserEdited(ctx, itemID)
}

func (s *DecisionService) DismissItem(ctx context.Context, itemID uuid.UUID) error {
	if err := s.repo.UpdateDecisionItemStatus(ctx, itemID, "dismissed"); err != nil {
		return err
	}
	return s.repo.MarkUserEdited(ctx, itemID)
}

func (s *DecisionService) DeleteItem(ctx context.Context, itemID uuid.UUID) error {
	item, err := s.repo.GetDecisionItemByID(ctx, itemID)
	if err != nil {
		return fmt.Errorf("item not found: %w", err)
	}
	if item.Status == "completed" {
		return fmt.Errorf("completed items cannot be deleted")
	}
	return s.repo.DeleteDecisionItem(ctx, itemID)
}

func (s *DecisionService) UpdateItem(ctx context.Context, itemID uuid.UUID, text string, ownerName *string) error {
	if text == "" {
		return fmt.Errorf("text is required")
	}
	return s.repo.UpdateDecisionItem(ctx, itemID, text, ownerName)
}
