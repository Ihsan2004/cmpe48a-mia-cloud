package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UploadSession mirrors the upload_sessions row.
type UploadSession struct {
	ID            uuid.UUID
	MeetingID     uuid.UUID
	UserID        uuid.UUID
	ContentType   string
	TotalBytes    *int64 // null until client declares it
	ReceivedBytes int64
	StoragePath   string // final audio path, e.g. {user_id}/{meeting_id}/audio.webm
	Status        string // in_progress | completed | aborted
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time
}

// ErrUploadSessionNotFound is returned when no active session exists for the query.
var ErrUploadSessionNotFound = errors.New("upload session not found")

// UploadRepository is the persistence boundary for resumable uploads.
type UploadRepository interface {
	// GetActiveByMeeting returns the latest in_progress session for a meeting, if any.
	GetActiveByMeeting(ctx context.Context, meetingID uuid.UUID) (*UploadSession, error)
	// Create inserts a new session (caller supplies all fields but id/timestamps).
	Create(ctx context.Context, s *UploadSession) error
	// AddReceivedBytes atomically bumps received_bytes by delta; returns the new value.
	AddReceivedBytes(ctx context.Context, sessionID uuid.UUID, delta int64) (int64, error)
	// MarkCompleted flips status to completed + sets completed_at.
	MarkCompleted(ctx context.Context, sessionID uuid.UUID) error
	// MarkAborted flips status to aborted (e.g. client cancelled or cleanup swept it).
	MarkAborted(ctx context.Context, sessionID uuid.UUID) error
	// GetByID is useful for lookups from the handler layer.
	GetByID(ctx context.Context, sessionID uuid.UUID) (*UploadSession, error)
}

type postgresUploadRepository struct{}

// NewUploadRepository wires the default postgres-backed implementation.
func NewUploadRepository() UploadRepository {
	return &postgresUploadRepository{}
}

const uploadSessionColumns = `id, meeting_id, user_id, content_type, total_bytes, received_bytes,
	storage_path, status, created_at, updated_at, completed_at`

func scanUploadSession(row pgx.Row) (*UploadSession, error) {
	var s UploadSession
	err := row.Scan(
		&s.ID, &s.MeetingID, &s.UserID, &s.ContentType, &s.TotalBytes, &s.ReceivedBytes,
		&s.StoragePath, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *postgresUploadRepository) GetActiveByMeeting(ctx context.Context, meetingID uuid.UUID) (*UploadSession, error) {
	query := `SELECT ` + uploadSessionColumns + `
		FROM upload_sessions
		WHERE meeting_id = $1 AND status = 'in_progress'
		ORDER BY created_at DESC
		LIMIT 1`
	row := DB.QueryRow(ctx, query, meetingID)
	s, err := scanUploadSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active upload session: %w", err)
	}
	return s, nil
}

func (r *postgresUploadRepository) Create(ctx context.Context, s *UploadSession) error {
	query := `INSERT INTO upload_sessions
		(meeting_id, user_id, content_type, total_bytes, storage_path)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, received_bytes, status, created_at, updated_at`
	row := DB.QueryRow(ctx, query, s.MeetingID, s.UserID, s.ContentType, s.TotalBytes, s.StoragePath)
	if err := row.Scan(&s.ID, &s.ReceivedBytes, &s.Status, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return fmt.Errorf("create upload session: %w", err)
	}
	return nil
}

func (r *postgresUploadRepository) AddReceivedBytes(ctx context.Context, sessionID uuid.UUID, delta int64) (int64, error) {
	if delta < 0 {
		return 0, fmt.Errorf("add received bytes: delta must be non-negative, got %d", delta)
	}
	query := `UPDATE upload_sessions
		SET received_bytes = received_bytes + $1
		WHERE id = $2 AND status = 'in_progress'
		RETURNING received_bytes`
	var newTotal int64
	row := DB.QueryRow(ctx, query, delta, sessionID)
	if err := row.Scan(&newTotal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrUploadSessionNotFound
		}
		return 0, fmt.Errorf("add received bytes: %w", err)
	}
	return newTotal, nil
}

func (r *postgresUploadRepository) MarkCompleted(ctx context.Context, sessionID uuid.UUID) error {
	query := `UPDATE upload_sessions
		SET status = 'completed', completed_at = now()
		WHERE id = $1 AND status = 'in_progress'`
	tag, err := DB.Exec(ctx, query, sessionID)
	if err != nil {
		return fmt.Errorf("mark upload session completed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUploadSessionNotFound
	}
	return nil
}

func (r *postgresUploadRepository) MarkAborted(ctx context.Context, sessionID uuid.UUID) error {
	query := `UPDATE upload_sessions
		SET status = 'aborted'
		WHERE id = $1 AND status = 'in_progress'`
	_, err := DB.Exec(ctx, query, sessionID)
	if err != nil {
		return fmt.Errorf("mark upload session aborted: %w", err)
	}
	return nil
}

func (r *postgresUploadRepository) GetByID(ctx context.Context, sessionID uuid.UUID) (*UploadSession, error) {
	query := `SELECT ` + uploadSessionColumns + `
		FROM upload_sessions
		WHERE id = $1`
	row := DB.QueryRow(ctx, query, sessionID)
	s, err := scanUploadSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get upload session by id: %w", err)
	}
	return s, nil
}
