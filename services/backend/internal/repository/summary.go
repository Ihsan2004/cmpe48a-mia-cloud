package repository

import (
	"context"
	"fmt"
	"strings"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SummaryRepository interface {
	CreateSummary(ctx context.Context, summary *models.MeetingSummary) error
	GetSummariesByMeetingID(ctx context.Context, meetingID uuid.UUID) ([]models.MeetingSummary, error)
	GetMeetingLogs(ctx context.Context, meetingID uuid.UUID) ([]string, error)
	GetLatestInterimSummary(ctx context.Context, meetingID uuid.UUID) (*models.MeetingSummary, error)
	// Error tracking
	CreateSummaryError(ctx context.Context, summaryErr *models.SummaryError) error
	ResolveErrors(ctx context.Context, meetingID uuid.UUID) error
}

type postgresSummaryRepository struct{}

func NewSummaryRepository() SummaryRepository {
	return &postgresSummaryRepository{}
}

func (r *postgresSummaryRepository) CreateSummary(ctx context.Context, summary *models.MeetingSummary) error {
	query := `
		INSERT INTO meeting_summaries (id, meeting_id, content, summary_type, created_at)
		VALUES ($1, $2, $3, $4, NOW())
	`
	_, err := DB.Exec(ctx, query,
		summary.ID,
		summary.MeetingID,
		summary.Content,
		summary.SummaryType,
	)
	if err != nil {
		return fmt.Errorf("failed to create summary: %w", err)
	}
	return nil
}

func (r *postgresSummaryRepository) GetSummariesByMeetingID(ctx context.Context, meetingID uuid.UUID) ([]models.MeetingSummary, error) {
	query := `
		SELECT id, meeting_id, content, summary_type, created_at, updated_at
		FROM meeting_summaries
		WHERE meeting_id = $1
		ORDER BY created_at DESC
	`
	rows, err := DB.Query(ctx, query, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch summaries: %w", err)
	}
	defer rows.Close()

	var summaries []models.MeetingSummary
	for rows.Next() {
		var s models.MeetingSummary
		if err := rows.Scan(&s.ID, &s.MeetingID, &s.Content, &s.SummaryType, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, nil
}

func (r *postgresSummaryRepository) GetMeetingLogs(ctx context.Context, meetingID uuid.UUID) ([]string, error) {
	query := `
		SELECT text
		FROM transcript_segments
		WHERE meeting_id = $1
		ORDER BY start_time ASC, created_at ASC
	`
	rows, err := DB.Query(ctx, query, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch meeting logs: %w", err)
	}
	defer rows.Close()

	var chunks []string
	for rows.Next() {
		var chunk string
		if err := rows.Scan(&chunk); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	var validChunks []string
	for _, c := range chunks {
		trimmed := strings.TrimSpace(c)
		if trimmed != "" {
			validChunks = append(validChunks, trimmed)
		}
	}
	return validChunks, nil
}

func (r *postgresSummaryRepository) GetLatestInterimSummary(ctx context.Context, meetingID uuid.UUID) (*models.MeetingSummary, error) {
	query := `
		SELECT id, meeting_id, content, summary_type, created_at, updated_at
		FROM meeting_summaries
		WHERE meeting_id = $1 AND summary_type = 'interim'
		ORDER BY created_at DESC
		LIMIT 1
	`
	row := DB.QueryRow(ctx, query, meetingID)
	var s models.MeetingSummary
	if err := row.Scan(&s.ID, &s.MeetingID, &s.Content, &s.SummaryType, &s.CreatedAt, &s.UpdatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch latest interim summary: %w", err)
	}
	return &s, nil
}

func (r *postgresSummaryRepository) CreateSummaryError(ctx context.Context, summaryErr *models.SummaryError) error {
	query := `
		INSERT INTO summary_errors (id, meeting_id, error_type, error_message, error_detail, phase, retry_count, resolved, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, FALSE, NOW())
	`
	_, err := DB.Exec(ctx, query,
		summaryErr.ID,
		summaryErr.MeetingID,
		summaryErr.ErrorType,
		summaryErr.ErrorMessage,
		summaryErr.ErrorDetail,
		summaryErr.Phase,
		summaryErr.RetryCount,
	)
	if err != nil {
		return fmt.Errorf("failed to create summary error: %w", err)
	}
	return nil
}

func (r *postgresSummaryRepository) ResolveErrors(ctx context.Context, meetingID uuid.UUID) error {
	query := `
		UPDATE summary_errors
		SET resolved = TRUE, resolved_at = NOW()
		WHERE meeting_id = $1 AND resolved = FALSE
	`
	_, err := DB.Exec(ctx, query, meetingID)
	if err != nil {
		return fmt.Errorf("failed to resolve errors: %w", err)
	}
	return nil
}
