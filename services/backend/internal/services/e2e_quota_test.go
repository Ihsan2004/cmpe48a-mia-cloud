//go:build e2e
// +build e2e

// E2E suite for the quota soft-cap pipeline (issue #39 Faz 6).
// Run with `go test -tags=e2e ./internal/services/...`.
//
// Exercises the realistic HTTP path: AuthMiddleware sets userID →
// QuotaAbuseLimiter → RequireQuota → handler. The middleware talks to
// the real QuotaService which talks to an in-memory fake repo —
// usage_logs + counter live as a single map keyed by (user, cycle).
// No Postgres, no network.
//
// Coverage matches docs/quota-limits-roadmap.md → Test Stratejisi → E2E:
//   - Free user spends $2 incrementally; the request that pushes past
//     the cap returns 429 with the expected JSON body.
//   - POC user gets the bigger cap.
//   - Internal tier is unlimited.
//   - Admin PATCH bumps a free user's override to $5 — the previously-
//     blocked user can call the gated endpoint again.

// External test package: handlers imports services, so the e2e test
// (which needs handlers) lives in services_test to break the cycle.
package services_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"mia-website/backend/internal/handlers"
	"mia-website/backend/internal/middleware"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// e2eQuotaRepo is the in-memory UserQuotaRepository the E2E suite uses
// in place of Postgres. State is keyed by user so admin patches +
// usage records can mutate the same row a follow-up Check reads.
type e2eQuotaRepo struct {
	mu       sync.Mutex
	users    map[uuid.UUID]*e2eQuotaUser
	cycleEnd time.Time
}

type e2eQuotaUser struct {
	tier     string
	override *float64
	used     float64
	notes    *string
}

func newE2EQuotaRepo() *e2eQuotaRepo {
	return &e2eQuotaRepo{
		users:    map[uuid.UUID]*e2eQuotaUser{},
		cycleEnd: time.Now().Add(15 * 24 * time.Hour).UTC().Truncate(time.Second),
	}
}

func (r *e2eQuotaRepo) seed(userID uuid.UUID, tier string, override *float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[userID] = &e2eQuotaUser{tier: tier, override: override}
}

func (r *e2eQuotaRepo) GetSnapshot(_ context.Context, userID uuid.UUID, _ time.Time) (repository.UserQuotaSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[userID]
	if !ok {
		return repository.UserQuotaSnapshot{}, repository.ErrUserQuotaNotFound
	}
	return repository.UserQuotaSnapshot{
		UserID:                userID,
		Tier:                  u.tier,
		MonthlyUSDCapOverride: u.override,
		CycleAnchorDay:        17,
		Notes:                 u.notes,
		CycleStartAt:          r.cycleEnd.AddDate(0, -1, 0),
		CycleEndAt:            r.cycleEnd,
		UsedUSD:               u.used,
	}, nil
}

func (r *e2eQuotaRepo) RecordUsageAtomic(_ context.Context, log repository.UsageLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[log.UserID]
	if !ok {
		// Match production: missing row → swallow.
		return nil
	}
	u.used += log.CostUSD
	return nil
}

func (r *e2eQuotaRepo) ApplyAdminUpdate(_ context.Context, userID uuid.UUID, upd repository.AdminQuotaUpdate) (repository.UserQuotaSnapshot, error) {
	r.mu.Lock()
	u, ok := r.users[userID]
	if !ok {
		r.mu.Unlock()
		return repository.UserQuotaSnapshot{}, repository.ErrUserQuotaNotFound
	}
	if upd.Tier != nil {
		u.tier = *upd.Tier
	}
	if upd.ClearOverride {
		u.override = nil
	} else if upd.Override != nil {
		v := *upd.Override
		u.override = &v
	}
	if upd.Notes != nil {
		v := *upd.Notes
		u.notes = &v
	}
	r.mu.Unlock()
	return r.GetSnapshot(context.Background(), userID, time.Now())
}

// e2eApp wires the same middleware chain production uses. The handler
// behind the gate records a usage row each time the user calls it,
// simulating a cost-bearing endpoint (bot start, extract, brief
// regenerate, ...).
type e2eApp struct {
	app     *fiber.App
	repo    *e2eQuotaRepo
	svc     quota.Service
	costPer float64
}

func newE2EApp(t *testing.T, adminKey string, costPer float64) *e2eApp {
	t.Helper()
	repo := newE2EQuotaRepo()
	svc := quota.NewService(repo)

	app := fiber.New()
	// Stand-in for AuthMiddleware: pull userID from a custom header so
	// tests can swap identities without minting JWTs.
	app.Use(func(c *fiber.Ctx) error {
		if uid := c.Get("X-Test-UserID"); uid != "" {
			c.Locals("userID", uid)
		}
		return c.Next()
	})

	requireQuota := middleware.RequireQuota(svc)

	// Cost-bearing endpoint. Per production wire-up (UsageRecorder)
	// the handler records usage AFTER the work succeeds.
	app.Post("/api/cost-action", requireQuota, func(c *fiber.Ctx) error {
		uidStr := c.Locals("userID").(string)
		uid, _ := uuid.Parse(uidStr)
		_ = svc.RecordUsage(c.Context(), repository.UsageLog{
			UserID:  uid,
			Feature: "e2e",
			CostUSD: costPer,
		})
		return c.JSON(fiber.Map{"ok": true})
	})

	// Admin routes — same handler ops will hit in production.
	adminH := handlers.NewAdminUserQuotaHandler(repo, svc, adminKey)
	app.Get("/api/admin/users/:id/quota", adminH.Get)
	app.Patch("/api/admin/users/:id/quota", adminH.Patch)

	return &e2eApp{app: app, repo: repo, svc: svc, costPer: costPer}
}

func (e *e2eApp) costRequest(t *testing.T, userID uuid.UUID) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/cost-action", nil)
	req.Header.Set("X-Test-UserID", userID.String())
	resp, err := e.app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return resp.StatusCode, out
}

func (e *e2eApp) adminPatch(t *testing.T, key string, userID uuid.UUID, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/admin/users/"+userID.String()+"/quota", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func TestE2E_FreeUser_BlocksAtCap(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	e := newE2EApp(t, "admin-key", 0.50) // each call costs $0.50 → 4 to fill the $2 cap
	user := uuid.New()
	e.repo.seed(user, quota.TierFree, nil)

	// 4 calls land cleanly (used 0 → 0.50 → 1.00 → 1.50 → 2.00).
	// Check runs BEFORE the handler records, so on call #4 used is
	// still $1.50 at gate time and the handler proceeds.
	for i := 1; i <= 4; i++ {
		code, body := e.costRequest(t, user)
		if code != fiber.StatusOK {
			t.Fatalf("call #%d should pass at the gate, got %d body=%v", i, code, body)
		}
	}
	// 5th call: gate sees $2.00 used >= $2.00 cap → 429.
	code, body := e.costRequest(t, user)
	if code != fiber.StatusTooManyRequests {
		t.Fatalf("5th call should 429 (cap reached), got %d body=%v", code, body)
	}
	if body["error"] != "quota_exceeded" {
		t.Errorf("body.error: got %v want quota_exceeded", body["error"])
	}
	if body["tier"] != "free" {
		t.Errorf("body.tier: got %v want free", body["tier"])
	}
}

func TestE2E_POCUser_BiggerCap(t *testing.T) {
	t.Setenv("QUOTA_POC_USD", "")
	quota.ResetForTest()

	e := newE2EApp(t, "admin-key", 5.0) // $5 each → 4 calls = $20 cap
	user := uuid.New()
	e.repo.seed(user, quota.TierPOC, nil)

	for i := 1; i <= 4; i++ {
		code, body := e.costRequest(t, user)
		if code != fiber.StatusOK {
			t.Fatalf("POC call #%d should pass, got %d body=%v", i, code, body)
		}
	}
	code, _ := e.costRequest(t, user)
	if code != fiber.StatusTooManyRequests {
		t.Fatalf("POC 5th call should 429 ($20 cap), got %d", code)
	}
}

func TestE2E_InternalUser_NeverBlocked(t *testing.T) {
	t.Setenv("QUOTA_INTERNAL_USD", "")
	quota.ResetForTest()

	e := newE2EApp(t, "admin-key", 100.0)
	user := uuid.New()
	e.repo.seed(user, quota.TierInternal, nil)

	// 10 calls, $1000 cumulative — internal tier never 429s.
	for i := 1; i <= 10; i++ {
		code, body := e.costRequest(t, user)
		if code != fiber.StatusOK {
			t.Fatalf("internal call #%d should pass, got %d body=%v", i, code, body)
		}
	}
}

func TestE2E_AdminOverrideUnblocksUser(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	e := newE2EApp(t, "admin-key", 0.50)
	user := uuid.New()
	e.repo.seed(user, quota.TierFree, nil)

	// Burn through the $2 cap.
	for i := 0; i < 4; i++ {
		e.costRequest(t, user)
	}
	// Confirm blocked.
	code, _ := e.costRequest(t, user)
	if code != fiber.StatusTooManyRequests {
		t.Fatalf("setup: user should be blocked after 4 calls, got %d", code)
	}

	// Admin bumps the override to $5.
	patchCode, patchBody := e.adminPatch(t, "admin-key", user,
		`{"monthly_usd_cap_override": 5.0, "reason":"e2e unblock"}`)
	if patchCode != fiber.StatusOK {
		t.Fatalf("admin PATCH should 200, got %d body=%v", patchCode, patchBody)
	}

	// Same user retries — gate now sees $2.00 used < $5.00 cap → passes.
	code, body := e.costRequest(t, user)
	if code != fiber.StatusOK {
		t.Fatalf("override should unblock; got %d body=%v", code, body)
	}
}

func TestE2E_AdminClearOverride_RestoresPlanDefault(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	e := newE2EApp(t, "admin-key", 0.50)
	user := uuid.New()
	o := 10.0
	e.repo.seed(user, quota.TierFree, &o)

	// Burn $2 — well under the $10 override.
	for i := 0; i < 4; i++ {
		if code, _ := e.costRequest(t, user); code != fiber.StatusOK {
			t.Fatalf("expected pass under $10 override, got %d", code)
		}
	}

	// Drop the override. Now the user is back on the $2 free default
	// AND they've already used $2 → next call should 429.
	patchCode, _ := e.adminPatch(t, "admin-key", user,
		`{"clear_override": true, "reason":"e2e clear"}`)
	if patchCode != fiber.StatusOK {
		t.Fatalf("clear-override PATCH should 200, got %d", patchCode)
	}

	code, body := e.costRequest(t, user)
	if code != fiber.StatusTooManyRequests {
		t.Fatalf("dropping override should re-block the user, got %d body=%v", code, body)
	}
}
