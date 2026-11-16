package quota

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
)

// DriftReconciler runs the daily SUM(usage_logs.cost_usd) sanity check
// against usage_quota_periods.used_usd. Two divergence sources exist:
//
//   - The async usage_logs INSERT + counter UPSERT inside one statement
//     can fail (pg connection drop, statement timeout) after the
//     usage_logs side already landed via the data-modifying CTE.
//   - usage_logs.cost_usd recalculation drift (pricing override env var
//     flipped without restarting the counter-touching path).
//
// The cron runs once at startup as a catch-up and then on every interval
// tick (24h default) aligned to UTC midnight.
//
// The SQL mirrors docs/quota-limits-roadmap.md → Drift reconciliation;
// kept inline here so the cron's intent is reviewable in one place.
//
// `quota_drift_total{direction}` is bumped by application code (not the
// SQL) so the metric stays decoupled from DB layer.
type DriftReconciler struct {
	db  driftDB
	now func() time.Time
}

// driftDB is the narrow pgx-like surface DriftReconciler needs. The
// production constructor wires repository.DB; tests can pass a fake.
type driftDB interface {
	Query(ctx context.Context, sql string, args ...any) (driftRows, error)
}

type driftRows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// driftRow is what a single reconciliation iteration emits. Either
// nothing changed (Direction == "") or we corrected drift.
type driftRow struct {
	UserID     string
	Tier       string
	Before     float64
	After      float64
	Direction  string  // "over" | "under" | ""
	Delta      float64 // abs(after - before)
}

// driftThresholdUSD is the absolute delta below which we treat the
// counter as authoritative and skip the UPSERT. Picked to ignore
// floating-point noise from NUMERIC↔DOUBLE PRECISION conversions; any
// real drift after a missed counter write will be far above this.
const driftThresholdUSD = 0.01

func NewDriftReconciler() *DriftReconciler {
	return &DriftReconciler{
		db:  pgxAdapter{},
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Reconcile runs the daily reconciliation pass. Returns the number of
// rows the cron rewrote (i.e. drift events). Bumps the
// quota_drift_total{direction} counter for each correction.
//
// The query joins user_quotas + LATERAL quota_cycle_for(now()) + the
// active usage_quota_periods row + the SUM over usage_logs. Anything
// with |before - after| > driftThresholdUSD is UPSERTed to the actual.
// The RETURNING clause feeds the metric labels back to application
// code.
func (r *DriftReconciler) Reconcile(ctx context.Context) (int, error) {
	const query = `
		WITH active_cycles AS (
			SELECT uq.user_id, uq.tier, qc.cycle_start, qc.cycle_end
			FROM public.user_quotas uq
			CROSS JOIN LATERAL public.quota_cycle_for(uq.cycle_anchor_day, $1::TIMESTAMPTZ) qc
		),
		actual AS (
			SELECT
				ac.user_id,
				ac.tier,
				ac.cycle_start,
				ac.cycle_end,
				COALESCE(SUM(ul.cost_usd), 0)::NUMERIC(12, 6) AS actual_used
			FROM active_cycles ac
			LEFT JOIN public.usage_logs ul
			       ON ul.user_id = ac.user_id
			      AND ul.created_at >= ac.cycle_start
			      AND ul.created_at <  ac.cycle_end
			GROUP BY ac.user_id, ac.tier, ac.cycle_start, ac.cycle_end
		),
		diff AS (
			SELECT
				a.user_id,
				a.tier,
				a.cycle_start,
				a.cycle_end,
				a.actual_used,
				COALESCE(p.used_usd, 0)::NUMERIC(12, 6) AS counter_used
			FROM actual a
			LEFT JOIN public.usage_quota_periods p
			       ON p.user_id        = a.user_id
			      AND p.cycle_start_at = a.cycle_start
			WHERE ABS(COALESCE(p.used_usd, 0) - a.actual_used) > $2::NUMERIC
		),
		upserted AS (
			INSERT INTO public.usage_quota_periods AS p
				(user_id, cycle_start_at, cycle_end_at, used_usd, last_event_at)
			SELECT user_id, cycle_start, cycle_end, actual_used, now()
			FROM diff
			ON CONFLICT (user_id, cycle_start_at) DO UPDATE SET
				cycle_end_at  = EXCLUDED.cycle_end_at,
				used_usd      = EXCLUDED.used_usd,
				last_event_at = now()
			RETURNING user_id, cycle_start_at, used_usd AS new_used
		)
		SELECT
			d.user_id::TEXT,
			d.tier,
			d.counter_used::DOUBLE PRECISION,
			d.actual_used::DOUBLE PRECISION
		FROM diff d
	`

	rows, err := r.db.Query(ctx, query, r.now(), driftThresholdUSD)
	if err != nil {
		return 0, fmt.Errorf("drift reconcile: query: %w", err)
	}
	defer rows.Close()

	corrected := 0
	for rows.Next() {
		var row driftRow
		if err := rows.Scan(&row.UserID, &row.Tier, &row.Before, &row.After); err != nil {
			return corrected, fmt.Errorf("drift reconcile: scan: %w", err)
		}
		direction := metrics.QuotaDriftUnder
		if row.Before > row.After {
			direction = metrics.QuotaDriftOver
		}
		row.Direction = direction
		row.Delta = absFloat(row.After - row.Before)
		metrics.QuotaDrift.WithLabelValues(direction).Inc()

		slog.Info("quota drift reconciled",
			"user_id", row.UserID,
			"tier", row.Tier,
			"counter_was", row.Before,
			"actual", row.After,
			"direction", direction,
			"delta_usd", row.Delta)
		corrected++
	}
	if err := rows.Err(); err != nil {
		return corrected, fmt.Errorf("drift reconcile: rows: %w", err)
	}
	return corrected, nil
}

// StartCron runs Reconcile immediately (safety-net) then once per
// `interval`, aligned to the next UTC midnight so daily reports land at
// a predictable wall-clock. Mirrors AudioCleanupService.StartCron so
// the operational playbook stays consistent.
func (r *DriftReconciler) StartCron(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	if n, err := r.Reconcile(ctx); err != nil {
		slog.Error("quota drift cron: initial run failed", "error", err)
	} else {
		slog.Info("quota drift cron: initial run", "corrected", n)
	}

	now := r.now()
	nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	delay := nextMidnight.Sub(now)
	slog.Info("quota drift cron: next aligned run", "at_utc", nextMidnight, "in", delay)

	firstTimer := time.NewTimer(delay)
	defer firstTimer.Stop()

	select {
	case <-ctx.Done():
		slog.Info("quota drift cron stopped")
		return
	case <-firstTimer.C:
		if n, err := r.Reconcile(ctx); err != nil {
			slog.Error("quota drift cron: aligned run failed", "error", err)
		} else if n > 0 {
			slog.Info("quota drift cron: aligned run", "corrected", n)
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("quota drift cron stopped")
			return
		case <-ticker.C:
			if n, err := r.Reconcile(ctx); err != nil {
				slog.Error("quota drift cron: tick run failed", "error", err)
			} else if n > 0 {
				slog.Info("quota drift cron: tick run", "corrected", n)
			}
		}
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// pgxAdapter wraps repository.DB so DriftReconciler holds onto a narrow
// interface instead of the global pool. Tests inject a fake driftDB.
type pgxAdapter struct{}

func (pgxAdapter) Query(ctx context.Context, sql string, args ...any) (driftRows, error) {
	rows, err := repository.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
