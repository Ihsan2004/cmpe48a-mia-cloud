package services

import "testing"

func TestTitleSimilarityV3_ExactMatchSpecific(t *testing.T) {
	t.Parallel()
	sim, specific := TitleSimilarityV3("Product Roadmap", "Product Roadmap")
	if sim != 1.0 {
		t.Errorf("expected 1.0 for exact match, got %f", sim)
	}
	if specific != 2 {
		t.Errorf("expected 2 specific common (product, roadmap), got %d", specific)
	}
}

func TestTitleSimilarityV3_GenericOnlyOverlap_Penalised(t *testing.T) {
	t.Parallel()
	// "Engineering Sync" vs "Backend Sync" — only "sync" overlaps and
	// it's generic. Legacy TitleSimilarity returns 0.5 (1/2). v3 should
	// return ~0.2 (0.4/2) and report 0 specific common tokens.
	sim, specific := TitleSimilarityV3("Engineering Sync", "Backend Sync")
	legacy := TitleSimilarity("Engineering Sync", "Backend Sync")

	if specific != 0 {
		t.Errorf("expected 0 specific common, got %d", specific)
	}
	if sim >= legacy {
		t.Errorf("v3 should down-weight generic-only overlap (legacy=%f, v3=%f)", legacy, sim)
	}
	if sim > 0.25 {
		t.Errorf("expected sim ≤ 0.25 for generic-only overlap, got %f", sim)
	}
}

func TestTitleSimilarityV3_SpecificPlusGeneric(t *testing.T) {
	t.Parallel()
	// "Roadmap Sync" vs "Roadmap Review" — "roadmap" specific, "sync" /
	// "review" generic but not shared. Common = 1 specific.
	// 1 specific (1.0) / max(2,2) = 0.5
	sim, specific := TitleSimilarityV3("Roadmap Sync", "Roadmap Review")
	if specific != 1 {
		t.Errorf("expected 1 specific common, got %d", specific)
	}
	if sim < 0.49 || sim > 0.51 {
		t.Errorf("expected sim ≈ 0.5, got %f", sim)
	}
}

func TestTitleSimilarityV3_GenericOverlapStillCounts(t *testing.T) {
	t.Parallel()
	// "Product Roadmap Sync" vs "Roadmap Sync" — common: roadmap (specific) + sync (generic).
	// numerator = 1.0 + 0.4 = 1.4 ; denominator = max(3,2) = 3 → ≈ 0.467
	sim, specific := TitleSimilarityV3("Product Roadmap Sync", "Roadmap Sync")
	if specific != 1 {
		t.Errorf("expected 1 specific common, got %d", specific)
	}
	if sim < 0.45 || sim > 0.49 {
		t.Errorf("expected sim ≈ 0.467, got %f", sim)
	}
}

func TestTitleSimilarityV3_TurkishGenericTokens(t *testing.T) {
	t.Parallel()
	// "Ürün Toplantısı" vs "Pazarlama Toplantısı" — "toplantı" should
	// be treated as generic (Turkish equivalent of "meeting").
	sim, specific := TitleSimilarityV3("Ürün Toplantı", "Pazarlama Toplantı")
	if specific != 0 {
		t.Errorf("expected 0 specific common (toplantı is generic), got %d", specific)
	}
	if sim > 0.25 {
		t.Errorf("expected low sim for generic-only Turkish overlap, got %f", sim)
	}
}

func TestTitleSimilarityV3_CaseInsensitive(t *testing.T) {
	t.Parallel()
	sim1, sp1 := TitleSimilarityV3("PRODUCT ROADMAP", "product roadmap")
	sim2, sp2 := TitleSimilarityV3("Product Roadmap", "Product Roadmap")
	if sim1 != sim2 || sp1 != sp2 {
		t.Errorf("case-insensitive divergence: (%f,%d) vs (%f,%d)", sim1, sp1, sim2, sp2)
	}
}

func TestTitleSimilarityV3_BothEmpty(t *testing.T) {
	t.Parallel()
	sim, specific := TitleSimilarityV3("", "")
	if sim != 1.0 || specific != 0 {
		t.Errorf("expected (1.0, 0) for both empty, got (%f, %d)", sim, specific)
	}
}

func TestTitleSimilarityV3_OneEmpty(t *testing.T) {
	t.Parallel()
	sim, specific := TitleSimilarityV3("Product Roadmap", "")
	if sim != 0 || specific != 0 {
		t.Errorf("expected (0, 0) for one empty, got (%f, %d)", sim, specific)
	}
}

func TestTitleSimilarityV3_StopWordsStrippedSameAsLegacy(t *testing.T) {
	t.Parallel()
	// "Haftalık Product Roadmap" → stop word "haftalık" removed, leaves
	// "product roadmap"; both compared against "Product Roadmap" should
	// score the same as exact-match.
	sim, specific := TitleSimilarityV3("Haftalık Product Roadmap", "Product Roadmap")
	if sim != 1.0 {
		t.Errorf("expected 1.0 after stop-word strip, got %f", sim)
	}
	if specific != 2 {
		t.Errorf("expected 2 specific common, got %d", specific)
	}
}

func TestIsGenericTitleToken(t *testing.T) {
	t.Parallel()
	generic := []string{"sync", "meeting", "review", "standup", "toplantı", "ekip"}
	for _, s := range generic {
		if !isGenericTitleToken(s) {
			t.Errorf("expected %q to be generic", s)
		}
	}
	specific := []string{"product", "roadmap", "ürün", "pazarlama", "sprint"}
	for _, s := range specific {
		if isGenericTitleToken(s) {
			t.Errorf("expected %q to be specific", s)
		}
	}
}
