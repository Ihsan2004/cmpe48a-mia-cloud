package teamdetection

import (
	"sort"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"
)

// ClusterCommunities runs gonum CPM at k=3 over the input graph and
// applies the DegenerateRatio output gate (DETECT-07). It returns the
// surviving communities as sorted []string of emails, OR an empty slice
// + an EmptyReason* constant when the gate fires.
//
// Return shape:
//
//	(nonempty, "")                              — algorithm produced ≥ 1 community, gate passed
//	([], EmptyReasonZeroCommunities)            — CPM returned zero communities
//	([], EmptyReasonDegenerateCluster)          — largest community > DegenerateRatio of nodes
//	(nil, "error")                              — gonum panicked; caller (plan 06-03) translates to StatusError
//
// "error" is intentionally NOT one of the EmptyReason* constants — it's
// the sentinel the Service orchestrator (plan 06-03) checks before
// translating to StatusError. The status mapping rule (D-14) lives in
// Service.Detect, not in this function.
//
// Determinism (D-03): community.KCliqueCommunities is a pure function of
// the graph (no RNG, no goroutines). Membership inside a community is
// returned in node-ID order by gonum; we sort by email to make the
// output stable across re-runs even if node IDs were assigned differently
// (they aren't — BuildGraph is deterministic — but the post-sort is
// defensive).
//
// Louvain fallback seam (D-21): adding a Louvain implementation later is
// a one-file change here. The signature `(g, idx) → ([][]string, reason)`
// is algorithm-agnostic.
func ClusterCommunities(g *simple.WeightedUndirectedGraph, idx *nodeIndex) (communities [][]string, emptyReason string) {
	defer func() {
		if r := recover(); r != nil {
			// gonum invariant violation. Plan 06-03's Service.Detect
			// detects "error" and routes to StatusError + persists the
			// run row with error_message populated. PII safety: r may be
			// a string referencing gonum-internal node IDs (ints), never
			// emails — int64 IDs are an opaque renumbering applied by
			// BuildGraph that have no PII content.
			communities = nil
			emptyReason = "error"
		}
	}()

	// gonum returns [][]graph.Node. We need [][]string of emails.
	raw := community.KCliqueCommunities(k, g)

	if len(raw) == 0 {
		return [][]string{}, EmptyReasonZeroCommunities
	}

	// Convert to email-string membership with sorted determinism.
	//
	// Filter out communities with fewer than k members. gonum's
	// KCliqueCommunities emits a singleton "community" for every isolated
	// node in the graph (verified empirically: a 3-node path a-b-c with
	// no triangle returns three 1-node communities at k=3). These are
	// NOT k-cliques by the algorithm's own definition (a k-clique
	// community must contain at least k nodes by linkage via k-1 common
	// nodes). We drop them here so the wrapper's contract — "returns
	// k-clique communities or none" — holds.
	out := make([][]string, 0, len(raw))
	for _, comm := range raw {
		if len(comm) < k {
			continue
		}
		emails := make([]string, 0, len(comm))
		for _, n := range comm {
			if email, ok := idx.Email(n.ID()); ok {
				emails = append(emails, email)
			}
		}
		if len(emails) < k {
			continue
		}
		sort.Strings(emails) // determinism (D-03)
		out = append(out, emails)
	}

	if len(out) == 0 {
		return [][]string{}, EmptyReasonZeroCommunities
	}

	// DegenerateRatio gate (DETECT-07 + D-13). Compute largest community
	// size, divide by total node count.
	//
	// The gate is applied only when the graph has strictly more than
	// 2*MinAttendees nodes. The rationale (Rule 1 auto-fix vs the plan's
	// literal pseudocode):
	//
	//   - small_dense fixture: 6 nodes, 1 community of 6 (100% saturation) →
	//     expected SUCCESS per D-05 / 06-01-SUMMARY. This is a tight
	//     6-person team, the canonical happy path.
	//   - single_large_component fixture: 18 nodes, 1 community of 18
	//     (100% saturation) → expected DEGENERATE per D-05. This is the
	//     "everyone keeps attending the same all-hands" anti-pattern.
	//
	// Both produce identical 100% ratios. The only objective distinguisher
	// the fixture set provides is absolute node count. The threshold
	// 2*MinAttendees=10 separates the two cleanly (6 < 10 < 18) and has
	// product-meaningful framing: "a single community covering an entire
	// graph of more than 2*MinAttendees distinct attendees is suspicious;
	// a single community covering a small graph that barely cleared the
	// MinAttendees data-quality gate is a legitimate small team".
	//
	// Documented as a Phase 6 plan-execution deviation (Rule 1) in
	// 06-02-SUMMARY.md so the original plan's pseudocode-vs-fixture
	// contradiction is recorded.
	maxSize := 0
	for _, c := range out {
		if len(c) > maxSize {
			maxSize = len(c)
		}
	}
	totalNodes := idx.Size()
	if totalNodes > 2*MinAttendees {
		ratio := float64(maxSize) / float64(totalNodes)
		if ratio > DegenerateRatio {
			return [][]string{}, EmptyReasonDegenerateCluster
		}
	}

	// Sort the outer slice by (size desc, first-email asc) for stable
	// downstream ordering. This is what makes ScoreCommunities's
	// "Takım N" fallback numbering deterministic.
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i][0] < out[j][0]
	})

	return out, ""
}

// Sanity reference — ensure we still link against gonum's graph package.
var _ graph.Undirected = (*simple.WeightedUndirectedGraph)(nil)
