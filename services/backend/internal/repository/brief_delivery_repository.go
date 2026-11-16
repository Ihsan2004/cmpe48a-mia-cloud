package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBriefDeliveryDuplicate is returned by Create when the UNIQUE
// (meeting_id, channel) constraint fires. Callers use this to treat
// idempotent re-enqueue attempts as no-ops (roadmap Karar #15).
var ErrBriefDeliveryDuplicate = errors.New("brief delivery already exists for this meeting+channel")

// BriefDeliveryRepository is the persistence contract for per-channel
// delivery audit records. The interface stays narrow — the dispatcher
// (briefnotify.Dispatcher) mocks it in unit tests, so every method here
// gets a fake counterpart.
type BriefDeliveryRepository interface {
	Create(ctx context.Context, delivery *models.BriefDelivery) error
	GetByID(ctx context.Context, id uuid.UUID) (*models.BriefDelivery, error)
	GetByMeetingChannel(ctx context.Context, meetingID uuid.UUID, channel string) (*models.BriefDelivery, error)
	ListPendingDue(ctx context.Context, now time.Time, limit int) ([]models.BriefDelivery, error)
	ListRecent(ctx context.Context, filter BriefDeliveryFilter) ([]models.BriefDelivery, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status string, providerMsgID string, lastErr string) error
	IncrementAttempts(ctx context.Context, id uuid.UUID) error
}

// BriefDeliveryFilter narrows the audit list query. Zero values are
// wildcards: empty MeetingID → all meetings, empty Status → all states.
// Limit is clamped to [1, 500] to protect against accidental full-table
// dumps from the admin dashboard.
type BriefDeliveryFilter struct {
	MeetingID uuid.UUID
	UserID    uuid.UUID
	Channel   string
	Status    string
	Limit     int
}

type postgresBriefDeliveryRepository struct{}

func NewBriefDeliveryRepository() BriefDeliveryRepository {
	return &postgresBriefDeliveryRepository{}
}

func (r *postgresBriefDeliveryRepository) Create(ctx context.Context, delivery *models.BriefDelivery) error {
	if delivery == nil {
		return fmt.Errorf("delivery cannot be nil")
	}
	if delivery.MeetingID == uuid.Nil {
		return fmt.Errorf("delivery.MeetingID cannot be nil")
	}
	if delivery.UserID == uuid.Nil {
		return fmt.Errorf("delivery.UserID cannot be nil")
	}
	if delivery.Channel != models.BriefDeliveryChannelEmail && delivery.Channel != models.BriefDeliveryChannelSlack {
		return fmt.Errorf("invalid channel: %q", delivery.Channel)
	}
	if delivery.ID == uuid.Nil {
		delivery.ID = uuid.New()
	}
	if delivery.Status == "" {
		delivery.Status = models.BriefDeliveryStatusPending
	}

	query := `
		INSERT INTO brief_deliveries (
			id, meeting_id, user_id, channel, status, attempts,
			scheduled_for, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW())
		RETURNING created_at, updated_at
	`
	err := DB.QueryRow(
		ctx, query,
		delivery.ID, delivery.MeetingID, delivery.UserID,
		delivery.Channel, delivery.Status, delivery.Attempts,
		delivery.ScheduledFor,
	).Scan(&delivery.CreatedAt, &delivery.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		// 23505 = unique_violation. The only UNIQUE on this table is
		// (meeting_id, channel), so any such error means we're trying
		// to double-schedule the same channel for the same meeting.
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrBriefDeliveryDuplicate
		}
		return fmt.Errorf("failed to insert brief_deliveries: %w", err)
	}
	return nil
}

func (r *postgresBriefDeliveryRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.BriefDelivery, error) {
	if id == uuid.Nil {
		return nil, fmt.Errorf("id cannot be nil")
	}
	return r.queryOne(ctx, `
		SELECT id, meeting_id, user_id, channel, status, attempts,
		       last_error, provider_message_id, scheduled_for, sent_at,
		       created_at, updated_at
		FROM brief_deliveries
		WHERE id = $1
	`, id)
}

func (r *postgresBriefDeliveryRepository) GetByMeetingChannel(ctx context.Context, meetingID uuid.UUID, channel string) (*models.BriefDelivery, error) {
	if meetingID == uuid.Nil {
		return nil, fmt.Errorf("meetingID cannot be nil")
	}
	return r.queryOne(ctx, `
		SELECT id, meeting_id, user_id, channel, status, attempts,
		       last_error, provider_message_id, scheduled_for, sent_at,
		       created_at, updated_at
		FROM brief_deliveries
		WHERE meeting_id = $1 AND channel = $2
	`, meetingID, channel)
}

func (r *postgresBriefDeliveryRepository) queryOne(ctx context.Context, sql string, args ...any) (*models.BriefDelivery, error) {
	var d models.BriefDelivery
	err := DB.QueryRow(ctx, sql, args...).Scan(
		&d.ID, &d.MeetingID, &d.UserID, &d.Channel, &d.Status, &d.Attempts,
		&d.LastError, &d.ProviderMessageID, &d.ScheduledFor, &d.SentAt,
		&d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read brief_deliveries row: %w", err)
	}
	return &d, nil
}

// ListPendingDue returns all deliveries whose scheduled_for has elapsed
// and are still status='pending'. The dispatch window in BriefScheduler
// feeds this list into the background_jobs queue.
func (r *postgresBriefDeliveryRepository) ListPendingDue(ctx context.Context, now time.Time, limit int) ([]models.BriefDelivery, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := DB.Query(ctx, `
		SELECT id, meeting_id, user_id, channel, status, attempts,
		       last_error, provider_message_id, scheduled_for, sent_at,
		       created_at, updated_at
		FROM brief_deliveries
		WHERE status = 'pending' AND scheduled_for <= $1
		ORDER BY scheduled_for ASC
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending deliveries: %w", err)
	}
	defer rows.Close()

	var out []models.BriefDelivery
	for rows.Next() {
		var d models.BriefDelivery
		if err := rows.Scan(
			&d.ID, &d.MeetingID, &d.UserID, &d.Channel, &d.Status, &d.Attempts,
			&d.LastError, &d.ProviderMessageID, &d.ScheduledFor, &d.SentAt,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan pending delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListRecent powers the admin audit endpoint. Returns the 100 most
// recently-updated rows that match the filter, newest first. All filter
// fields are ANDed; unset fields are wildcards. Limit is clamped:
// 0 or negative → 100 (sensible default), >500 → 500 (dashboard cap).
func (r *postgresBriefDeliveryRepository) ListRecent(ctx context.Context, filter BriefDeliveryFilter) ([]models.BriefDelivery, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	// Build the WHERE clause dynamically so the SQL stays in a single
	// plan-cacheable form per distinct filter combination (pgx caches
	// on the exact SQL text). Using parameterised placeholders prevents
	// injection; concatenating the fragments ourselves is simpler than
	// pulling in a query-builder dep.
	var (
		where []string
		args  []any
	)
	if filter.MeetingID != uuid.Nil {
		args = append(args, filter.MeetingID)
		where = append(where, fmt.Sprintf("meeting_id = $%d", len(args)))
	}
	if filter.UserID != uuid.Nil {
		args = append(args, filter.UserID)
		where = append(where, fmt.Sprintf("user_id = $%d", len(args)))
	}
	if filter.Channel != "" {
		args = append(args, filter.Channel)
		where = append(where, fmt.Sprintf("channel = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}

	sql := `
		SELECT id, meeting_id, user_id, channel, status, attempts,
		       last_error, provider_message_id, scheduled_for, sent_at,
		       created_at, updated_at
		FROM brief_deliveries
	`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", len(args))

	rows, err := DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query brief_deliveries: %w", err)
	}
	defer rows.Close()

	var out []models.BriefDelivery
	for rows.Next() {
		var d models.BriefDelivery
		if err := rows.Scan(
			&d.ID, &d.MeetingID, &d.UserID, &d.Channel, &d.Status, &d.Attempts,
			&d.LastError, &d.ProviderMessageID, &d.ScheduledFor, &d.SentAt,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan delivery row: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateStatus transitions a delivery into a terminal state (sent/failed/
// skipped). lastErr and providerMsgID are both optional — empty strings
// are persisted as NULL so the audit doesn't accumulate stale "" values.
// sent_at is set only when status='sent'.
func (r *postgresBriefDeliveryRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status string, providerMsgID string, lastErr string) error {
	if id == uuid.Nil {
		return fmt.Errorf("id cannot be nil")
	}
	switch status {
	case models.BriefDeliveryStatusSent,
		models.BriefDeliveryStatusFailed,
		models.BriefDeliveryStatusSkipped,
		models.BriefDeliveryStatusPending:
		// ok
	default:
		return fmt.Errorf("invalid status: %q", status)
	}

	var (
		msgIDParam *string
		errParam   *string
	)
	if providerMsgID != "" {
		msgIDParam = &providerMsgID
	}
	if lastErr != "" {
		errParam = &lastErr
	}

	query := `
		UPDATE brief_deliveries
		SET status = $2,
		    provider_message_id = COALESCE($3, provider_message_id),
		    last_error = $4,
		    sent_at = CASE WHEN $2 = 'sent' THEN NOW() ELSE sent_at END,
		    updated_at = NOW()
		WHERE id = $1
	`
	if _, err := DB.Exec(ctx, query, id, status, msgIDParam, errParam); err != nil {
		return fmt.Errorf("failed to update brief delivery status: %w", err)
	}
	return nil
}

func (r *postgresBriefDeliveryRepository) IncrementAttempts(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return fmt.Errorf("id cannot be nil")
	}
	if _, err := DB.Exec(ctx, `
		UPDATE brief_deliveries
		SET attempts = attempts + 1, updated_at = NOW()
		WHERE id = $1
	`, id); err != nil {
		return fmt.Errorf("failed to increment attempts: %w", err)
	}
	return nil
}
