package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxStoredJobErrorLength = 4000

type BackgroundJobRepository interface {
	EnqueuePostMeetingCompletion(ctx context.Context, meetingID uuid.UUID, meetingURL string, maxAttempts int) (uuid.UUID, error)
	EnqueueTranscribe(ctx context.Context, meetingID uuid.UUID, maxAttempts int) (uuid.UUID, error)
	EnqueueNotifyBrief(ctx context.Context, deliveryID uuid.UUID, scheduledAt time.Time, maxAttempts int) (uuid.UUID, error)
	ClaimNextPending(ctx context.Context, workerID string) (*models.BackgroundJob, error)
	MarkCompleted(ctx context.Context, jobID uuid.UUID) error
	MarkForRetry(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time, errMessage string) error
	MoveToDLQ(ctx context.Context, job *models.BackgroundJob, errMessage string) error
}

type postgresBackgroundJobRepository struct{}

func NewBackgroundJobRepository() BackgroundJobRepository {
	return &postgresBackgroundJobRepository{}
}

func (r *postgresBackgroundJobRepository) EnqueuePostMeetingCompletion(ctx context.Context, meetingID uuid.UUID, meetingURL string, maxAttempts int) (uuid.UUID, error) {
	if meetingID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("meetingID cannot be nil")
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	payload := models.PostMeetingCompletionJobPayload{
		MeetingID:  meetingID,
		MeetingURL: meetingURL,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to marshal background job payload: %w", err)
	}

	jobID := uuid.New()
	query := `
		INSERT INTO background_jobs (
			id, job_type, payload, status, attempts, max_attempts, scheduled_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pending', 0, $4, NOW(), NOW(), NOW())
	`
	if _, err := DB.Exec(ctx, query, jobID, models.BackgroundJobTypePostMeetingCompletion, payloadJSON, maxAttempts); err != nil {
		return uuid.Nil, fmt.Errorf("failed to enqueue background job: %w", err)
	}

	return jobID, nil
}

// EnqueueNotifyBrief schedules a per-channel brief delivery attempt.
// scheduledAt MUST equal brief_deliveries.scheduled_for so ClaimNextPending
// won't pick the job up before the dispatch window opens; the worker
// then reads the delivery row for the actual send context.
// maxAttempts defaults to 3 (roadmap Karar #14 — 30s/2min/10min).
func (r *postgresBackgroundJobRepository) EnqueueNotifyBrief(ctx context.Context, deliveryID uuid.UUID, scheduledAt time.Time, maxAttempts int) (uuid.UUID, error) {
	if deliveryID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("deliveryID cannot be nil")
	}
	if scheduledAt.IsZero() {
		return uuid.Nil, fmt.Errorf("scheduledAt cannot be zero")
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	payload := models.NotifyBriefPayload{DeliveryID: deliveryID}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to marshal notify_brief payload: %w", err)
	}

	jobID := uuid.New()
	query := `
		INSERT INTO background_jobs (
			id, job_type, payload, status, attempts, max_attempts, scheduled_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pending', 0, $4, $5, NOW(), NOW())
	`
	if _, err := DB.Exec(ctx, query, jobID, models.BackgroundJobTypeNotifyBrief, payloadJSON, maxAttempts, scheduledAt); err != nil {
		return uuid.Nil, fmt.Errorf("failed to enqueue notify_brief job: %w", err)
	}
	return jobID, nil
}

// EnqueueTranscribe schedules a transcribe job for ElevenLabs batch STT.
// The worker resolves the signed audio URL at run time, so we only persist
// the meeting id here.
func (r *postgresBackgroundJobRepository) EnqueueTranscribe(ctx context.Context, meetingID uuid.UUID, maxAttempts int) (uuid.UUID, error) {
	if meetingID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("meetingID cannot be nil")
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	payload := models.TranscribeJobPayload{MeetingID: meetingID}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to marshal transcribe payload: %w", err)
	}

	jobID := uuid.New()
	query := `
		INSERT INTO background_jobs (
			id, job_type, payload, status, attempts, max_attempts, scheduled_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, 'pending', 0, $4, NOW(), NOW(), NOW())
	`
	if _, err := DB.Exec(ctx, query, jobID, models.BackgroundJobTypeTranscribe, payloadJSON, maxAttempts); err != nil {
		return uuid.Nil, fmt.Errorf("failed to enqueue transcribe job: %w", err)
	}
	return jobID, nil
}

func (r *postgresBackgroundJobRepository) ClaimNextPending(ctx context.Context, workerID string) (*models.BackgroundJob, error) {
	query := `
		WITH next_job AS (
			SELECT id
			FROM background_jobs
			WHERE status = 'pending'
			  AND scheduled_at <= NOW()
			ORDER BY scheduled_at ASC, created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE background_jobs bj
		SET status = 'processing',
		    attempts = bj.attempts + 1,
		    started_at = NOW(),
		    locked_by = $1,
		    locked_at = NOW(),
		    updated_at = NOW()
		FROM next_job
		WHERE bj.id = next_job.id
		RETURNING bj.id, bj.job_type, bj.payload, bj.status, bj.attempts, bj.max_attempts, bj.scheduled_at, bj.created_at, bj.updated_at
	`

	var job models.BackgroundJob
	err := DB.QueryRow(ctx, query, workerID).Scan(
		&job.ID,
		&job.JobType,
		&job.Payload,
		&job.Status,
		&job.Attempts,
		&job.MaxAttempts,
		&job.ScheduledAt,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to claim background job: %w", err)
	}

	return &job, nil
}

func (r *postgresBackgroundJobRepository) MarkCompleted(ctx context.Context, jobID uuid.UUID) error {
	query := `
		UPDATE background_jobs
		SET status = 'completed',
		    finished_at = NOW(),
		    last_error = NULL,
		    updated_at = NOW()
		WHERE id = $1
	`
	if _, err := DB.Exec(ctx, query, jobID); err != nil {
		return fmt.Errorf("failed to mark background job completed: %w", err)
	}
	return nil
}

func (r *postgresBackgroundJobRepository) MarkForRetry(ctx context.Context, jobID uuid.UUID, nextRunAt time.Time, errMessage string) error {
	query := `
		UPDATE background_jobs
		SET status = 'pending',
		    scheduled_at = $2,
		    last_error = $3,
		    updated_at = NOW()
		WHERE id = $1
	`
	if _, err := DB.Exec(ctx, query, jobID, nextRunAt, truncateJobError(errMessage)); err != nil {
		return fmt.Errorf("failed to mark background job for retry: %w", err)
	}
	return nil
}

func (r *postgresBackgroundJobRepository) MoveToDLQ(ctx context.Context, job *models.BackgroundJob, errMessage string) error {
	if job == nil {
		return fmt.Errorf("job cannot be nil")
	}

	tx, err := DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction for DLQ move: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE background_jobs
		SET status = 'dead',
		    finished_at = NOW(),
		    last_error = $2,
		    updated_at = NOW()
		WHERE id = $1
	`, job.ID, truncateJobError(errMessage)); err != nil {
		return fmt.Errorf("failed to mark job dead: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO background_jobs_dlq (
			id, original_job_id, job_type, payload, attempts, max_attempts, error_message, failed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`, uuid.New(), job.ID, job.JobType, job.Payload, job.Attempts, job.MaxAttempts, truncateJobError(errMessage)); err != nil {
		return fmt.Errorf("failed to insert DLQ record: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit DLQ move: %w", err)
	}

	return nil
}

func truncateJobError(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "unknown error"
	}
	if len(msg) <= maxStoredJobErrorLength {
		return msg
	}
	return msg[:maxStoredJobErrorLength]
}
