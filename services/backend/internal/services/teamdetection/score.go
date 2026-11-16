package teamdetection

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"mia-website/backend/internal/services"

	"gonum.org/v1/gonum/graph/simple"
)

// ScoreCommunities turns the cluster output into a slice of fully-populated
// DetectedTeam values. CONTEXT.md D-08..D-12.
//
// Inputs:
//   - communities: cluster.go output (sorted size-desc, first-email-asc).
//     Empty slice means caller already routed to empty-state — return [].
//   - events: the original BackfillEvent slice (used for TopSeries +
//     SuggestedName + SharedMeetingCount derivation).
//   - g: the co-attendance graph (used for confidence density numerator).
//   - idx: the email↔node-ID index (used to translate graph edges back
//     to emails for in-cluster checks).
//
// Output: []DetectedTeam (NOT nil — empty slice when no communities).
// Order matches the input cluster order (so caller can match "Takım N"
// numbering).
func ScoreCommunities(
	communities [][]string,
	events []services.BackfillEvent,
	g *simple.WeightedUndirectedGraph,
	idx *nodeIndex,
) []DetectedTeam {
	if len(communities) == 0 {
		return []DetectedTeam{}
	}

	out := make([]DetectedTeam, 0, len(communities))
	for teamIdx, members := range communities {
		// members is already sorted alphabetically (cluster.go). Build a
		// set for quick "is this email in this team?" checks.
		memberSet := make(map[string]bool, len(members))
		for _, m := range members {
			memberSet[m] = true
		}

		// 1) Collect the in-cluster meetings: for each event, count it
		//    if at least 2 of its attendees are members of this team.
		//    Bucket by RecurringEventID (or EventID for ad-hoc) for
		//    TopSeries weight accumulation.
		type seriesAccum struct {
			title  string
			weight float64
		}
		seriesByID := make(map[string]*seriesAccum)
		// Also: per-member, the set of meeting IDs they co-attended with
		// another member of this team (TRUST-01).
		memberMeetings := make(map[string]map[string]bool) // email -> meetingID set
		for _, m := range members {
			memberMeetings[m] = make(map[string]bool)
		}

		for _, ev := range events {
			// Emails in this event that ARE members of this team.
			memInEvent := make([]string, 0)
			for _, a := range ev.Attendees {
				if memberSet[a.Email] {
					memInEvent = append(memInEvent, a.Email)
				}
			}
			if len(memInEvent) < 2 {
				continue // no in-cluster co-attendance contributed
			}
			n := len(ev.Attendees)
			if n < 2 {
				continue
			}
			// In-cluster edge-weight contribution from this single meeting:
			// (memInEvent choose 2) edges × 1/(n-1) raw weight each.
			pairs := float64(len(memInEvent)*(len(memInEvent)-1)) / 2.0
			rawPerEdge := 1.0 / float64(n-1)
			contribution := pairs * rawPerEdge

			bucketID := ev.RecurringEventID
			if bucketID == "" {
				bucketID = "evt:" + ev.EventID
			}
			s, ok := seriesByID[bucketID]
			if !ok {
				s = &seriesAccum{title: ev.Title}
				seriesByID[bucketID] = s
			}
			s.weight += contribution

			// TRUST-01: every member in this meeting (who is in this
			// team) gains this meeting ID in their set.
			for _, m := range memInEvent {
				memberMeetings[m][ev.EventID] = true
			}
		}

		// 2) ConfidenceScore — D-09 formula.
		// norm_median = min(1.0, median(SharedMeetingCount) / ConfidenceMedianNormConst)
		// density     = sum_of_intra_cluster_edge_weights / max_possible_edge_weight_for_node_count
		// score       = ConfidenceWeightMedian * norm_median + ConfidenceWeightDensity * density
		sharedCounts := make([]int, 0, len(members))
		for _, m := range members {
			sharedCounts = append(sharedCounts, len(memberMeetings[m]))
		}
		median := medianInt(sharedCounts)
		normMedian := float64(median) / float64(ConfidenceMedianNormConst)
		if normMedian > 1.0 {
			normMedian = 1.0
		}

		// Density: sum the actual edge weights between every (a,b) pair
		// inside the team (using the graph), divide by the theoretical
		// maximum. Max = (numMembers choose 2) × 1.0 (the highest single
		// edge weight in our model is 1/(n-1) for n=2, which is 1.0, and
		// can accumulate across distinct weeks — but for the density
		// normalization we cap at choose-2 to keep score ∈ [0,1]).
		intraSum := 0.0
		for i := 0; i < len(members); i++ {
			aID, aOK := idx.ID(members[i])
			if !aOK {
				continue
			}
			for j := i + 1; j < len(members); j++ {
				bID, bOK := idx.ID(members[j])
				if !bOK {
					continue
				}
				if e := g.WeightedEdgeBetween(aID, bID); e != nil {
					intraSum += e.Weight()
				}
			}
		}
		maxPairs := float64(len(members)*(len(members)-1)) / 2.0
		var density float64
		if maxPairs > 0 {
			density = intraSum / maxPairs
			if density > 1.0 {
				density = 1.0
			}
		}

		score := ConfidenceWeightMedian*normMedian + ConfidenceWeightDensity*density
		if score < 0 {
			score = 0
		} else if score > 1 {
			score = 1
		}
		bucket := bucketFor(score)

		// 3) SuggestedName (D-10) — stopword-strip + most-frequent token.
		name := pickSuggestedName(events, memberSet, teamIdx)

		// 4) TopSeries (D-11) — top 3 raw titles by weight, lex tie-break.
		//     Inlined (no standalone helper) — seriesAccum is unexported and
		//     can't appear in a top-level helper's parameter list without
		//     exporting it. Flatten + sort here.
		type topSeriesEntry struct {
			title  string
			weight float64
		}
		flat := make([]topSeriesEntry, 0, len(seriesByID))
		for _, sx := range seriesByID {
			flat = append(flat, topSeriesEntry{title: sx.title, weight: sx.weight})
		}
		sort.Slice(flat, func(i, j int) bool {
			if flat[i].weight != flat[j].weight {
				return flat[i].weight > flat[j].weight
			}
			return flat[i].title < flat[j].title
		})
		if len(flat) > 3 {
			flat = flat[:3]
		}
		topSeries := make([]string, 0, len(flat))
		for _, e := range flat {
			topSeries = append(topSeries, e.title)
		}

		// 5) Members slice with SharedMeetingCount.
		dms := make([]DetectedMember, 0, len(members))
		for _, m := range members {
			dms = append(dms, DetectedMember{
				Email:              m,
				SharedMeetingCount: len(memberMeetings[m]),
			})
		}

		out = append(out, DetectedTeam{
			SuggestedName:    name,
			ConfidenceBucket: bucket,
			ConfidenceScore:  score,
			TopSeries:        topSeries,
			Members:          dms,
		})
	}
	return out
}

// bucketFor maps a [0,1] confidence score to "high"/"mid"/"low" per D-09.
func bucketFor(score float64) string {
	switch {
	case score >= ConfidenceThresholdHigh:
		return "high"
	case score >= ConfidenceThresholdMid:
		return "mid"
	default:
		return "low"
	}
}

// medianInt returns the median of an int slice. Empty slice → 0. Even
// length → average of two middle values (rounded down via integer math
// — we only care about the bucket boundary, not sub-integer precision).
func medianInt(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]int, len(xs))
	copy(cp, xs)
	sort.Ints(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

// pickSuggestedName implements D-10: tokenize every meeting title where
// at least 2 of the team's members were attendees, strip punctuation +
// stopwords, return the most-frequent surviving token (title-cased).
// Fallback "Takım N" (1-indexed teamIdx+1). Tie-break: alphabetical.
func pickSuggestedName(events []services.BackfillEvent, memberSet map[string]bool, teamIdx int) string {
	counts := make(map[string]int)
	for _, ev := range events {
		memHits := 0
		for _, a := range ev.Attendees {
			if memberSet[a.Email] {
				memHits++
				if memHits >= 2 {
					break
				}
			}
		}
		if memHits < 2 {
			continue
		}
		for _, tok := range tokenizeTitle(ev.Title) {
			if IsStopword(tok) {
				continue
			}
			counts[tok]++
		}
	}
	if len(counts) == 0 {
		return "Takım " + strconv.Itoa(teamIdx+1)
	}
	// Pick most-frequent; tie-break: alphabetical. Iterate keys in
	// sorted order so the tie-break is deterministic (map iteration is
	// otherwise non-deterministic in Go).
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	bestTok := ""
	bestN := 0
	for _, tok := range keys {
		n := counts[tok]
		if n > bestN {
			bestTok = tok
			bestN = n
		}
		// n < bestN: skip. n == bestN: keep the earlier (alphabetical
		// first) which is already in bestTok thanks to sorted iteration.
	}
	return titleCase(bestTok)
}

// tokenizeTitle lowercases title, strips punctuation, splits on whitespace.
// Returns lower-case tokens for stopword comparison.
func tokenizeTitle(title string) []string {
	if title == "" {
		return nil
	}
	// Strip punctuation; keep letters + digits + spaces.
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// titleCase upper-cases the first rune (Turkish-aware enough for the
// common case; strings.Title is deprecated and locale-broken).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
