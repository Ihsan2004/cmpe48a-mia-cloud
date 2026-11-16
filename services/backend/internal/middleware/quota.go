package middleware

import (
	"errors"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/google/uuid"
)

// quotaCooldownMarker tracks userIDs that recently hit a quota_exceeded
// 429. The QuotaAbuseLimiter applies its per-minute ceiling ONLY to
// users in this set, so healthy users below the cap pay no per-request
// limit. Process-local — multi-replica deployments would need a shared
// store, but the ACA backend is single-instance today.
//
// Roadmap (docs/quota-limits-roadmap.md §Rate limit): "429 dönen userID
// için Fiber rate limiter: dakikada 10 istek tavanı". Earlier impl
// hit-rated every authenticated user on these endpoints — this scopes
// it correctly.
var quotaCooldownMarker sync.Map // map[uuid.UUID]time.Time (expiry)

const quotaCooldownTTL = time.Minute

func markQuotaCooldown(userID uuid.UUID) {
	if userID == uuid.Nil {
		return
	}
	quotaCooldownMarker.Store(userID, time.Now().Add(quotaCooldownTTL))
}

// isInQuotaCooldown checks whether the user has been 429'd within the
// last quotaCooldownTTL. Expired entries are deleted lazily — they're
// harmless until next overwrite but the cleanup keeps the map bounded
// when a user finally quiets down.
func isInQuotaCooldown(userID uuid.UUID) bool {
	if userID == uuid.Nil {
		return false
	}
	v, ok := quotaCooldownMarker.Load(userID)
	if !ok {
		return false
	}
	expiry, ok := v.(time.Time)
	if !ok || time.Now().After(expiry) {
		quotaCooldownMarker.Delete(userID)
		return false
	}
	return true
}

// RequireQuota gates user-initiated cost-bearing endpoints behind the
// monthly soft cap. Behaviour:
//
//   - Status.Exceeded ⇒ 429 + Retry-After + X-RateLimit-* + JSON body
//     keyed by `error: "quota_exceeded"`. The body returns yüzde + reset
//     tarihi, **never dollars** — UX karar (kullanıcı dolar görmez).
//   - Allowed ⇒ X-RateLimit-* headers populated and the chain continues.
//   - ErrUserQuotaNotFound or other repo errors ⇒ fail-open with slog.
//     Trigger + backfill guarantee the row exists; not blocking real
//     users over an infra hiccup is the safer default. quota_checks_total
//     counter (Faz 5) will surface this.
//
// AuthMiddleware MUST run before this — userID is read from c.Locals.
func RequireQuota(svc quota.Service) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() == fiber.MethodOptions {
			return c.Next()
		}

		userIDStr, ok := c.Locals("userID").(string)
		if !ok || userIDStr == "" {
			// Auth middleware should have populated this; treat the
			// absence as an unauthenticated request rather than a
			// quota error.
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Unauthorized",
				"code":  "TOKEN_MISSING",
			})
		}
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid user ID",
				"code":  "TOKEN_INVALID_CLAIMS",
			})
		}

		status, err := svc.Check(c.Context(), userID)
		if err != nil {
			if errors.Is(err, repository.ErrUserQuotaNotFound) {
				slog.Error("Quota check: user_quotas row missing — fail-open",
					"user_id", userID)
			} else {
				slog.Error("Quota check failed — fail-open",
					"user_id", userID, "error", err)
			}
			// Fail-open: do NOT set rate-limit headers (we don't have a
			// real read) and let the handler run.
			return c.Next()
		}

		writeRateLimitHeaders(c, status)

		if status.Exceeded {
			// Use the route pattern (not the matched URL) so the label
			// cardinality stays bounded across millions of distinct
			// :id values. c.Route().Path returns "/api/meetings/:id/join"
			// rather than the concrete URL.
			endpoint := c.Route().Path
			if endpoint == "" {
				endpoint = c.Path()
			}
			metrics.QuotaRejections.WithLabelValues(status.Tier, endpoint).Inc()

			// Flag this user for the abuse limiter (60s sliding TTL).
			// Roadmap §Rate limit scopes the per-minute ceiling to
			// users who already hit 429 — otherwise healthy traffic
			// would get throttled at the 11th legitimate cost action.
			markQuotaCooldown(userID)

			writeRetryAfter(c, status.CycleEndAt)
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":           "quota_exceeded",
				"message":         exceededMessage(status.CycleEndAt),
				"used_pct":        status.UsedPct,
				"cycle_resets_at": status.CycleEndAt.UTC().Format(time.RFC3339),
				"tier":            status.Tier,
			})
		}

		return c.Next()
	}
}

// writeRateLimitHeaders publishes the standard X-RateLimit-* triplet plus
// the tier badge header. Unit is **percent**; the cap is always 100 for
// finite-cap users, so the user-visible quota — like the JSON response —
// stays dollar-free. For unlimited tiers (internal, no override) Remaining
// stays at 100 and Reset still points at the next cycle anchor for UX
// consistency.
func writeRateLimitHeaders(c *fiber.Ctx, status quota.Status) {
	c.Set("X-RateLimit-Limit", "100")
	remaining := 100 - status.UsedPct
	if remaining < 0 {
		remaining = 0
	}
	c.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	if !status.CycleEndAt.IsZero() {
		c.Set("X-RateLimit-Reset", strconv.FormatInt(status.CycleEndAt.Unix(), 10))
	}
	if status.Tier != "" {
		c.Set("X-RateLimit-Tier", status.Tier)
	}
}

func writeRetryAfter(c *fiber.Ctx, cycleEnd time.Time) {
	if cycleEnd.IsZero() {
		return
	}
	seconds := int64(math.Ceil(time.Until(cycleEnd).Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Set("Retry-After", strconv.FormatInt(seconds, 10))
}

// exceededMessage returns the Turkish user-facing string. The frontend
// can localize via the `error` code, but a sane default keeps non-UI
// consumers (cron alerts, log aggregators) readable.
func exceededMessage(cycleEnd time.Time) string {
	if cycleEnd.IsZero() {
		return "Bu döngünün kotası doldu."
	}
	day := cycleEnd.UTC().Day()
	month := turkishMonth(cycleEnd.UTC().Month())
	return "Bu döngünün kotası doldu. " + strconv.Itoa(day) + " " + month + " tarihinde sıfırlanacak."
}

// QuotaAbuseLimiter caps quota-gated endpoints at 10 req/min per user
// — but ONLY for users currently in the quota cooldown set (i.e. those
// who got a quota_exceeded 429 within the last minute). Healthy users
// below the cap pass through immediately via the Next predicate, so a
// legitimate burst of bot-start clicks doesn't get throttled.
//
// Mounted AFTER AuthMiddleware so the userID key is populated.
func QuotaAbuseLimiter() fiber.Handler {
	return limiter.New(limiter.Config{
		// Skip predicate: when this returns true, the limiter doesn't
		// count the request and lets it through. We skip everyone who
		// is NOT in the cooldown set — this is the "scope the limiter
		// to 429'd users" roadmap requirement.
		Next: func(c *fiber.Ctx) bool {
			uidStr, ok := c.Locals("userID").(string)
			if !ok || uidStr == "" {
				return true // pre-auth or system; let downstream auth handle
			}
			uid, err := uuid.Parse(uidStr)
			if err != nil {
				return true
			}
			return !isInQuotaCooldown(uid)
		},
		Max:        10,
		Expiration: time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			if s, ok := c.Locals("userID").(string); ok && s != "" {
				return "quota-abuse:" + s
			}
			return "quota-abuse-ip:" + c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			c.Set("Retry-After", "60")
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":   "rate_limited",
				"message": "Çok fazla istek. Bir dakika sonra tekrar dene.",
			})
		},
	})
}

// ResetQuotaCooldownForTest clears the in-memory cooldown set. Exported
// only for use from middleware_test.go in the same package; not for
// production callers.
func ResetQuotaCooldownForTest() {
	quotaCooldownMarker.Range(func(k, _ any) bool {
		quotaCooldownMarker.Delete(k)
		return true
	})
}

func turkishMonth(m time.Month) string {
	switch m {
	case time.January:
		return "Ocak"
	case time.February:
		return "Şubat"
	case time.March:
		return "Mart"
	case time.April:
		return "Nisan"
	case time.May:
		return "Mayıs"
	case time.June:
		return "Haziran"
	case time.July:
		return "Temmuz"
	case time.August:
		return "Ağustos"
	case time.September:
		return "Eylül"
	case time.October:
		return "Ekim"
	case time.November:
		return "Kasım"
	case time.December:
		return "Aralık"
	}
	return ""
}
