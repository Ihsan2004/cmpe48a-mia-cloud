package quota

import (
	"os"
	"testing"
)

func TestPlanForTier_Defaults(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	t.Setenv("QUOTA_POC_USD", "")
	t.Setenv("QUOTA_INTERNAL_USD", "")
	ResetForTest()

	cases := []struct {
		tier       string
		wantCapNil bool
		wantCap    float64
	}{
		{TierFree, false, 2.0},
		{TierPOC, false, 20.0},
		{TierInternal, true, 0},
	}
	for _, c := range cases {
		t.Run(c.tier, func(t *testing.T) {
			p := PlanForTier(c.tier)
			if p.Tier != c.tier {
				t.Errorf("tier mismatch: got %q want %q", p.Tier, c.tier)
			}
			if c.wantCapNil {
				if p.CapUSD != nil {
					t.Errorf("expected unlimited cap, got %v", *p.CapUSD)
				}
				return
			}
			if p.CapUSD == nil {
				t.Fatalf("expected cap=%v, got nil", c.wantCap)
			}
			if *p.CapUSD != c.wantCap {
				t.Errorf("cap mismatch: got %v want %v", *p.CapUSD, c.wantCap)
			}
		})
	}
}

func TestPlanForTier_EnvOverride(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "3.50")
	t.Setenv("QUOTA_POC_USD", "100")
	t.Setenv("QUOTA_INTERNAL_USD", "500")
	ResetForTest()
	t.Cleanup(func() {
		os.Unsetenv("QUOTA_FREE_USD")
		os.Unsetenv("QUOTA_POC_USD")
		os.Unsetenv("QUOTA_INTERNAL_USD")
		ResetForTest()
	})

	if cap := PlanForTier(TierFree).CapUSD; cap == nil || *cap != 3.5 {
		t.Errorf("free cap override: got %v want 3.5", deref(cap))
	}
	if cap := PlanForTier(TierPOC).CapUSD; cap == nil || *cap != 100 {
		t.Errorf("poc cap override: got %v want 100", deref(cap))
	}
	if cap := PlanForTier(TierInternal).CapUSD; cap == nil || *cap != 500 {
		t.Errorf("internal cap override: got %v want 500", deref(cap))
	}
}

func TestPlanForTier_UnknownFallsBackToFree(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()
	p := PlanForTier("startup-tier-that-does-not-exist")
	if p.Tier != TierFree {
		t.Errorf("unknown tier should fall back to free, got %q", p.Tier)
	}
}

func TestResolveCap_OverrideWins(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	override := 7.50
	cap := ResolveCap(TierFree, &override)
	if cap == nil || *cap != 7.5 {
		t.Errorf("override should win over plan default: got %v", deref(cap))
	}

	cap = ResolveCap(TierFree, nil)
	if cap == nil || *cap != 2.0 {
		t.Errorf("nil override should fall to plan default: got %v", deref(cap))
	}
}

func TestResolveCap_InternalUnlimited(t *testing.T) {
	t.Setenv("QUOTA_INTERNAL_USD", "")
	ResetForTest()
	if cap := ResolveCap(TierInternal, nil); cap != nil {
		t.Errorf("internal with no override should be unlimited, got %v", *cap)
	}
}

func TestResolveCap_OverrideZeroBlocks(t *testing.T) {
	t.Setenv("QUOTA_INTERNAL_USD", "")
	ResetForTest()
	zero := 0.0
	cap := ResolveCap(TierInternal, &zero)
	if cap == nil || *cap != 0 {
		t.Errorf("override=0 should produce cap=0 (full block), got %v", deref(cap))
	}
}

func TestEnvFloatPtr_UnlimitedSentinels(t *testing.T) {
	// GitHub / Azure secret stores often disallow empty secret values,
	// so operators set explicit textual sentinels to mean "unlimited".
	// Each one must produce nil without emitting a "treating as
	// unlimited" warning (verified separately by log inspection in CI).
	cases := []string{"unlimited", "UNLIMITED", "None", "null", "inf", "Infinity", "  unlimited  "}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("QUOTA_INTERNAL_USD", raw)
			ResetForTest()
			if cap := PlanForTier(TierInternal).CapUSD; cap != nil {
				t.Errorf("sentinel %q must yield unlimited (nil cap), got %v", raw, *cap)
			}
		})
	}
}

func TestEnvFloat_InvalidFallsBack(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "not-a-number")
	ResetForTest()
	if cap := PlanForTier(TierFree).CapUSD; cap == nil || *cap != 2.0 {
		t.Errorf("invalid env should fall back to 2.0, got %v", deref(cap))
	}
}

func TestEnvFloat_NegativeFallsBack(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "-5")
	ResetForTest()
	if cap := PlanForTier(TierFree).CapUSD; cap == nil || *cap != 2.0 {
		t.Errorf("negative env should fall back to 2.0, got %v", deref(cap))
	}
}

func deref(p *float64) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}
