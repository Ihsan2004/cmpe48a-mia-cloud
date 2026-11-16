package services

import (
	"context"
	"errors"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// postgresRecordingMethodDeps is the production impl backed by the
// global DB pool. Kept separate from recording_method.go so the test
// file doesn't need to import repository.DB.
type postgresRecordingMethodDeps struct{}

// ErrMeetingNotFoundForRecordingMethod indicates the meeting row is missing.
// Distinct from ErrMeetingNotFound (which means "not owned by caller") so
// the scheduler can handle deletions vs. permission drift separately.
var ErrMeetingNotFoundForRecordingMethod = errors.New("recording method: meeting not found")

func (postgresRecordingMethodDeps) GetMeetingOverride(ctx context.Context, meetingID uuid.UUID) (*string, uuid.UUID, error) {
	const q = `SELECT recording_method_override, user_id FROM meetings WHERE id = $1`
	var override *string
	var userID uuid.UUID
	err := repository.DB.QueryRow(ctx, q, meetingID).Scan(&override, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, ErrMeetingNotFoundForRecordingMethod
	}
	if err != nil {
		return nil, uuid.Nil, err
	}
	return override, userID, nil
}

func (postgresRecordingMethodDeps) GetProfileMethod(ctx context.Context, userID uuid.UUID) (string, error) {
	const q = `SELECT COALESCE(recording_method, 'desktop') FROM profiles WHERE id = $1`
	var m string
	if err := repository.DB.QueryRow(ctx, q, userID).Scan(&m); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "desktop", nil // user exists but no profile row yet → safe default (bot is dormant under soft-hide)
		}
		return "", err
	}
	return m, nil
}
