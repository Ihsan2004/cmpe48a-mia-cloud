package teamdetection

import (
	"encoding/json"
	"fmt"
)

// algorithmVersion is the snapshot tag persisted to
// team_detection_runs.algorithm_version. Bump on any const change in
// THIS file. CONTEXT.md D-18. Format: "<algo>-k<k>-v<rev>".
//
// v2 (2026-05-13): lowered MinAttendees from 5→3 to accept tight
// small-team scenarios (4-person daily-standup K4 was being rejected
// at the data-quality gate even though it was the textbook positive
// signal). Cluster-stage k=3 CPM + the existing
// totalNodes > 2*MinAttendees DegenerateRatio escape together still
// reject 1:1-only graphs (no triangles → zero_communities) and
// all-hands cliques (large dominant cluster). See debug session
// teams-detect-degenerate.md for the diagnostic that surfaced this.
const algorithmVersion = "cpm-k3-v2"

// k is the CPM clique size. CONTEXT.md D-01: locked at 3 by research
// SUMMARY's RESOLVED TENSION A. 2 produces overlapping spaghetti
// communities (every triangle = a community); 4+ requires denser
// co-attendance than typical real calendars provide.
// Sweep range explored: 2-5. See params_sweep_test.go output (plan 06-03).
const k = 3

// MaxMeetingSize — meetings with more than this many attendees are
// dropped from the graph entirely. CONTEXT.md D-02 + D-03 + DETECT-03.
// Driven by all_hands_plus_small.json (50-person all-hands must not
// pollute small-team detection) and interview_panel_noise.json (typical
// interview panels run 5-8 interviewers, well under 25, so the cutoff
// doesn't lose legitimate signal). Sweep range explored: 15-40.
const MaxMeetingSize = 25

// MinMeetings — data-quality gate (CONTEXT.md D-13). When the input
// (BEFORE the MaxMeetingSize filter — see Claude's Discretion note in
// CONTEXT) has fewer than this many meetings, route to empty-state with
// EmptyReasonInsufficientMeetings. Driven by cold_start.json (3 meetings
// must trip this gate). Sweep range explored: 5-15.
const MinMeetings = 10

// MinAttendees — data-quality gate (D-13). After graph construction,
// when the graph has fewer than this many distinct nodes, route to
// empty-state with EmptyReasonInsufficientAttendees. Lowered to 3
// in algorithm v2 (2026-05-13) — the original value of 5 rejected
// legitimate 4-person daily-standup teams (a textbook K4 of co-attending
// teammates). With a 3-node floor, 1:1-only graphs still degenerate
// via cluster.go's zero_communities path (a tree has no triangles, so
// CPM k=3 returns no communities); and the
// totalNodes > 2*MinAttendees DegenerateRatio escape still fires for
// large dominant components. Sweep range originally explored: 3-8.
const MinAttendees = 3

// MinMeetingsPerEdge — edges with fewer than this many co-attended
// meetings are dropped from the graph. CONTEXT.md D-02. Driven by
// interview_panel_noise.json (interview-panel pairs co-attend exactly
// once, so the gate filters them out). P2 mitigation.
// Sweep range explored: 1-4.
const MinMeetingsPerEdge = 2

// DegenerateRatio — when the largest community covers more than this
// fraction of the graph's nodes, route to empty-state with
// EmptyReasonDegenerateCluster. CONTEXT.md D-13 + DETECT-07. Driven by
// single_large_component.json (18 people all in same 12 meetings → 100%
// in one cluster → trip this gate) and boundary_78pct.json (78% must
// PASS the gate, proving the gate fires only when it should).
// Sweep range explored: 0.70-0.90.
const DegenerateRatio = 0.80

// Confidence scoring formula (CONTEXT.md D-09):
//
//	norm_median = min(1.0, median_shared_meeting_count / ConfidenceMedianNormConst)
//	density     = sum_of_intra_cluster_edge_weights / max_possible_for_node_count
//	score       = ConfidenceWeightMedian * norm_median + ConfidenceWeightDensity * density
//
// Bucket: score >= ConfidenceThresholdHigh → "high"
//
//	score >= ConfidenceThresholdMid  → "mid"
//	else                              → "low"
//
// Starting values from CONTEXT.md D-09; sweep-tuned against
// small_dense.json (must produce "high") + multi_team_overlap.json
// (must produce ≥ "mid" for both clusters).
const (
	ConfidenceWeightMedian    = 0.7
	ConfidenceWeightDensity   = 0.3
	ConfidenceMedianNormConst = 10
	ConfidenceThresholdHigh   = 0.65
	ConfidenceThresholdMid    = 0.35
)

// SnapshotParameters returns the deterministic JSON snapshot persisted
// to team_detection_runs.parameters_jsonb. CONTEXT.md D-18 + D-20.
//
// IMPORTANT: this function is hand-written (not reflection-based) so
// adding a new const requires explicitly updating this map. A unit test
// (params_test.go in plan 06-03) verifies the snapshot keys match the
// expected set so a forgotten update fails CI.
//
// The map is encoded with sorted keys (json.Marshal does NOT sort by
// default for maps with string keys — Go 1.12+ DOES sort string-keyed
// maps in JSON output). The function returns the encoded bytes ready
// for json.RawMessage assignment.
func SnapshotParameters() json.RawMessage {
	m := map[string]any{
		"algorithm_version":            algorithmVersion,
		"k":                            k,
		"max_meeting_size":             MaxMeetingSize,
		"min_meetings":                 MinMeetings,
		"min_attendees":                MinAttendees,
		"min_meetings_per_edge":        MinMeetingsPerEdge,
		"degenerate_ratio":             DegenerateRatio,
		"confidence_weight_median":     ConfidenceWeightMedian,
		"confidence_weight_density":    ConfidenceWeightDensity,
		"confidence_median_norm_const": ConfidenceMedianNormConst,
		"confidence_threshold_high":    ConfidenceThresholdHigh,
		"confidence_threshold_mid":     ConfidenceThresholdMid,
	}
	b, err := json.Marshal(m)
	if err != nil {
		// json.Marshal on a map of primitives cannot fail; if it does,
		// the program is in an impossible state and panicking is
		// preferable to silently writing an empty parameters_jsonb.
		panic(fmt.Sprintf("SnapshotParameters: json.Marshal failed: %v", err))
	}
	return json.RawMessage(b)
}

// AlgorithmVersion is the exported accessor for algorithmVersion.
// Plan 06-03's Service.Detect writes this into
// team_detection_runs.algorithm_version.
func AlgorithmVersion() string { return algorithmVersion }
