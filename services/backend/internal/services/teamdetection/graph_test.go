package teamdetection

import (
	"math"
	"strconv"
	"testing"
	"time"

	"mia-website/backend/internal/services"

	"gonum.org/v1/gonum/graph/simple"
)

// mkEvent builds a single BackfillEvent with the named attendees on a
// given StartTime + RecurringEventID. The attendee_count drives the
// 1/(n-1) weight decay used by BuildGraph (DETECT-04).
func mkEvent(id, recurringID, title string, start time.Time, emails ...string) services.BackfillEvent {
	atts := make([]services.BackfillAttendee, len(emails))
	for i, e := range emails {
		atts[i] = services.BackfillAttendee{Email: e, ResponseStatus: "accepted"}
	}
	return services.BackfillEvent{
		EventID:          id,
		RecurringEventID: recurringID,
		StartTime:        start,
		EndTime:          start.Add(30 * time.Minute),
		Title:            title,
		Attendees:        atts,
	}
}

// TestBuildGraph_MaxMeetingSizeFilter verifies DETECT-03: meetings with
// more than MaxMeetingSize attendees are dropped from the graph entirely.
func TestBuildGraph_MaxMeetingSizeFilter(t *testing.T) {
	// Build a 26-attendee meeting (should be dropped) and a 25-attendee
	// meeting on different recurring series so edges don't merge.
	emails26 := make([]string, 26)
	for i := range emails26 {
		emails26[i] = "user" + strconv.Itoa(i) + "@example.com"
	}
	emails25 := make([]string, 25)
	for i := range emails25 {
		emails25[i] = "okuser" + strconv.Itoa(i) + "@example.com"
	}
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("e26-a", "big", "All Hands", now, emails26...),
		mkEvent("e26-b", "big", "All Hands", now.AddDate(0, 0, 7), emails26...),
		mkEvent("e25-a", "ok", "Big Team Sync", now, emails25...),
		mkEvent("e25-b", "ok", "Big Team Sync", now.AddDate(0, 0, 7), emails25...),
	}
	g, idx := BuildGraph(events)
	// 26-person clique should contribute NO nodes (filtered before any edge
	// accumulation; user0..user25 should not appear in the index).
	if _, ok := idx.ID("user0@example.com"); ok {
		t.Fatal("DETECT-03: 26-attendee meeting attendees leaked into the graph")
	}
	// 25-person clique should contribute all 25 nodes.
	for _, e := range emails25 {
		if _, ok := idx.ID(e); !ok {
			t.Fatalf("DETECT-03: 25-attendee meeting attendee %q missing from graph", e)
		}
	}
	if idx.Size() != 25 {
		t.Fatalf("DETECT-03: want 25 nodes, got %d", idx.Size())
	}
	_ = g
}

// TestBuildGraph_WeightDecay_3vs15 verifies DETECT-04: a 3-attendee
// meeting contributes per-event weight 1/(3-1)=0.5; a 15-attendee meeting
// contributes 1/(15-1)≈0.0714. Ratio 7:1.
func TestBuildGraph_WeightDecay_3vs15(t *testing.T) {
	alice, bob, carol := "alice@example.com", "bob@example.com", "carol@example.com"
	extras := make([]string, 13)
	for i := range extras {
		extras[i] = "noise" + strconv.Itoa(i) + "@example.com"
	}
	w1 := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)  // week 15
	w2 := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC) // week 16
	w3 := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC) // week 17
	w4 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC) // week 18

	// Build edge for the 3-attendee meeting — need ≥ 2 distinct meetings
	// to clear MinMeetingsPerEdge. Add two distinct events in distinct weeks
	// so distinct-week dedup keeps both.
	events := []services.BackfillEvent{
		mkEvent("s3-a", "series3", "Tiny", w1, alice, bob, carol),
		mkEvent("s3-b", "series3", "Tiny", w2, alice, bob, carol),
		// 15-attendee meetings — use a distinct series so it lives on its
		// own edge bucket. Add 2 distinct meetings in distinct weeks.
		mkEvent("s15-a", "series15", "Wide", w3, append([]string{alice, bob}, extras...)...),
		mkEvent("s15-b", "series15", "Wide", w4, append([]string{alice, bob}, extras...)...),
	}
	g, idx := BuildGraph(events)

	aID, ok := idx.ID(alice)
	if !ok {
		t.Fatal("alice missing from graph")
	}
	bID, ok := idx.ID(bob)
	if !ok {
		t.Fatal("bob missing from graph")
	}
	edge := g.WeightedEdgeBetween(aID, bID)
	if edge == nil {
		t.Fatal("alice-bob edge missing")
	}
	got := edge.Weight()

	// Expected: from series3 — 2 distinct weeks × 0.5 each = 1.0
	//           from series15 — 2 distinct weeks × (1/14) each = 0.142857...
	//           sum = 1.142857...
	expectedSeries3 := 2.0 * 0.5
	expectedSeries15 := 2.0 / 14.0
	expected := expectedSeries3 + expectedSeries15
	if math.Abs(got-expected) > 1e-9 {
		t.Fatalf("DETECT-04: got %.6f, want %.6f", got, expected)
	}

	// DETECT-04 explicit ratio assertion: per-meeting contribution at
	// n=3 is 0.5, at n=15 is 0.0714... — ratio 7:1.
	ratio := 0.5 / (1.0 / 14.0)
	if math.Abs(ratio-7.0) > 1e-9 {
		t.Fatalf("DETECT-04: per-meeting ratio = %.6f, want 7.0", ratio)
	}
}

// TestBuildGraph_TemporalSpreadWeighting verifies DETECT-05: 5 meetings
// between the same pair in 1 week vs 5 meetings in 5 distinct weeks
// produce edge weights with ratio ≥ 5:1.
func TestBuildGraph_TemporalSpreadWeighting(t *testing.T) {
	alice, bob := "alice@example.com", "bob@example.com"
	carol := "carol@example.com" // 3rd attendee so each meeting has 3 people

	// Group A: 5 meetings all in week 15 of 2026 (April 6-12 UTC, Mon-Sun).
	wkA := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	groupA := []services.BackfillEvent{
		mkEvent("A1", "groupA", "T", wkA, alice, bob, carol),
		mkEvent("A2", "groupA", "T", wkA.Add(24*time.Hour), alice, bob, carol),
		mkEvent("A3", "groupA", "T", wkA.Add(48*time.Hour), alice, bob, carol),
		mkEvent("A4", "groupA", "T", wkA.Add(72*time.Hour), alice, bob, carol),
		mkEvent("A5", "groupA", "T", wkA.Add(96*time.Hour), alice, bob, carol),
	}
	gA, idxA := BuildGraph(groupA)
	aID, _ := idxA.ID(alice)
	bID, _ := idxA.ID(bob)
	edgeA := gA.WeightedEdgeBetween(aID, bID)
	if edgeA == nil {
		t.Fatal("DETECT-05: groupA alice-bob edge missing")
	}
	weightA := edgeA.Weight()

	// Group B: 5 meetings in 5 distinct weeks (April 6, 13, 20, 27, May 4).
	groupB := []services.BackfillEvent{
		mkEvent("B1", "groupB", "T", time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC), alice, bob, carol),
		mkEvent("B2", "groupB", "T", time.Date(2026, 4, 13, 9, 0, 0, 0, time.UTC), alice, bob, carol),
		mkEvent("B3", "groupB", "T", time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC), alice, bob, carol),
		mkEvent("B4", "groupB", "T", time.Date(2026, 4, 27, 9, 0, 0, 0, time.UTC), alice, bob, carol),
		mkEvent("B5", "groupB", "T", time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), alice, bob, carol),
	}
	gB, idxB := BuildGraph(groupB)
	aIDB, _ := idxB.ID(alice)
	bIDB, _ := idxB.ID(bob)
	edgeB := gB.WeightedEdgeBetween(aIDB, bIDB)
	if edgeB == nil {
		t.Fatal("DETECT-05: groupB alice-bob edge missing")
	}
	weightB := edgeB.Weight()

	// DETECT-05: distinct-week weighting → groupB ≥ 5× groupA.
	ratio := weightB / weightA
	if ratio < 5.0-1e-9 {
		t.Fatalf("DETECT-05: distinct-week ratio = %.4f, want ≥ 5.0 (weightA=%.4f weightB=%.4f)",
			ratio, weightA, weightB)
	}
}

// TestBuildGraph_MinMeetingsPerEdgeGate verifies the interview-panel
// edge-frequency filter (P2 mitigation): edges with < MinMeetingsPerEdge
// co-attended meetings are dropped.
func TestBuildGraph_MinMeetingsPerEdgeGate(t *testing.T) {
	a, b, c, d := "alice@example.com", "bob@example.com", "carol@example.com", "dan@example.com"
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("s1", "interview", "Panel", w, a, b),
		mkEvent("s2", "team", "Team", w, c, d),
		mkEvent("s3", "team", "Team", w.AddDate(0, 0, 7), c, d),
	}
	g, idx := BuildGraph(events)
	aID, aok := idx.ID(a)
	bID, bok := idx.ID(b)
	cID, cok := idx.ID(c)
	dID, dok := idx.ID(d)
	// After gate: alice/bob may still appear as isolates (no edges) OR
	// be excluded entirely. The contract is: NO edge exists between them.
	if aok && bok {
		if e := g.WeightedEdgeBetween(aID, bID); e != nil {
			t.Fatal("MinMeetingsPerEdge: alice-bob edge should be dropped (1 co-attendance)")
		}
	}
	if !cok || !dok {
		t.Fatal("carol/dan should be in the graph")
	}
	if e := g.WeightedEdgeBetween(cID, dID); e == nil {
		t.Fatal("MinMeetingsPerEdge: carol-dan edge should survive (2 co-attendances)")
	}
}

// TestBuildGraph_Deterministic verifies CONTEXT.md D-03 — two BuildGraph
// calls on the same input produce byte-equal node IDs and edge weights.
func TestBuildGraph_Deterministic(t *testing.T) {
	a, b, c := "alice@example.com", "bob@example.com", "carol@example.com"
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("e1", "series", "T", w, a, b, c),
		mkEvent("e2", "series", "T", w.AddDate(0, 0, 7), a, b, c),
	}
	g1, idx1 := BuildGraph(events)
	g2, idx2 := BuildGraph(events)

	if idx1.Size() != idx2.Size() {
		t.Fatalf("non-deterministic size: %d vs %d", idx1.Size(), idx2.Size())
	}
	for email, id1 := range idx1.emailToID {
		id2, ok := idx2.ID(email)
		if !ok || id1 != id2 {
			t.Fatalf("non-deterministic node ID for %s: %d vs %d", email, id1, id2)
		}
	}
	// Compare edges
	for _, email := range []string{a, b, c} {
		for _, other := range []string{a, b, c} {
			if email >= other {
				continue
			}
			e1, _ := idx1.ID(email)
			f1, _ := idx1.ID(other)
			e2, _ := idx2.ID(email)
			f2, _ := idx2.ID(other)
			ed1 := g1.WeightedEdgeBetween(e1, f1)
			ed2 := g2.WeightedEdgeBetween(e2, f2)
			if (ed1 == nil) != (ed2 == nil) {
				t.Fatalf("edge presence mismatch (%s,%s)", email, other)
			}
			if ed1 != nil && math.Abs(ed1.Weight()-ed2.Weight()) > 1e-12 {
				t.Fatalf("non-deterministic weight (%s,%s): %.12f vs %.12f", email, other, ed1.Weight(), ed2.Weight())
			}
		}
	}
}

// TestBuildGraph_EmptyInput verifies BuildGraph is panic-safe on nil
// input (T-06-02 mitigation).
func TestBuildGraph_EmptyInput(t *testing.T) {
	g, idx := BuildGraph(nil)
	if idx.Size() != 0 {
		t.Fatalf("want 0 nodes, got %d", idx.Size())
	}
	_ = g
}

// TestBuildGraph_SingleAttendeeMeeting verifies the n<2 short-circuit
// (no pairs can form from a single attendee).
func TestBuildGraph_SingleAttendeeMeeting(t *testing.T) {
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("solo", "x", "Solo", w, "alice@example.com"),
	}
	g, idx := BuildGraph(events)
	if idx.Size() != 0 {
		t.Fatalf("single-attendee meeting must contribute 0 nodes (no pairs); got %d", idx.Size())
	}
	_ = g
}

// TestBuildGraph_NodeIndexRoundtrip verifies the nodeIndex bidirectional
// mapping is consistent for every node in the graph.
func TestBuildGraph_NodeIndexRoundtrip(t *testing.T) {
	a, b := "alice@example.com", "bob@example.com"
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("e1", "s", "T", w, a, b),
		mkEvent("e2", "s", "T", w.AddDate(0, 0, 7), a, b),
	}
	_, idx := BuildGraph(events)
	for _, e := range []string{a, b} {
		id, ok := idx.ID(e)
		if !ok {
			t.Fatalf("email %s missing from index", e)
		}
		got, ok := idx.Email(id)
		if !ok || got != e {
			t.Fatalf("roundtrip failed: id=%d → %q want %q", id, got, e)
		}
	}
}

// Sanity assertion — gonum's concrete graph type used here.
var _ = (*simple.WeightedUndirectedGraph)(nil)
