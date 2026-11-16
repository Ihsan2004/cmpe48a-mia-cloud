// Package quota implements per-user monthly USD soft-cap enforcement.
//
// Tier defaults live here, not in the database CHECK constraint, so adding
// a new tier ("startup", "enterprise") is a code + env change — not a
// migration. Migration 021's user_quotas.tier is plain TEXT for this
// reason.
//
// Cap precedence: monthly_usd_cap_override on the user row > plan default
// from this registry. Internal tier defaults to nil (unlimited).
package quota

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	TierFree     = "free"
	TierPOC      = "poc"
	TierInternal = "internal"
)

// Plan is the static config for a single tier.
type Plan struct {
	Tier   string
	CapUSD *float64 // nil ⇒ unlimited
}

var (
	registryOnce sync.Once
	registry     map[string]Plan
)

func plans() map[string]Plan {
	registryOnce.Do(func() {
		freeCap := envFloat("QUOTA_FREE_USD", 2.0)
		pocCap := envFloat("QUOTA_POC_USD", 20.0)
		internalCap := envFloatPtr("QUOTA_INTERNAL_USD")

		registry = map[string]Plan{
			TierFree:     {Tier: TierFree, CapUSD: &freeCap},
			TierPOC:      {Tier: TierPOC, CapUSD: &pocCap},
			TierInternal: {Tier: TierInternal, CapUSD: internalCap},
		}
	})
	return registry
}

// PlanForTier returns the configured plan for a tier key. Unknown tiers
// fall back to free — this can only happen if an admin patches a user
// row to a tier string the running binary doesn't recognize (e.g. a tier
// added in a newer build during a rolling deploy). Falling back to free
// is the safest default: limits get applied rather than skipped.
func PlanForTier(tier string) Plan {
	if p, ok := plans()[tier]; ok {
		return p
	}
	slog.Warn("Unknown quota tier — falling back to free", "tier", tier)
	return plans()[TierFree]
}

// ResolveCap returns the effective cap for a user: override wins over
// the tier default. Nil means unlimited.
func ResolveCap(tier string, override *float64) *float64 {
	if override != nil {
		v := *override
		return &v
	}
	return PlanForTier(tier).CapUSD
}

// ResetForTest clears the memoized registry so QUOTA_* env changes in
// tests take effect. Not exported for production callers.
func ResetForTest() {
	registryOnce = sync.Once{}
	registry = nil
}

func envFloat(key string, fallback float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		slog.Warn("Invalid quota cap env var — using fallback",
			"key", key, "value", raw, "fallback", fallback)
		return fallback
	}
	return v
}

// envFloatPtr returns nil when env unset, *float when set. Empty string is
// treated as unset (unlimited). Explicit textual sentinels — "unlimited",
// "none", "null", "inf" — are honoured without a warning log: secret-management
// platforms (GitHub Actions, Azure KeyVault) often disallow truly empty
// values, so operators reach for these sentinels to express
// "intentionally no cap". Negative / unparseable numbers still log a
// warning so a config typo is visible.
func envFloatPtr(key string) *float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	switch strings.ToLower(raw) {
	case "unlimited", "none", "null", "inf", "infinity":
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		slog.Warn("Invalid quota cap env var — treating as unlimited",
			"key", key, "value", raw)
		return nil
	}
	return &v
}
