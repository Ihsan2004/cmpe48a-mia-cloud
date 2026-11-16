package repository

import (
	"context"
	"fmt"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

type BriefRepository interface {
	CreateBrief(ctx context.Context, brief *models.MeetingBrief) error
	GetBriefByMeetingAndUser(ctx context.Context, meetingID, userID uuid.UUID) (*models.MeetingBrief, error)
	GetUpcomingBriefs(ctx context.Context, userID uuid.UUID) ([]models.MeetingBrief, error)
	SetBriefOpened(ctx context.Context, briefID uuid.UUID) error
	SetEmailSent(ctx context.Context, briefID uuid.UUID) error
	UpsertFeedback(ctx context.Context, userID, meetingID uuid.UUID, briefID *uuid.UUID, feedback string, note *string) error
}

type postgresBriefRepository struct{}

func NewBriefRepository() BriefRepository {
	return &postgresBriefRepository{}
}

func (r *postgresBriefRepository) CreateBrief(ctx context.Context, brief *models.MeetingBrief) error {
	if brief.ID == uuid.Nil {
		brief.ID = uuid.New()
	}
	_, err := DB.Exec(ctx, `
		INSERT INTO meeting_briefs (id, user_id, target_meeting_id, chain_id, content, generation_method)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, target_meeting_id) DO UPDATE
		SET content = $5, generation_method = $6, chain_id = $4, updated_at = NOW()
	`, brief.ID, brief.UserID, brief.TargetMeetingID, brief.ChainID, brief.Content, brief.GenerationMethod)
	if err != nil {
		return fmt.Errorf("failed to create brief: %w", err)
	}
	return nil
}

func (r *postgresBriefRepository) GetBriefByMeetingAndUser(ctx context.Context, meetingID, userID uuid.UUID) (*models.MeetingBrief, error) {
	var brief models.MeetingBrief
	err := DB.QueryRow(ctx, `
		SELECT id, user_id, target_meeting_id, chain_id, content, generation_method,
		       opened_at, email_sent_at, created_at, updated_at
		FROM meeting_briefs
		WHERE target_meeting_id = $1 AND user_id = $2
	`, meetingID, userID).Scan(
		&brief.ID, &brief.UserID, &brief.TargetMeetingID, &brief.ChainID,
		&brief.Content, &brief.GenerationMethod,
		&brief.OpenedAt, &brief.EmailSentAt, &brief.CreatedAt, &brief.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("brief not found: %w", err)
	}
	return &brief, nil
}

func (r *postgresBriefRepository) GetUpcomingBriefs(ctx context.Context, userID uuid.UUID) ([]models.MeetingBrief, error) {
	query := `
		SELECT mb.id, mb.user_id, mb.target_meeting_id, mb.chain_id, mb.content, mb.generation_method,
		       mb.opened_at, mb.email_sent_at, mb.created_at, mb.updated_at
		FROM meeting_briefs mb
		JOIN meetings m ON m.id = mb.target_meeting_id
		WHERE mb.user_id = $1
		  AND m.start_time > NOW()
		  AND m.start_time < NOW() + INTERVAL '24 hours'
		ORDER BY m.start_time ASC
	`

	rows, err := DB.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get upcoming briefs: %w", err)
	}
	defer rows.Close()

	var briefs []models.MeetingBrief
	for rows.Next() {
		var brief models.MeetingBrief
		if err := rows.Scan(
			&brief.ID, &brief.UserID, &brief.TargetMeetingID, &brief.ChainID,
			&brief.Content, &brief.GenerationMethod,
			&brief.OpenedAt, &brief.EmailSentAt, &brief.CreatedAt, &brief.UpdatedAt,
		); err != nil {
			continue
		}
		briefs = append(briefs, brief)
	}
	return briefs, nil
}

func (r *postgresBriefRepository) SetBriefOpened(ctx context.Context, briefID uuid.UUID) error {
	// Idempotent — only set if not already set
	_, err := DB.Exec(ctx, `
		UPDATE meeting_briefs SET opened_at = $1 WHERE id = $2 AND opened_at IS NULL
	`, time.Now(), briefID)
	return err
}

func (r *postgresBriefRepository) SetEmailSent(ctx context.Context, briefID uuid.UUID) error {
	_, err := DB.Exec(ctx, `
		UPDATE meeting_briefs SET email_sent_at = NOW() WHERE id = $1
	`, briefID)
	return err
}

func (r *postgresBriefRepository) UpsertFeedback(ctx context.Context, userID, meetingID uuid.UUID, briefID *uuid.UUID, feedback string, note *string) error {
	_, err := DB.Exec(ctx, `
		INSERT INTO brief_feedback (user_id, target_meeting_id, brief_id, feedback, note)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, target_meeting_id)
		DO UPDATE SET feedback = $4, note = $5, updated_at = NOW()
	`, userID, meetingID, briefID, feedback, note)
	if err != nil {
		return fmt.Errorf("failed to upsert feedback: %w", err)
	}
	return nil
}
