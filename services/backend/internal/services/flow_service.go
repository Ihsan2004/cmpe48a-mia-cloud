package services

import (
	"context"
	"fmt"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// FlowService orchestrates flow data retrieval with date parsing and scope handling.
type FlowService struct {
	flowRepo   repository.FlowRepository
	profileSvc *ProfileService
}

// NewFlowService creates a new FlowService with its dependencies.
func NewFlowService() *FlowService {
	return &FlowService{
		flowRepo:   repository.NewFlowRepository(),
		profileSvc: NewProfileService(),
	}
}

// GetFlowData retrieves aggregated flow data (meetings, chains, decisions) for a user.
// Parses date range strings (RFC3339), defaults to current work week, caps at 90 days.
func (s *FlowService) GetFlowData(ctx context.Context, userID uuid.UUID, startStr, endStr, scope string) (*repository.FlowData, error) {
	now := time.Now()
	var start, end time.Time

	if startStr != "" {
		parsed, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return nil, fmt.Errorf("invalid start date: %w", err)
		}
		start = parsed
	} else {
		// Default: Monday 00:00 of current week
		weekday := now.Weekday()
		offset := int(weekday) - int(time.Monday)
		if offset < 0 {
			offset += 7
		}
		start = time.Date(now.Year(), now.Month(), now.Day()-offset, 0, 0, 0, 0, now.Location())
	}

	if endStr != "" {
		parsed, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return nil, fmt.Errorf("invalid end date: %w", err)
		}
		end = parsed
	} else {
		// Default: Friday 23:59:59 of current week
		weekday := now.Weekday()
		offset := int(weekday) - int(time.Monday)
		if offset < 0 {
			offset += 7
		}
		end = time.Date(now.Year(), now.Month(), now.Day()-offset+4, 23, 59, 59, 0, now.Location())
	}

	// Cap maximum range at 90 days to prevent unbounded queries
	if end.Sub(start) > 90*24*time.Hour {
		return nil, fmt.Errorf("date range exceeds 90 day maximum")
	}

	// Resolve user email for team scope attendee matching
	userEmail := ""
	if scope == "team" {
		profile, err := s.profileSvc.GetProfile(userID)
		if err != nil {
			return nil, fmt.Errorf("failed to get user profile: %w", err)
		}
		if profile.Email != nil {
			userEmail = *profile.Email
		}
	}

	return s.flowRepo.GetFlowData(ctx, userID, userEmail, start, end, scope)
}
