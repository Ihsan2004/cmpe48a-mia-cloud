package repository

import (
	"context"
	"fmt"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

type NoteRepository interface {
	GetMeetingNote(ctx context.Context, meetingID, userID uuid.UUID) (*models.MeetingNote, error)
	UpsertMeetingNote(ctx context.Context, meetingID, userID uuid.UUID, content string) (*models.MeetingNote, error)
}

type postgresNoteRepository struct{}

func NewNoteRepository() NoteRepository {
	return &postgresNoteRepository{}
}

func (r *postgresNoteRepository) GetMeetingNote(ctx context.Context, meetingID, userID uuid.UUID) (*models.MeetingNote, error) {
	var note models.MeetingNote
	err := DB.QueryRow(ctx, `
		SELECT id, meeting_id, user_id, content, created_at, updated_at
		FROM meeting_notes
		WHERE meeting_id = $1 AND user_id = $2
	`, meetingID, userID).Scan(
		&note.ID, &note.MeetingID, &note.UserID, &note.Content, &note.CreatedAt, &note.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("note not found: %w", err)
	}
	return &note, nil
}

func (r *postgresNoteRepository) UpsertMeetingNote(ctx context.Context, meetingID, userID uuid.UUID, content string) (*models.MeetingNote, error) {
	var note models.MeetingNote
	err := DB.QueryRow(ctx, `
		INSERT INTO meeting_notes (meeting_id, user_id, content)
		VALUES ($1, $2, $3)
		ON CONFLICT (meeting_id, user_id)
		DO UPDATE SET content = $3, updated_at = NOW()
		RETURNING id, meeting_id, user_id, content, created_at, updated_at
	`, meetingID, userID, content).Scan(
		&note.ID, &note.MeetingID, &note.UserID, &note.Content, &note.CreatedAt, &note.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert note: %w", err)
	}
	return &note, nil
}
