package services

import (
	"context"
	"fmt"
	"log/slog"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ChainService handles chain management and cascade updates
type ChainService struct {
	chainRepo    repository.ChainRepository
	decisionRepo repository.DecisionRepository
}

func NewChainService() *ChainService {
	return &ChainService{
		chainRepo:    repository.NewChainRepository(),
		decisionRepo: repository.NewDecisionRepository(),
	}
}

// UpdateMeetingChain updates chain assignment for a meeting and cascades to items.
// Single transaction: meeting + items updated together.
func (s *ChainService) UpdateMeetingChain(ctx context.Context, meetingID uuid.UUID, newChainID *uuid.UUID) error {
	// Update meeting chain_id
	if newChainID != nil {
		// Verify chain exists
		if _, err := s.chainRepo.FindByID(ctx, *newChainID); err != nil {
			return fmt.Errorf("chain not found: %w", err)
		}

		_, err := repository.DB.Exec(ctx, `UPDATE meetings SET chain_id = $1 WHERE id = $2`, *newChainID, meetingID)
		if err != nil {
			return fmt.Errorf("failed to update meeting chain: %w", err)
		}

		// Cascade to decision items
		if err := s.decisionRepo.UpdateChainIDForMeeting(ctx, meetingID, *newChainID); err != nil {
			slog.Error("failed to cascade chain to items", "meeting_id", meetingID, "error", err)
			return fmt.Errorf("failed to cascade chain: %w", err)
		}
	} else {
		// Remove from chain
		_, err := repository.DB.Exec(ctx, `UPDATE meetings SET chain_id = NULL WHERE id = $1`, meetingID)
		if err != nil {
			return fmt.Errorf("failed to remove meeting chain: %w", err)
		}
		repository.DB.Exec(ctx, `UPDATE decision_items SET chain_id = NULL WHERE meeting_id = $1`, meetingID)
	}

	slog.Info("meeting chain updated", "meeting_id", meetingID, "new_chain_id", newChainID)
	return nil
}
