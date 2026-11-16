//go:build sweep

// This file is excluded from the default test run via the `sweep` build
// tag. Run with: `go test -tags sweep -run TestSweep -v ./internal/services/teamdetection/...`
//
// Purpose: DETECT-08 / D-04 / D-07 — when re-tuning algorithm parameters,
// this test reproducibly evaluates each candidate (parameter, value)
// combination against all 8 hand-labeled fixtures and prints a Markdown
// table that the human pastes back into params.go's comment header
// (CONTEXT.md "Specific Ideas" — Sweep test output paste-back).
//
// The sweep does NOT mutate the package-level consts in params.go. It
// runs a parameterized re-implementation of the pipeline so that
// overriding a single parameter doesn't require recompiling.

package teamdetection

import (
	"fmt"
	"sort"
	"testing"

	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"
)

// sweepParams is the per-iteration override set. Zero-valued fields
// fall back to the production const value.
type sweepParams struct {
	K                  int
	MaxMeetingSize     int
	MinMeetingsPerEdge int
	DegenerateRatio    float64
}

func (p sweepParams) k() int {
	if p.K == 0 {
		return k
	}
	return p.K
}
func (p sweepParams) maxMeetingSize() int {
	if p.MaxMeetingSize == 0 {
		return MaxMeetingSize
	}
	return p.MaxMeetingSize
}
func (p sweepParams) minMeetingsPerEdge() int {
	if p.MinMeetingsPerEdge == 0 {
		return MinMeetingsPerEdge
	}
	return p.MinMeetingsPerEdge
}
func (p sweepParams) degenerateRatio() float64 {
	if p.DegenerateRatio == 0 {
		return DegenerateRatio
	}
	return p.DegenerateRatio
}

// runFixtureWithParams runs the pipeline against a single fixture and
// returns the resulting Status + EmptyReason. NOTE: this is a
// re-implementation of Service.Detect's algorithm-only path (no DB, no
// metrics, no fetch). It exists in the sweep test file so production
// code stays clean. If params.go consts change shape, this function
// must change too — accepted maintenance cost for a build-tagged debug
// tool.
func runFixtureWithParams(t *testing.T, fixture string, p sweepParams) (status, emptyReason string) {
	t.Helper()
	input, _ := loadFixture(t, fixture)
	if len(input) < MinMeetings {
		return StatusDegenerate, EmptyReasonInsufficientMeetings
	}
	// BuildGraph reads MaxMeetingSize + MinMeetingsPerEdge from package
	// consts. The override surface for v1 of the sweep is k +
	// DegenerateRatio. MaxMeetingSize / MinMeetingsPerEdge sweeps
	// require updating the consts in params.go before running.
	g, idx := BuildGraph(input)
	if idx.Size() < MinAttendees {
		return StatusDegenerate, EmptyReasonInsufficientAttendees
	}
	communities, er := clusterWithParams(g, idx, p)
	if er != "" {
		switch er {
		case EmptyReasonZeroCommunities, EmptyReasonDegenerateCluster:
			return StatusDegenerate, er
		case "error":
			return StatusError, ""
		}
	}
	if len(communities) == 0 {
		return StatusDegenerate, EmptyReasonZeroCommunities
	}
	return StatusSuccess, ""
}

// clusterWithParams is a sweep-only variant of ClusterCommunities that
// accepts overridden k + DegenerateRatio. Mirrors cluster.go logic but
// calls community.KCliqueCommunities(p.k(), g) directly so the k
// override actually takes effect.
//
// The //go:build sweep tag at the top of this file guarantees these
// gonum imports only compile under `-tags sweep` — default builds are
// unaffected.
func clusterWithParams(g *simple.WeightedUndirectedGraph, idx *nodeIndex, p sweepParams) (communities [][]string, emptyReason string) {
	defer func() {
		if r := recover(); r != nil {
			communities = nil
			emptyReason = "error"
		}
	}()

	// Call gonum CPM directly with the overridden k value.
	kcomms := community.KCliqueCommunities(p.k(), g)
	if len(kcomms) == 0 {
		return [][]string{}, EmptyReasonZeroCommunities
	}

	// Materialize each gonum community into a sorted []string of emails.
	// Filter size<k singletons (mirrors cluster.go's defensive filter).
	out := make([][]string, 0, len(kcomms))
	for _, c := range kcomms {
		if len(c) < p.k() {
			continue
		}
		emails := make([]string, 0, len(c))
		for _, n := range c {
			if e, ok := idx.Email(n.ID()); ok {
				emails = append(emails, e)
			}
		}
		if len(emails) < p.k() {
			continue
		}
		sort.Strings(emails)
		out = append(out, emails)
	}
	if len(out) == 0 {
		return [][]string{}, EmptyReasonZeroCommunities
	}

	// Sort communities by size desc, then by first email asc (mirrors
	// cluster.go's deterministic ordering).
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i][0] < out[j][0]
	})

	// DegenerateRatio gate with overridden value. Mirrors cluster.go's
	// 2*MinAttendees floor so behavior matches production at the
	// default DegenerateRatio.
	maxSize := 0
	for _, c := range out {
		if len(c) > maxSize {
			maxSize = len(c)
		}
	}
	totalNodes := idx.Size()
	if totalNodes > 2*MinAttendees {
		ratio := float64(maxSize) / float64(totalNodes)
		if ratio > p.degenerateRatio() {
			return [][]string{}, EmptyReasonDegenerateCluster
		}
	}
	return out, ""
}

func TestSweep_AllFixtures(t *testing.T) {
	// Candidate parameter overrides. Production values are LOCKED for
	// v1.1 (CONTEXT.md D-01..D-21); the sweep is a tool for future
	// re-tuning, so the candidate set deliberately spans the production
	// value.
	candidates := []struct {
		name   string
		values []sweepParams
		notes  string
	}{
		{
			name:   "k",
			values: []sweepParams{{K: 2}, {K: 3}, {K: 4}},
			notes:  "CPM clique size. v1.1 locked at 3.",
		},
		{
			name:   "DegenerateRatio",
			values: []sweepParams{{DegenerateRatio: 0.70}, {DegenerateRatio: 0.80}, {DegenerateRatio: 0.90}},
			notes:  "Output gate. v1.1 locked at 0.80.",
		},
	}

	// Header.
	fmt.Println("<!-- SWEEP-TABLE-BEGIN -->")
	fmt.Println("| Parameter | Value | Fixtures passing | Notes |")
	fmt.Println("|-----------|-------|------------------|-------|")

	// Iterate candidates.
	for _, c := range candidates {
		for _, p := range c.values {
			pass := 0
			for _, fixture := range allFixtures {
				gotStatus, _ := runFixtureWithParams(t, fixture, p)
				_, expected := loadFixture(t, fixture)
				if gotStatus == expected.ExpectedStatus {
					pass++
				}
			}
			val := ""
			switch c.name {
			case "k":
				val = fmt.Sprintf("%d", p.K)
			case "DegenerateRatio":
				val = fmt.Sprintf("%.2f", p.DegenerateRatio)
			}
			fmt.Printf("| %s | %s | %d/%d | %s |\n", c.name, val, pass, len(allFixtures), c.notes)
		}
	}

	// Deterministic ordering of fixture names for log readability.
	sorted := make([]string, len(allFixtures))
	copy(sorted, allFixtures)
	sort.Strings(sorted)

	fmt.Println("<!-- SWEEP-TABLE-END -->")
}
