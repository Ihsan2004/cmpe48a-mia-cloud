package services

// Title similarity for chain matcher v3.
//
// Differs from the legacy TitleSimilarity (chain_matcher.go) in that
// generic title tokens — words like "sync", "meeting", "review",
// "weekly" — are down-weighted in the overlap score. The legacy form
// counted them at full weight, which let "Weekly Sync" match
// "Engineering Sync" as if "sync" alone were a chain signal.
//
// The roadmap (docs/CHAIN_DETECTION_ROADMAP.md §Stage 1) calls this
// guard out explicitly: generic title tokens MUST NOT, on their own,
// produce a strong title signal.
//
// Implementation: weighted Jaccard. Each common token contributes its
// `tokenWeight` (1.0 specific, genericTokenWeight generic) to the
// numerator; the denominator is `max(|wordsA|, |wordsB|)` (unweighted)
// so two specific overlaps still cap at the full-similarity ceiling.
// `specificCommon` is reported alongside `T` so callers can plug it
// into `feature_scores` for the shadow log without recomputing.

// genericTokenWeight is the partial credit a generic title token gets
// when it appears on both sides of a comparison. Lower than 1.0 so
// "Weekly Sync" vs "Backend Sync" doesn't punch above its weight; not
// zero because two meetings sharing "sync" *is* a weak signal — just
// not enough to gate by itself. 0.4 is the smallest value where the
// legacy hard threshold (T >= 0.5) still passes for "Product Sync"
// vs "Product Sync Meeting" without false positives.
const genericTokenWeight = 0.4

// genericTitleTokens are tokens whose presence in *both* titles is too
// weak a signal to drive a chain match by themselves. Mirrors the
// roadmap's example list and adds the obvious adjacent forms (Turkish
// + English) we actually see in calendars.
//
// Stop words (chain_matcher.go) are stripped earlier. Tokens here
// survive normalisation but contribute partial weight.
var genericTitleTokens = map[string]bool{
	// English
	"sync":           true,
	"syncup":         true,
	"meeting":        true,
	"call":           true,
	"review":         true,
	"standup":        true,
	"check":          true,
	"checkin":        true,
	"catchup":        true,
	"chat":           true,
	"discussion":     true,
	"update":         true,
	"updates":        true,
	"planning":       true,
	"retro":          true,
	"retrospective":  true,
	"1on1":           true,
	"oneonone":       true,
	"team":           true,
	// Turkish
	"toplantı":   true,
	"toplanti":   true,
	"sohbet":     true,
	"görüşme":    true,
	"gorusme":    true,
	"degerlendirme": true,
	"değerlendirme": true,
	"planlama":   true,
	"ekip":       true,
	"takım":      true,
	"takim":      true,
}

// isGenericTitleToken reports whether a normalised token is on the
// generic list. Caller is responsible for lowercasing.
func isGenericTitleToken(t string) bool {
	return genericTitleTokens[t]
}

// TitleSimilarityV3 returns the v3 (generic-token-aware) similarity and
// a count of how many overlap tokens were *specific* (i.e. survived the
// generic guard). The first return value drives the Stage 1 gate; the
// second helps the shadow log surface "this match is mostly noise".
//
// Behaviour:
//
//	"Product Sync"          vs "Product Sync"          → 1.0, 1 specific  ("product")
//	"Engineering Sync"      vs "Backend Sync"          → 0.2, 0 specific  ("sync" generic only)
//	"Weekly Roadmap Sync"   vs "Roadmap Sync"          → ~0.7, 1 specific ("roadmap")
//	"Sprint Planning"       vs "Sprint Review"         → 0.5, 1 specific ("sprint")
//
// Empty inputs follow the legacy convention: both empty = 1.0, one
// empty = 0.0. Stop-word removal reuses normalizeTitle from
// chain_matcher.go so the two matchers agree on tokenisation.
func TitleSimilarityV3(a, b string) (similarity float64, specificCommon int) {
	return titleSimilarityV3WithWords(normalizeTitle(a), b)
}

// titleSimilarityV3WithWords is the hot-loop variant of TitleSimilarityV3:
// the caller passes A's normalised words already tokenised, so a
// Decide pass that scores N candidates against the same new meeting
// only normalises A once.
func titleSimilarityV3WithWords(wordsA []string, b string) (similarity float64, specificCommon int) {
	wordsB := normalizeTitle(b)

	if len(wordsA) == 0 && len(wordsB) == 0 {
		return 1.0, 0
	}
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0, 0
	}

	common := wordIntersection(wordsA, wordsB)

	weighted := 0.0
	for _, w := range common {
		if isGenericTitleToken(w) {
			weighted += genericTokenWeight
		} else {
			weighted += 1.0
			specificCommon++
		}
	}

	maxLen := len(wordsA)
	if len(wordsB) > maxLen {
		maxLen = len(wordsB)
	}

	return weighted / float64(maxLen), specificCommon
}
