package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// fakeUserQuotaRepo is the test seam for AdminUserQuotaHandler. Embeds
// the interface so the handler test only needs to override the two
// methods it exercises.
type fakeUserQuotaRepo struct {
	repository.UserQuotaRepository
	snapshot repository.UserQuotaSnapshot
	getErr   error
	applied  *repository.AdminQuotaUpdate
	updated  repository.UserQuotaSnapshot
	applyErr error
}

func (r *fakeUserQuotaRepo) GetSnapshot(_ context.Context, userID uuid.UUID, _ time.Time) (repository.UserQuotaSnapshot, error) {
	if r.getErr != nil {
		return repository.UserQuotaSnapshot{}, r.getErr
	}
	s := r.snapshot
	s.UserID = userID
	return s, nil
}

func (r *fakeUserQuotaRepo) ApplyAdminUpdate(_ context.Context, userID uuid.UUID, upd repository.AdminQuotaUpdate) (repository.UserQuotaSnapshot, error) {
	r.applied = &upd
	if r.applyErr != nil {
		return repository.UserQuotaSnapshot{}, r.applyErr
	}
	s := r.updated
	s.UserID = userID
	return s, nil
}

func newAdminApp(repo *fakeUserQuotaRepo, key string) *fiber.App {
	app := fiber.New()
	svc := quota.NewService(repo)
	h := NewAdminUserQuotaHandler(repo, svc, key)
	app.Get("/api/admin/users/:id/quota", h.Get)
	app.Patch("/api/admin/users/:id/quota", h.Patch)
	return app
}

func doReq(t *testing.T, app *fiber.App, method, path, auth string, body []byte) (*fiber.Ctx, int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return nil, resp.StatusCode, out
}

func TestAdminUserQuota_GetUnauthorizedWithoutKey(t *testing.T) {
	repo := &fakeUserQuotaRepo{}
	app := newAdminApp(repo, "secret-key")
	uid := uuid.New().String()

	_, code, body := doReq(t, app, "GET", "/api/admin/users/"+uid+"/quota", "", nil)
	if code != fiber.StatusUnauthorized {
		t.Errorf("expected 401, got %d body=%v", code, body)
	}
}

func TestAdminUserQuota_GetUnauthorizedWithWrongKey(t *testing.T) {
	repo := &fakeUserQuotaRepo{}
	app := newAdminApp(repo, "secret-key")
	uid := uuid.New().String()

	_, code, _ := doReq(t, app, "GET", "/api/admin/users/"+uid+"/quota", "wrong-key", nil)
	if code != fiber.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", code)
	}
}

func TestAdminUserQuota_GetReturnsDollarPayload(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	cycleStart := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	cycleEnd := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	notes := "Acme POC"
	repo := &fakeUserQuotaRepo{
		snapshot: repository.UserQuotaSnapshot{
			Tier:           quota.TierFree,
			UsedUSD:        1.0, // 50% of $2 free cap
			CycleStartAt:   cycleStart,
			CycleEndAt:     cycleEnd,
			CycleAnchorDay: 17,
			Notes:          &notes,
		},
	}
	app := newAdminApp(repo, "secret-key")
	uid := uuid.New().String()

	_, code, body := doReq(t, app, "GET", "/api/admin/users/"+uid+"/quota", "secret-key", nil)
	if code != fiber.StatusOK {
		t.Fatalf("expected 200, got %d body=%v", code, body)
	}
	if body["tier"] != "free" {
		t.Errorf("tier: got %v want free", body["tier"])
	}
	// Admin view DOES expose dollars (Faz 5 explicit decision).
	if cap, _ := body["cap_usd"].(float64); cap != 2.0 {
		t.Errorf("cap_usd: got %v want 2.0", body["cap_usd"])
	}
	if used, _ := body["used_usd"].(float64); used != 1.0 {
		t.Errorf("used_usd: got %v want 1.0", body["used_usd"])
	}
	if pct, _ := body["used_pct"].(float64); int(pct) != 50 {
		t.Errorf("used_pct: got %v want 50", body["used_pct"])
	}
	if body["notes"] != "Acme POC" {
		t.Errorf("notes: got %v want Acme POC", body["notes"])
	}
	if body["cycle_starts_at"] == nil || body["cycle_resets_at"] == nil {
		t.Errorf("cycle window must be present, got %v", body)
	}
}

func TestAdminUserQuota_GetUnlimitedTierReturnsNilCap(t *testing.T) {
	t.Setenv("QUOTA_INTERNAL_USD", "")
	quota.ResetForTest()

	repo := &fakeUserQuotaRepo{snapshot: repository.UserQuotaSnapshot{
		Tier: quota.TierInternal,
	}}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	_, code, body := doReq(t, app, "GET", "/api/admin/users/"+uid+"/quota", "k", nil)
	if code != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if body["cap_usd"] != nil {
		t.Errorf("internal tier cap should be null, got %v", body["cap_usd"])
	}
}

func TestAdminUserQuota_GetNotFound(t *testing.T) {
	repo := &fakeUserQuotaRepo{getErr: repository.ErrUserQuotaNotFound}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	_, code, _ := doReq(t, app, "GET", "/api/admin/users/"+uid+"/quota", "k", nil)
	if code != fiber.StatusNotFound {
		t.Errorf("expected 404, got %d", code)
	}
}

func TestAdminUserQuota_PatchAppliesTierAndOverride(t *testing.T) {
	t.Setenv("QUOTA_POC_USD", "")
	quota.ResetForTest()

	override := 50.0
	repo := &fakeUserQuotaRepo{updated: repository.UserQuotaSnapshot{
		Tier:                  quota.TierPOC,
		MonthlyUSDCapOverride: &override,
		UsedUSD:               5.0,
	}}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	body := []byte(`{"tier":"poc","monthly_usd_cap_override":50.0,"reason":"Acme POC ramp-up"}`)
	_, code, resp := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
	if code != fiber.StatusOK {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	if repo.applied == nil {
		t.Fatal("ApplyAdminUpdate was not invoked")
	}
	if repo.applied.Tier == nil || *repo.applied.Tier != quota.TierPOC {
		t.Errorf("Tier patch missing: %+v", repo.applied)
	}
	if repo.applied.Override == nil || *repo.applied.Override != 50.0 {
		t.Errorf("Override patch missing: %+v", repo.applied)
	}
	if repo.applied.Reason != "Acme POC ramp-up" {
		t.Errorf("Reason not threaded: %+v", repo.applied)
	}
	if repo.applied.Actor == "" {
		t.Errorf("Actor must be populated, got empty")
	}
	if resp["tier"] != "poc" {
		t.Errorf("response tier: got %v want poc", resp["tier"])
	}
}

func TestAdminUserQuota_PatchClearOverride(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	repo := &fakeUserQuotaRepo{updated: repository.UserQuotaSnapshot{Tier: quota.TierFree}}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	body := []byte(`{"clear_override":true}`)
	_, code, _ := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
	if code != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if !repo.applied.ClearOverride {
		t.Errorf("ClearOverride flag not threaded: %+v", repo.applied)
	}
	if repo.applied.Override != nil {
		t.Errorf("Override should be nil when ClearOverride=true, got %v", *repo.applied.Override)
	}
}

// Postel's law: REST-idiomatic JSON null on the override field must
// produce the same "drop the override" outcome as clear_override:true.
func TestAdminUserQuota_PatchExplicitNullClearsOverride(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	repo := &fakeUserQuotaRepo{updated: repository.UserQuotaSnapshot{Tier: quota.TierFree}}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	body := []byte(`{"monthly_usd_cap_override": null}`)
	_, code, _ := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
	if code != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if !repo.applied.ClearOverride {
		t.Errorf("explicit JSON null must map to ClearOverride=true, got %+v", repo.applied)
	}
	if repo.applied.Override != nil {
		t.Errorf("Override must be nil for null payload, got %v", *repo.applied.Override)
	}
}

// Field absent (PATCH semantics) must NOT trigger clear — only "I sent
// you a value" or "I sent you null" should rewrite the override.
func TestAdminUserQuota_PatchAbsentFieldLeavesOverrideAlone(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	quota.ResetForTest()

	repo := &fakeUserQuotaRepo{updated: repository.UserQuotaSnapshot{Tier: quota.TierFree}}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	// Only tier in the body — override should NOT be touched.
	body := []byte(`{"tier":"free"}`)
	_, code, _ := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
	if code != fiber.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if repo.applied.ClearOverride {
		t.Errorf("absent field must NOT trigger ClearOverride")
	}
	if repo.applied.Override != nil {
		t.Errorf("absent field must leave Override nil-as-untouched")
	}
}

func TestAdminUserQuota_PatchRejectsInvalidTier(t *testing.T) {
	repo := &fakeUserQuotaRepo{}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	body := []byte(`{"tier":"superuser"}`)
	_, code, resp := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
	if code != fiber.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%v", code, resp)
	}
	if repo.applied != nil {
		t.Errorf("repo must not be called when tier is invalid")
	}
}

// Postel: tier is accepted in any casing + whitespace, normalized to
// the canonical lowercase form before it reaches the repository.
func TestAdminUserQuota_PatchTierCaseInsensitive(t *testing.T) {
	repo := &fakeUserQuotaRepo{updated: repository.UserQuotaSnapshot{Tier: quota.TierPOC}}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	for _, raw := range []string{"POC", "Poc", " poc ", "pOc"} {
		repo.applied = nil
		body := []byte(`{"tier":"` + raw + `"}`)
		_, code, _ := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
		if code != fiber.StatusOK {
			t.Errorf("tier=%q should normalize and pass, got %d", raw, code)
			continue
		}
		if repo.applied == nil || repo.applied.Tier == nil || *repo.applied.Tier != quota.TierPOC {
			t.Errorf("tier=%q must normalize to canonical 'poc', got %+v", raw, repo.applied)
		}
	}
}

func TestAdminUserQuota_PatchRejectsNegativeOverride(t *testing.T) {
	repo := &fakeUserQuotaRepo{}
	app := newAdminApp(repo, "k")
	uid := uuid.New().String()

	body := []byte(`{"monthly_usd_cap_override":-1.0}`)
	_, code, _ := doReq(t, app, "PATCH", "/api/admin/users/"+uid+"/quota", "k", body)
	if code != fiber.StatusBadRequest {
		t.Errorf("expected 400 for negative override, got %d", code)
	}
}

func TestAdminUserQuota_PatchInvalidUserID(t *testing.T) {
	repo := &fakeUserQuotaRepo{}
	app := newAdminApp(repo, "k")
	body := []byte(`{"tier":"poc"}`)
	_, code, _ := doReq(t, app, "PATCH", "/api/admin/users/not-a-uuid/quota", "k", body)
	if code != fiber.StatusBadRequest {
		t.Errorf("expected 400 for non-uuid id, got %d", code)
	}
}

func TestAdminKeyLabel(t *testing.T) {
	// 6-hex SHA-256 fingerprint: deterministic, no plaintext leak.
	got := adminKeyLabel("abcdef1234")
	if len(got) != 6 {
		t.Errorf("fingerprint length: got %d want 6 (%q)", len(got), got)
	}
	if got != adminKeyLabel("abcdef1234") {
		t.Errorf("fingerprint must be deterministic")
	}
	if got == adminKeyLabel("different-key") {
		t.Errorf("different keys must produce different fingerprints")
	}
	// MUST NOT leak any of the plaintext key.
	if strings.Contains(got, "abcd") || strings.Contains(got, "abc") {
		t.Errorf("fingerprint leaked plaintext prefix: %q", got)
	}
	if adminKeyLabel("") != "anon" {
		t.Errorf("empty key should label as 'anon'")
	}
}
