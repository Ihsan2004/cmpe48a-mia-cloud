package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// fakeQuota implements quota.Service for middleware tests. Each test
// pins the status it wants to observe; the call counter lets us assert
// the middleware actually invoked Check (vs short-circuiting on auth).
type fakeQuota struct {
	status     quota.Status
	checkErr   error
	checkCalls int
}

func (f *fakeQuota) Check(_ context.Context, _ uuid.UUID) (quota.Status, error) {
	f.checkCalls++
	if f.checkErr != nil {
		return quota.Status{}, f.checkErr
	}
	return f.status, nil
}

func (f *fakeQuota) RecordUsage(_ context.Context, _ repository.UsageLog) error {
	return nil
}

// withAuthLocals installs the userID local that AuthMiddleware would
// normally populate, so the quota middleware reads a valid identity.
func withAuthLocals(uid string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals("userID", uid)
		return c.Next()
	}
}

// downstream is the post-middleware handler. When invoked it sets a
// known status code/body so tests can prove the middleware passed
// through (allow) vs short-circuited (429 / 401).
func downstream(c *fiber.Ctx) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": true})
}

func newApp(uid string, svc quota.Service) *fiber.App {
	app := fiber.New()
	if uid != "" {
		app.Use(withAuthLocals(uid))
	}
	app.Use(RequireQuota(svc))
	app.Post("/protected", downstream)
	return app
}

func TestRequireQuota_AllowSetsHeadersAndContinues(t *testing.T) {
	uid := uuid.New().String()
	cycleEnd := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	fake := &fakeQuota{status: quota.Status{
		Tier:         quota.TierFree,
		UsedPct:      40,
		CycleStartAt: cycleEnd.AddDate(0, -1, 0),
		CycleEndAt:   cycleEnd,
	}}
	app := newApp(uid, fake)

	req := httptest.NewRequest("POST", "/protected", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "100" {
		t.Errorf("X-RateLimit-Limit: got %q want 100", got)
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "60" {
		t.Errorf("X-RateLimit-Remaining: got %q want 60 (100-40)", got)
	}
	if got := resp.Header.Get("X-RateLimit-Tier"); got != quota.TierFree {
		t.Errorf("X-RateLimit-Tier: got %q want free", got)
	}
	if got := resp.Header.Get("X-RateLimit-Reset"); got == "" {
		t.Errorf("X-RateLimit-Reset must be set on allow")
	} else if n, err := strconv.ParseInt(got, 10, 64); err != nil || n != cycleEnd.Unix() {
		t.Errorf("X-RateLimit-Reset: got %q want %d", got, cycleEnd.Unix())
	}
	if fake.checkCalls != 1 {
		t.Errorf("Check should run exactly once, got %d", fake.checkCalls)
	}
}

func TestRequireQuota_ExceededReturns429AndBlocks(t *testing.T) {
	uid := uuid.New().String()
	cycleEnd := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	fake := &fakeQuota{status: quota.Status{
		Tier:         quota.TierFree,
		UsedPct:      100,
		CycleStartAt: cycleEnd.AddDate(0, -1, 0),
		CycleEndAt:   cycleEnd,
		Exceeded:     true,
	}}
	app := newApp(uid, fake)

	req := httptest.NewRequest("POST", "/protected", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON body: %v (%s)", err, body)
	}
	if got["error"] != "quota_exceeded" {
		t.Errorf("error: got %v want quota_exceeded", got["error"])
	}
	if got["tier"] != quota.TierFree {
		t.Errorf("tier: got %v want free", got["tier"])
	}
	if pct, _ := got["used_pct"].(float64); int(pct) != 100 {
		t.Errorf("used_pct: got %v want 100", got["used_pct"])
	}
	if _, ok := got["cycle_resets_at"].(string); !ok {
		t.Errorf("cycle_resets_at must be a string, got %v", got["cycle_resets_at"])
	}
	if _, hasUsedUSD := got["used_usd"]; hasUsedUSD {
		t.Errorf("body must NOT leak used_usd (kullanıcı dolar görmez): %v", got)
	}
	if _, hasCap := got["cap_usd"]; hasCap {
		t.Errorf("body must NOT leak cap_usd (kullanıcı dolar görmez): %v", got)
	}

	if resp.Header.Get("Retry-After") == "" {
		t.Errorf("Retry-After header must be set on 429")
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining: got %q want 0", got)
	}
}

func TestRequireQuota_InternalTierAlwaysAllows(t *testing.T) {
	uid := uuid.New().String()
	fake := &fakeQuota{status: quota.Status{
		Tier:     quota.TierInternal,
		UsedPct:  0,
		CapUSD:   nil,
		Exceeded: false,
	}}
	app := newApp(uid, fake)

	req := httptest.NewRequest("POST", "/protected", nil)
	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("internal tier must always allow, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-RateLimit-Tier"); got != quota.TierInternal {
		t.Errorf("tier badge: got %q want internal", got)
	}
}

func TestRequireQuota_MissingUserIDReturns401(t *testing.T) {
	app := newApp("", &fakeQuota{}) // no auth locals injected
	req := httptest.NewRequest("POST", "/protected", nil)
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("expected 401 when userID missing, got %d", resp.StatusCode)
	}
}

func TestRequireQuota_InvalidUserIDReturns401(t *testing.T) {
	app := newApp("not-a-uuid", &fakeQuota{})
	req := httptest.NewRequest("POST", "/protected", nil)
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Errorf("expected 401 when userID is not a UUID, got %d", resp.StatusCode)
	}
}

func TestRequireQuota_FailOpenOnRepoError(t *testing.T) {
	// Any non-quota_exceeded error (DB blip, ErrUserQuotaNotFound) must
	// not block the user — soft cap is a comfort layer, infra failures
	// should fall through to the handler.
	uid := uuid.New().String()
	fake := &fakeQuota{checkErr: repository.ErrUserQuotaNotFound}
	app := newApp(uid, fake)

	req := httptest.NewRequest("POST", "/protected", nil)
	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("fail-open expected on quota error, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "" {
		t.Errorf("rate-limit headers must NOT be set on fail-open path, got Limit=%q", got)
	}
}

func TestRequireQuota_OptionsBypasses(t *testing.T) {
	// CORS preflight must not block on quota.
	uid := uuid.New().String()
	fake := &fakeQuota{status: quota.Status{Exceeded: true, UsedPct: 100}}
	app := newApp(uid, fake)

	req := httptest.NewRequest("OPTIONS", "/protected", nil)
	resp, _ := app.Test(req)
	defer resp.Body.Close()

	// OPTIONS hits no route handler, but it MUST NOT 429.
	if resp.StatusCode == fiber.StatusTooManyRequests {
		t.Errorf("OPTIONS must bypass quota gate, got 429")
	}
	if fake.checkCalls != 0 {
		t.Errorf("OPTIONS should skip Check entirely, got calls=%d", fake.checkCalls)
	}
}

// Issue #39 review fix — QuotaAbuseLimiter must apply 10/min ONLY to
// users who got a quota_exceeded 429 in the last minute. Healthy users
// hammering an endpoint past 10/min must pass through.
func TestQuotaAbuseLimiter_HealthyUserNotThrottled(t *testing.T) {
	ResetQuotaCooldownForTest()
	uid := uuid.New().String()

	app := fiber.New()
	app.Use(withAuthLocals(uid))
	app.Use(QuotaAbuseLimiter())
	app.Post("/protected", downstream)

	// Fire 15 requests in quick succession — under the old impl, the
	// 11th would 429 with rate_limited. Under the cooldown-scoped
	// limiter the user passes the Next skip and never accrues a count.
	for i := 0; i < 15; i++ {
		req := httptest.NewRequest("POST", "/protected", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("call #%d: %v", i+1, err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Errorf("healthy user call #%d should pass, got %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestQuotaAbuseLimiter_CooldownUserThrottled(t *testing.T) {
	ResetQuotaCooldownForTest()
	uidStr := uuid.New().String()
	uid, _ := uuid.Parse(uidStr)

	// Simulate the user having just received a quota_exceeded 429.
	markQuotaCooldown(uid)

	app := fiber.New()
	app.Use(withAuthLocals(uidStr))
	app.Use(QuotaAbuseLimiter())
	app.Post("/protected", downstream)

	// First 10 still pass (limiter Max=10/min); 11th gets the abuse 429.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/protected", nil)
		resp, _ := app.Test(req)
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("cooldown user call #%d should still be under 10/min ceiling, got %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}

	req := httptest.NewRequest("POST", "/protected", nil)
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Errorf("11th request must be rate-limited, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["error"] != "rate_limited" {
		t.Errorf("body.error: got %v want rate_limited", got["error"])
	}
	if resp.Header.Get("Retry-After") != "60" {
		t.Errorf("Retry-After: got %q want 60", resp.Header.Get("Retry-After"))
	}
}

func TestQuotaAbuseLimiter_CooldownExpires(t *testing.T) {
	// Direct cooldown set with a past expiry — isInQuotaCooldown
	// should clear it lazily and return false.
	ResetQuotaCooldownForTest()
	uid := uuid.New()
	quotaCooldownMarker.Store(uid, time.Now().Add(-time.Second))
	if isInQuotaCooldown(uid) {
		t.Errorf("expired cooldown entry must read as not-in-cooldown")
	}
	// And the lazy cleanup should have removed the entry.
	if _, ok := quotaCooldownMarker.Load(uid); ok {
		t.Errorf("expired cooldown entry must be deleted on lookup")
	}
}

func TestExceededMessage_TurkishMonth(t *testing.T) {
	// June 12 → "12 Haziran tarihinde sıfırlanacak."
	cycleEnd := time.Date(2026, time.June, 12, 0, 0, 0, 0, time.UTC)
	msg := exceededMessage(cycleEnd)
	want := "Bu döngünün kotası doldu. 12 Haziran tarihinde sıfırlanacak."
	if msg != want {
		t.Errorf("got %q want %q", msg, want)
	}
}

func TestExceededMessage_EmptyCycleEnd(t *testing.T) {
	if got := exceededMessage(time.Time{}); got != "Bu döngünün kotası doldu." {
		t.Errorf("zero time should produce short message, got %q", got)
	}
}
