package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"mia-website/backend/internal/pkg/metrics"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrUserQuotaNotFound is returned by GetSnapshot when there is no
// user_quotas row for the requested user. Migration 021 + the
// auto-create trigger on public.users make this an anomaly in
// production; callers should slog.Error and refuse to enforce.
var ErrUserQuotaNotFound = errors.New("user quota row not found")

// UserQuotaSnapshot is the single-query Status payload used by
// QuotaService.Check. Joins user_quotas + quota_cycle_for() + the
// (possibly absent) active usage_quota_periods row in one round trip.
// UsedUSD is 0 when no period row exists for the current cycle yet
// (= no usage recorded since the last cycle reset).
type UserQuotaSnapshot struct {
	UserID                uuid.UUID
	Tier                  string
	MonthlyUSDCapOverride *float64
	CycleAnchorDay        int16
	CycleStartAt          time.Time
	CycleEndAt            time.Time
	UsedUSD               float64
	Notes                 *string // admin-only POC anlaşma konteksti
}

// AdminQuotaUpdate is the PATCH payload accepted by the admin endpoint.
// Pointer fields use a tri-state contract:
//
//   - nil          → leave the column untouched
//   - non-nil ptr  → apply value
//
// Override has an explicit ClearOverride flag so the caller can express
// "drop the override entirely" without a magic sentinel (NaN, -1, …).
// Actor + Reason are mandatory for the audit row.
type AdminQuotaUpdate struct {
	Tier          *string
	Override      *float64
	ClearOverride bool
	Notes         *string
	Actor         string
	Reason        string
}

// UserQuotaRepository owns the persistence contract for the quota system.
// Two write paths exist intentionally:
//
//   - RecordUsageAtomic does the hot-path INSERT(usage_logs) +
//     UPSERT(usage_quota_periods) in a single statement; called once per
//     successful API operation via UsageRecorder.
//   - UpdateTier / SetOverride are admin paths (Faz 5) and also write a
//     user_quota_events audit row in the same tx.
type UserQuotaRepository interface {
	// GetSnapshot returns tier + active cycle window + used_usd in one
	// query. ErrUserQuotaNotFound when no user_quotas row exists.
	GetSnapshot(ctx context.Context, userID uuid.UUID, at time.Time) (UserQuotaSnapshot, error)

	// RecordUsageAtomic inserts the usage log AND bumps the counter for
	// the user's current cycle, in a single SQL statement. If the
	// user_quotas row is missing (anomaly — see ErrUserQuotaNotFound)
	// the usage log is still recorded but the counter UPSERT is a
	// no-op; the daily drift reconciliation (Faz 5) backfills the
	// counter from usage_logs anyway.
	RecordUsageAtomic(ctx context.Context, log UsageLog) error

	// ApplyAdminUpdate atomically mutates the user_quotas row AND writes
	// a user_quota_events audit row in one transaction. Per-field nil
	// pointers mean "no change". Returns the post-update snapshot so the
	// admin UI can render the new state without a follow-up read.
	ApplyAdminUpdate(ctx context.Context, userID uuid.UUID, update AdminQuotaUpdate) (UserQuotaSnapshot, error)
}

type postgresUserQuotaRepository struct{}

func NewUserQuotaRepository() UserQuotaRepository {
	return &postgresUserQuotaRepository{}
}

func (r *postgresUserQuotaRepository) GetSnapshot(ctx context.Context, userID uuid.UUID, at time.Time) (UserQuotaSnapshot, error) {
	const query = `
		SELECT
			uq.user_id,
			uq.tier,
			uq.monthly_usd_cap_override,
			uq.cycle_anchor_day,
			uq.notes,
			qc.cycle_start,
			qc.cycle_end,
			COALESCE(p.used_usd, 0)::DOUBLE PRECISION AS used_usd
		FROM public.user_quotas uq
		CROSS JOIN LATERAL public.quota_cycle_for(uq.cycle_anchor_day, $2::TIMESTAMPTZ) qc
		LEFT JOIN public.usage_quota_periods p
			ON p.user_id = uq.user_id
		   AND p.cycle_start_at = qc.cycle_start
		WHERE uq.user_id = $1
	`
	var s UserQuotaSnapshot
	err := DB.QueryRow(ctx, query, userID, at).Scan(
		&s.UserID,
		&s.Tier,
		&s.MonthlyUSDCapOverride,
		&s.CycleAnchorDay,
		&s.Notes,
		&s.CycleStartAt,
		&s.CycleEndAt,
		&s.UsedUSD,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserQuotaSnapshot{}, ErrUserQuotaNotFound
	}
	if err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("get user quota snapshot: %w", err)
	}
	return s, nil
}

func (r *postgresUserQuotaRepository) ApplyAdminUpdate(ctx context.Context, userID uuid.UUID, update AdminQuotaUpdate) (UserQuotaSnapshot, error) {
	if update.Actor == "" {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: actor is required")
	}

	tx, err := DB.Begin(ctx)
	if err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the row + capture the old state for the audit JSON in one
	// round trip. FOR UPDATE blocks concurrent admin patches against
	// the same user; the snapshot is also the source of truth for
	// "before" in user_quota_events.
	var (
		curTier     string
		curOverride *float64
		curAnchor   int16
		curNotes    *string
	)
	err = tx.QueryRow(ctx, `
		SELECT tier, monthly_usd_cap_override, cycle_anchor_day, notes
		FROM public.user_quotas
		WHERE user_id = $1
		FOR UPDATE
	`, userID).Scan(&curTier, &curOverride, &curAnchor, &curNotes)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserQuotaSnapshot{}, ErrUserQuotaNotFound
	}
	if err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: lock row: %w", err)
	}

	newTier := curTier
	if update.Tier != nil {
		newTier = *update.Tier
	}
	newOverride := curOverride
	if update.ClearOverride {
		newOverride = nil
	} else if update.Override != nil {
		v := *update.Override
		newOverride = &v
	}
	newNotes := curNotes
	if update.Notes != nil {
		v := *update.Notes
		newNotes = &v
	}

	// updated_at auto-bumps via the BEFORE UPDATE trigger created in
	// migration 021, so we don't set it here.
	if _, err := tx.Exec(ctx, `
		UPDATE public.user_quotas
		SET tier = $2,
		    monthly_usd_cap_override = $3,
		    notes = $4
		WHERE user_id = $1
	`, userID, newTier, newOverride, newNotes); err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: write user_quotas: %w", err)
	}

	oldSnap, err := json.Marshal(map[string]any{
		"tier":                     curTier,
		"monthly_usd_cap_override": curOverride,
		"cycle_anchor_day":         curAnchor,
		"notes":                    curNotes,
	})
	if err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: marshal old snapshot: %w", err)
	}
	newSnap, err := json.Marshal(map[string]any{
		"tier":                     newTier,
		"monthly_usd_cap_override": newOverride,
		"cycle_anchor_day":         curAnchor,
		"notes":                    newNotes,
	})
	if err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: marshal new snapshot: %w", err)
	}

	reason := update.Reason
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.user_quota_events
			(user_id, actor, old_snapshot, new_snapshot, reason)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
	`, userID, update.Actor, oldSnap, newSnap, reason); err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: write audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return UserQuotaSnapshot{}, fmt.Errorf("admin update: commit: %w", err)
	}

	// Re-read the snapshot via the canonical GetSnapshot path so the
	// returned UsedUSD + cycle window reflect any cycle rollover that
	// happened between calls.
	return r.GetSnapshot(ctx, userID, time.Now())
}

func (r *postgresUserQuotaRepository) RecordUsageAtomic(ctx context.Context, log UsageLog) error {
	if log.UserID == uuid.Nil {
		return fmt.Errorf("usage log user_id is required")
	}

	metadataJSON, err := json.Marshal(log.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	// Single statement, single transaction (via pgx auto-tx for
	// statements). Data-modifying CTE inserts the usage log;
	// the trailing INSERT bumps the counter joined against
	// quota_cycle_for() for the user's anchor day.
	//
	// RETURNING (xmax = 0) AS was_insert distinguishes "brand-new
	// cycle row" (INSERT path) from "increment to existing row"
	// (UPDATE path) so we can bump quota_cycle_resets_total per
	// rollover. We also pull the user's tier so the metric label
	// can stay correct without a second round trip.
	const query = `
		WITH inserted AS (
			INSERT INTO public.usage_logs (
				user_id, team_id, meeting_id, feature, provider, model,
				input_tokens, output_tokens, audio_seconds, cost_usd, metadata
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
			)
			RETURNING user_id, cost_usd
		),
		cyc AS (
			SELECT uq.user_id, uq.tier, qc.cycle_start, qc.cycle_end
			FROM public.user_quotas uq
			CROSS JOIN LATERAL public.quota_cycle_for(uq.cycle_anchor_day, now()) qc
			WHERE uq.user_id = $1
		)
		INSERT INTO public.usage_quota_periods AS p
			(user_id, cycle_start_at, cycle_end_at, used_usd, last_event_at)
		SELECT inserted.user_id, cyc.cycle_start, cyc.cycle_end,
		       inserted.cost_usd, now()
		FROM inserted
		JOIN cyc ON cyc.user_id = inserted.user_id
		ON CONFLICT (user_id, cycle_start_at) DO UPDATE SET
			used_usd      = p.used_usd + EXCLUDED.used_usd,
			last_event_at = now()
		RETURNING (xmax = 0) AS was_insert,
		          (SELECT tier FROM cyc) AS tier;
	`
	var (
		wasInsert bool
		tier      string
	)
	err = DB.QueryRow(ctx, query,
		log.UserID, log.TeamID, log.MeetingID, log.Feature, log.Provider, log.Model,
		log.InputTokens, log.OutputTokens, log.AudioSeconds, log.CostUSD, metadataJSON,
	).Scan(&wasInsert, &tier)
	if err != nil {
		// pgx.ErrNoRows: the user has no user_quotas row → trigger +
		// backfill should make this impossible. The usage_logs INSERT
		// inside the CTE still landed (data-modifying CTEs always run);
		// we just don't have a counter row to bump. Daily drift cron
		// will paper over this, but we slog.Warn so the anomaly is
		// observable instead of silently swallowed — if this fires
		// outside of tests it indicates either (a) a user was created
		// without the trigger firing, or (b) someone DELETEd the
		// user_quotas row manually.
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("RecordUsageAtomic: user_quotas row missing — usage logged but counter skipped",
				"user_id", log.UserID,
				"feature", log.Feature,
				"cost_usd", log.CostUSD)
			return nil
		}
		return fmt.Errorf("record usage atomic: %w", err)
	}
	if wasInsert {
		metrics.QuotaCycleResets.WithLabelValues(tier).Inc()
	}
	return nil
}
