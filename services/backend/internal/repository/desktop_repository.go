package repository

import (
	"context"
	"fmt"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// DesktopRepository encapsulates the two Phase 6 tables:
//   - desktop_sessions (WebSocket connection lifecycle)
//   - desktop_command_log (idempotent backend->desktop command record)
//
// The interface is intentionally narrow so the WebSocket handler and the
// DesktopMeetingScheduler can each depend on it without pulling in other
// repos.
type DesktopRepository interface {
	// CreateSession inserts a row when the hub accepts a handshake. The
	// returned DesktopSession has ID + ConnectedAt populated for the hub to
	// echo back in the "connected" message.
	CreateSession(ctx context.Context, userID uuid.UUID, clientVersion string) (*models.DesktopSession, error)

	// UpdateHeartbeat bumps last_heartbeat_at on every client ping. Missing
	// rows are a no-op (returns nil) — a session may have been closed
	// concurrently from another goroutine.
	UpdateHeartbeat(ctx context.Context, sessionID uuid.UUID) error

	// CloseSession stamps disconnected_at = now() for the given session.
	// Idempotent: calling twice is fine.
	CloseSession(ctx context.Context, sessionID uuid.UUID) error

	// FindStaleSessions returns open sessions (disconnected_at IS NULL) whose
	// last_heartbeat_at is older than olderThan. Used by the hub watchdog and
	// by a future cleanup job.
	FindStaleSessions(ctx context.Context, olderThan time.Time) ([]models.DesktopSession, error)

	// InsertCommandIfMissing writes a new row in desktop_command_log; the
	// UNIQUE (meeting_id, command_type) constraint short-circuits duplicates.
	// inserted=true means this call wrote the row; inserted=false means a
	// previous tick already did (scheduler must skip the actual send).
	InsertCommandIfMissing(
		ctx context.Context,
		meetingID, userID uuid.UUID,
		commandType string,
	) (inserted bool, err error)

	// MarkCommandAck records the client's response. Scoped by user_id so a
	// malicious client cannot mark someone else's command accepted by
	// spoofing a meeting_id (Phase 6 fix 2026-04-20). Safe to call with
	// status = "no_ack" after a timeout.
	MarkCommandAck(
		ctx context.Context,
		userID, meetingID uuid.UUID,
		commandType, status string,
	) error
}

// Valid desktop command + ack status values. Extracted so the handler can
// validate inputs defensively before persisting (and so the DB CHECK
// constraints + Go validation stay in sync).
var (
	ValidDesktopCommandTypes = map[string]struct{}{
		"start_recording": {},
		"stop_recording":  {},
		"force_stop":      {},
	}
	ValidDesktopAckStatuses = map[string]struct{}{
		"accepted": {},
		"declined": {},
		"no_ack":   {},
		// Phase 2.4 R9b — recorder extension prompt ack. The user
		// clicked "Devam ettir" on a force_stop and recording
		// continues for another 30 minutes. Distinct from `accepted`
		// so command_log distinguishes a normal Stop from an
		// extension event. DB CHECK widened in migration 012.
		"extended": {},
	}
)

// IsValidDesktopCommandType / IsValidDesktopAckStatus — the handler uses
// these before calling MarkCommandAck so unknown enum values never reach
// the DB even when CHECK constraints are disabled (e.g. managed-database
// surface where checks aren't yet migrated).
func IsValidDesktopCommandType(s string) bool {
	_, ok := ValidDesktopCommandTypes[s]
	return ok
}
func IsValidDesktopAckStatus(s string) bool {
	_, ok := ValidDesktopAckStatuses[s]
	return ok
}

// postgresDesktopRepository is the production implementation backed by the
// global DB pool (repository.DB). Tests don't hit this — they exercise the
// interface via a fake in services/.
type postgresDesktopRepository struct{}

// NewDesktopRepository returns the default pgx-backed implementation.
func NewDesktopRepository() DesktopRepository {
	return &postgresDesktopRepository{}
}

func (r *postgresDesktopRepository) CreateSession(
	ctx context.Context,
	userID uuid.UUID,
	clientVersion string,
) (*models.DesktopSession, error) {
	// clientVersion is nullable in DB; avoid writing empty strings so we
	// don't end up with "" rows which are harder to query on.
	var versionArg interface{}
	if clientVersion == "" {
		versionArg = nil
	} else {
		versionArg = clientVersion
	}

	row := DB.QueryRow(ctx, `
		INSERT INTO desktop_sessions (user_id, client_version)
		VALUES ($1, $2)
		RETURNING id, user_id, connected_at, last_heartbeat_at, disconnected_at, client_version
	`, userID, versionArg)

	var s models.DesktopSession
	if err := row.Scan(
		&s.ID,
		&s.UserID,
		&s.ConnectedAt,
		&s.LastHeartbeatAt,
		&s.DisconnectedAt,
		&s.ClientVersion,
	); err != nil {
		return nil, fmt.Errorf("desktop_sessions insert: %w", err)
	}
	return &s, nil
}

func (r *postgresDesktopRepository) UpdateHeartbeat(ctx context.Context, sessionID uuid.UUID) error {
	_, err := DB.Exec(ctx, `
		UPDATE desktop_sessions
		   SET last_heartbeat_at = now()
		 WHERE id = $1
		   AND disconnected_at IS NULL
	`, sessionID)
	if err != nil {
		return fmt.Errorf("desktop_sessions heartbeat: %w", err)
	}
	return nil
}

func (r *postgresDesktopRepository) CloseSession(ctx context.Context, sessionID uuid.UUID) error {
	_, err := DB.Exec(ctx, `
		UPDATE desktop_sessions
		   SET disconnected_at = now()
		 WHERE id = $1
		   AND disconnected_at IS NULL
	`, sessionID)
	if err != nil {
		return fmt.Errorf("desktop_sessions close: %w", err)
	}
	return nil
}

func (r *postgresDesktopRepository) FindStaleSessions(
	ctx context.Context,
	olderThan time.Time,
) ([]models.DesktopSession, error) {
	rows, err := DB.Query(ctx, `
		SELECT id, user_id, connected_at, last_heartbeat_at, disconnected_at, client_version
		  FROM desktop_sessions
		 WHERE disconnected_at IS NULL
		   AND last_heartbeat_at < $1
	`, olderThan)
	if err != nil {
		return nil, fmt.Errorf("desktop_sessions stale query: %w", err)
	}
	defer rows.Close()

	var out []models.DesktopSession
	for rows.Next() {
		var s models.DesktopSession
		if err := rows.Scan(
			&s.ID,
			&s.UserID,
			&s.ConnectedAt,
			&s.LastHeartbeatAt,
			&s.DisconnectedAt,
			&s.ClientVersion,
		); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *postgresDesktopRepository) InsertCommandIfMissing(
	ctx context.Context,
	meetingID, userID uuid.UUID,
	commandType string,
) (bool, error) {
	// ON CONFLICT DO NOTHING — the UNIQUE (meeting_id, command_type) index
	// guarantees we only ever get one row per (meeting, command). Count the
	// affected rows to tell the caller whether they actually inserted.
	tag, err := DB.Exec(ctx, `
		INSERT INTO desktop_command_log (meeting_id, user_id, command_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (meeting_id, command_type) DO NOTHING
	`, meetingID, userID, commandType)
	if err != nil {
		return false, fmt.Errorf("desktop_command_log insert: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *postgresDesktopRepository) MarkCommandAck(
	ctx context.Context,
	userID, meetingID uuid.UUID,
	commandType, status string,
) error {
	// user_id filter (Phase 6 fix 2026-04-20) — prevents one session from
	// ack-ing another user's command by knowing only the meeting_id. The
	// UPDATE is a no-op on mismatch, which we surface as nil since the
	// handler already logged the validated inputs.
	_, err := DB.Exec(ctx, `
		UPDATE desktop_command_log
		   SET client_ack_status = $4,
		       client_ack_at     = now()
		 WHERE meeting_id = $1
		   AND user_id    = $2
		   AND command_type = $3
	`, meetingID, userID, commandType, status)
	if err != nil {
		return fmt.Errorf("desktop_command_log ack: %w", err)
	}
	return nil
}
