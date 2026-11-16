package teamdetection

import (
	"sort"
	"strconv"
	"time"

	"mia-website/backend/internal/services"

	"gonum.org/v1/gonum/graph/simple"
)

// nodeIndex maps canonical email → gonum int64 node ID and back.
// Returned by BuildGraph so callers can recover emails from cluster
// outputs. Keys are emails verbatim from Phase 5 (already lowercased +
// trimmed at the fetcher boundary — see CONTEXT.md D-03: this package
// does NOT re-canonicalize).
type nodeIndex struct {
	emailToID map[string]int64
	idToEmail map[int64]string
}

// Email returns the email registered for the given gonum node ID.
func (n *nodeIndex) Email(id int64) (string, bool) {
	e, ok := n.idToEmail[id]
	return e, ok
}

// ID returns the gonum node ID registered for the given email.
func (n *nodeIndex) ID(email string) (int64, bool) {
	id, ok := n.emailToID[email]
	return id, ok
}

// Size returns the number of distinct nodes (emails) in the graph.
func (n *nodeIndex) Size() int { return len(n.emailToID) }

// isoWeekKey returns "YYYY-W##" — the canonical distinct-week key used
// by DETECT-05's temporal weighting. Uses the event's StartTime in UTC.
// Two events in the same ISO week share a key; two events in different
// ISO weeks produce different keys.
//
// Pad week to two digits so the string sorts the same as the (year, week)
// integer pair.
func isoWeekKey(t time.Time) string {
	year, week := t.UTC().ISOWeek()
	if week < 10 {
		return strconv.Itoa(year) + "-W0" + strconv.Itoa(week)
	}
	return strconv.Itoa(year) + "-W" + strconv.Itoa(week)
}

// BuildGraph constructs the co-attendance graph from a slice of
// BackfillEvent. CONTEXT.md D-02: edge weight between (a,b) =
//
//	Σ over (recurring-series, distinct-week) pairs that include both a and b:
//	    rawWeight(meeting) where rawWeight(m) = 1 / (m.attendee_count - 1)
//
// The distinct-week temporal weighting (DETECT-05) is applied as: within
// the SAME RecurringEventID bucket, only ONE meeting per ISO-week
// contributes (the first one encountered, after sorted iteration so the
// choice is deterministic). For ad-hoc meetings (empty RecurringEventID),
// each meeting gets its own bucket so every meeting contributes (it is
// already a "distinct event" by D-11 convention).
//
// Filters applied (in order):
//  1. Drop events with attendee_count > MaxMeetingSize (DETECT-03 — P2 mitigation)
//  2. Drop events with attendee_count < 2 (no pairs to form)
//  3. After accumulation, drop edges with coMeetingCount < MinMeetingsPerEdge
//     (P2 interview-panel mitigation)
//
// Determinism (CONTEXT.md D-03): events are sorted by (StartTime, EventID);
// per-event attendees are sorted by Email; final pair iteration is
// sorted by (low, high). No map iteration that produces graph state
// happens before a sort.
//
// Returns the gonum graph + the email↔node-ID index. The graph's
// self-loop and absent-edge weights are 0 (no self-attendance, missing
// edges = no co-attendance).
func BuildGraph(events []services.BackfillEvent) (*simple.WeightedUndirectedGraph, *nodeIndex) {
	g := simple.NewWeightedUndirectedGraph(0, 0)
	idx := &nodeIndex{
		emailToID: make(map[string]int64),
		idToEmail: make(map[int64]string),
	}

	// Step 1: filter events; accumulate per-pair contributions keyed by
	// (low, high, recurringBucket). Within each (pair, recurringBucket)
	// we only keep one event per ISO-week (distinct-week dedup).
	type pairKey struct {
		low, high, recurringBucket string
	}
	type weekDedup map[string]float64 // week -> rawWeight chosen
	contribs := make(map[pairKey]weekDedup)
	pairMeetingCount := make(map[[2]string]int) // (low, high) -> distinct meeting count for MinMeetingsPerEdge gate

	// Deterministic iteration: sort events by StartTime then EventID
	// (CONTEXT.md D-03 sorted iteration mandate).
	sortedEvents := make([]services.BackfillEvent, len(events))
	copy(sortedEvents, events)
	sort.Slice(sortedEvents, func(i, j int) bool {
		if !sortedEvents[i].StartTime.Equal(sortedEvents[j].StartTime) {
			return sortedEvents[i].StartTime.Before(sortedEvents[j].StartTime)
		}
		return sortedEvents[i].EventID < sortedEvents[j].EventID
	})

	for _, ev := range sortedEvents {
		n := len(ev.Attendees)
		if n > MaxMeetingSize {
			continue // DETECT-03 — drop all-hands clique
		}
		if n < 2 {
			continue // no pairs
		}
		raw := 1.0 / float64(n-1)
		bucket := ev.RecurringEventID
		if bucket == "" {
			// Ad-hoc meetings: every event is its own "series" (D-11). Use
			// EventID as a unique bucket so distinct-week dedup degenerates
			// to "include this meeting".
			bucket = "evt:" + ev.EventID
		}
		week := isoWeekKey(ev.StartTime)

		// Sort attendees by Email (D-03) for deterministic pair generation.
		emails := make([]string, n)
		for i, a := range ev.Attendees {
			emails[i] = a.Email
		}
		sort.Strings(emails)

		// Track which (low, high) pairs gained an instance from THIS event
		// for the MinMeetingsPerEdge gate.
		seenPairThisEvent := make(map[[2]string]bool)

		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				low, high := emails[i], emails[j] // already sorted
				pk := pairKey{low: low, high: high, recurringBucket: bucket}

				wd, ok := contribs[pk]
				if !ok {
					wd = make(weekDedup)
					contribs[pk] = wd
				}
				if _, exists := wd[week]; !exists {
					// First event in this (pair, series, week) bucket — count it.
					wd[week] = raw
				}
				// (Subsequent events in the same week/series/pair are dropped
				// — distinct-week temporal weighting per DETECT-05.)

				// Track distinct meeting count for the edge-frequency gate.
				if !seenPairThisEvent[[2]string{low, high}] {
					pairMeetingCount[[2]string{low, high}]++
					seenPairThisEvent[[2]string{low, high}] = true
				}
			}
		}
	}

	// Step 2: collapse contribs → final edge weights. Apply
	// MinMeetingsPerEdge gate.
	finalWeights := make(map[[2]string]float64)
	for pk, wd := range contribs {
		mc := pairMeetingCount[[2]string{pk.low, pk.high}]
		if mc < MinMeetingsPerEdge {
			continue // P2 interview-panel mitigation
		}
		var sum float64
		for _, w := range wd {
			sum += w
		}
		finalWeights[[2]string{pk.low, pk.high}] += sum
	}

	// Step 3: deterministically register nodes + edges. Iterate keys in
	// sorted order so node-ID assignment is deterministic across runs.
	type pair struct {
		low, high string
		weight    float64
	}
	pairs := make([]pair, 0, len(finalWeights))
	for k, w := range finalWeights {
		pairs = append(pairs, pair{low: k[0], high: k[1], weight: w})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].low != pairs[j].low {
			return pairs[i].low < pairs[j].low
		}
		return pairs[i].high < pairs[j].high
	})

	ensureNode := func(email string) int64 {
		if id, ok := idx.emailToID[email]; ok {
			return id
		}
		n := g.NewNode()
		g.AddNode(n)
		idx.emailToID[email] = n.ID()
		idx.idToEmail[n.ID()] = email
		return n.ID()
	}

	for _, p := range pairs {
		aID := ensureNode(p.low)
		bID := ensureNode(p.high)
		g.SetWeightedEdge(g.NewWeightedEdge(
			simple.Node(aID),
			simple.Node(bID),
			p.weight,
		))
	}

	return g, idx
}
