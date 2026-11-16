package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Integration suite for issue #39 — exercises the actual SQL contracts
// of migration 021 against a real Postgres. Skipped unless
// INTEGRATION_TEST=true + DATABASE_URL are set (same gate as the rest
// of repository/*_integration_test.go).
//
// What's covered:
//
//   - GetSnapshot: returns the auto-created tier='free' row populated by
//     the migration-21 trigger; UsedUSD = 0 before any usage.
//   - RecordUsageAtomic convergence: 200 concurrent fake-cost writes
//     end up in usage_quota_periods.used_usd to the cent (allowing
//     floating-point tolerance).
//   - ApplyAdminUpdate: round-trips tier + override and writes a
//     user_quota_events audit row.
//
// Cycle rollover is exercised in the drift cron test (forces a fake
// "next month" anchor via direct UPDATE on cycle_anchor_day so we
// don't need a time machine).

func createUserQuotaTestUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	email := "quota_" + userID.String()[:8] + "@test.com"
	_, err := DB.Exec(ctx, `
		INSERT INTO public.users (id, email, password_hash, full_name, email_verified, is_active)
		VALUES ($1, $2, 'test_hash', 'Quota Test', true, true)
	`, userID, email)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// users trigger users_create_quota auto-INSERTs the user_quotas
	// row. ON DELETE CASCADE on user_quotas + usage_quota_periods +
	// user_quota_events FKs cleans those up when we delete the user.
	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", userID)
	})
	return userID
}

func TestIntegration_GetSnapshot_TriggerAutoCreatesRow(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createUserQuotaTestUser(t, ctx)

	repo := NewUserQuotaRepository()
	snap, err := repo.GetSnapshot(ctx, userID, time.Now())
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.Tier != "free" {
		t.Errorf("trigger default: got tier=%q want free", snap.Tier)
	}
	if snap.UsedUSD != 0 {
		t.Errorf("fresh user: UsedUSD=%v want 0", snap.UsedUSD)
	}
	if snap.CycleStartAt.IsZero() || snap.CycleEndAt.IsZero() {
		t.Errorf("cycle window should be populated by quota_cycle_for, got [%v, %v]",
			snap.CycleStartAt, snap.CycleEndAt)
	}
	if snap.CycleEndAt.Before(snap.CycleStartAt) {
		t.Errorf("cycle_end < cycle_start: [%v, %v]", snap.CycleStartAt, snap.CycleEndAt)
	}
}

func TestIntegration_RecordUsageAtomic_ConcurrentConvergence(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createUserQuotaTestUser(t, ctx)
	repo := NewUserQuotaRepository()

	const (
		writers       = 20
		perWriter     = 10
		costPerRecord = 0.01
	)
	expected := float64(writers*perWriter) * costPerRecord

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				err := repo.RecordUsageAtomic(ctx, UsageLog{
					UserID:   userID,
					Feature:  "integration_test",
					Provider: "fake",
					Model:    "fake-model",
					CostUSD:  costPerRecord,
				})
				if err != nil {
					t.Errorf("RecordUsageAtomic: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	snap, err := repo.GetSnapshot(ctx, userID, time.Now())
	if err != nil {
		t.Fatalf("GetSnapshot after writes: %v", err)
	}
	// NUMERIC(12,6) round-trips back as DOUBLE PRECISION; allow a
	// micro-cent of float noise.
	if diff := snap.UsedUSD - expected; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("converged used_usd: got %v want %v (diff %v)",
			snap.UsedUSD, expected, diff)
	}

	// usage_logs SUM must agree (the UPSERT writes counter; usage_logs
	// is the source of truth the drift cron reconciles against).
	var actual float64
	row := DB.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0)
		FROM public.usage_logs
		WHERE user_id = $1
		  AND created_at >= $2 AND created_at < $3
	`, userID, snap.CycleStartAt, snap.CycleEndAt)
	if err := row.Scan(&actual); err != nil {
		t.Fatalf("scan usage_logs SUM: %v", err)
	}
	if diff := actual - expected; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("usage_logs SUM: got %v want %v", actual, expected)
	}
}

func TestIntegration_ApplyAdminUpdate_PersistsAndAudits(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createUserQuotaTestUser(t, ctx)
	repo := NewUserQuotaRepository()

	tier := "poc"
	override := 25.0
	notes := "integration test override"
	snap, err := repo.ApplyAdminUpdate(ctx, userID, AdminQuotaUpdate{
		Tier:     &tier,
		Override: &override,
		Notes:    &notes,
		Actor:    "admin:test",
		Reason:   "integration smoke test",
	})
	if err != nil {
		t.Fatalf("ApplyAdminUpdate: %v", err)
	}
	if snap.Tier != "poc" {
		t.Errorf("tier persisted: got %q want poc", snap.Tier)
	}
	if snap.MonthlyUSDCapOverride == nil || *snap.MonthlyUSDCapOverride != 25.0 {
		t.Errorf("override persisted: got %v want 25.0", snap.MonthlyUSDCapOverride)
	}

	// Audit row should exist (1 from migration backfill is unlikely on
	// a freshly-created user, so we expect exactly 1 row from this PATCH).
	var auditCount int
	err = DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM public.user_quota_events
		WHERE user_id = $1 AND actor = 'admin:test'
	`, userID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit event count: got %d want 1", auditCount)
	}

	// Now drop the override via the ClearOverride path.
	snap, err = repo.ApplyAdminUpdate(ctx, userID, AdminQuotaUpdate{
		ClearOverride: true,
		Actor:         "admin:test",
		Reason:        "clear override",
	})
	if err != nil {
		t.Fatalf("ClearOverride: %v", err)
	}
	if snap.MonthlyUSDCapOverride != nil {
		t.Errorf("override should be nil after ClearOverride, got %v", *snap.MonthlyUSDCapOverride)
	}
	if snap.Tier != "poc" {
		t.Errorf("tier should stay poc when only clearing override, got %q", snap.Tier)
	}
}

func TestIntegration_RecordUsageAtomic_MissingUserQuotaRow(t *testing.T) {
	// Edge case: orphan user_id (no user_quotas row, no trigger). The
	// CTE's INSERT into usage_logs still lands; the counter UPSERT is
	// a no-op. Drift cron is expected to repair on next run.
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	// Direct INSERT bypassing the users table → no trigger fires.
	orphan := uuid.New()
	// We need a real user row though, because usage_logs.user_id has no
	// FK but RecordUsageAtomic's CTE references user_quotas.
	// Instead: create user normally, then DELETE only the user_quotas
	// row to simulate the anomaly.
	userID := createUserQuotaTestUser(t, ctx)
	_, err := DB.Exec(ctx, "DELETE FROM public.user_quotas WHERE user_id = $1", userID)
	if err != nil {
		t.Fatalf("delete user_quotas: %v", err)
	}
	_ = orphan

	repo := NewUserQuotaRepository()
	err = repo.RecordUsageAtomic(ctx, UsageLog{
		UserID:   userID,
		Feature:  "integration_test",
		Provider: "fake",
		Model:    "fake-model",
		CostUSD:  0.05,
	})
	if err != nil {
		t.Fatalf("RecordUsageAtomic should swallow missing-row, got: %v", err)
	}

	// usage_logs row must still have landed.
	var n int
	if err := DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM public.usage_logs WHERE user_id = $1", userID,
	).Scan(&n); err != nil {
		t.Fatalf("count usage_logs: %v", err)
	}
	if n != 1 {
		t.Errorf("usage_logs row count: got %d want 1 (CTE insert must persist even when counter UPSERT is no-op)", n)
	}
}
