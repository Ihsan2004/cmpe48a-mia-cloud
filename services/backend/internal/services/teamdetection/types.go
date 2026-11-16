// Package teamdetection implements the v1.1 milestone's classic-first
// team-detection algorithm. The package consumes Phase 5's BackfillEvent
// slice, builds a co-attendance graph (graph.go), runs gonum's CPM
// k-clique communities at k=3 (cluster.go), scores each surviving
// community (score.go), routes degenerate inputs/outputs to the
// empty-state path, and writes one team_detection_runs row per
// invocation (service.go).
//
// Package-shape precedent: briefnotify/. Sub-files per concern, *_test.go
// siblings, no central god-file.
//
// Algorithm choice + parameters were tuned against the eight hand-labeled
// JSON fixtures under testdata/ (CONTEXT.md D-05). Re-tuning means
// re-running the build-tagged sweep test (`go test -tags sweep`) and
// pasting the new value table into params.go's comment block.
//
// What this package does NOT do:
//   - it does NOT write to teams or team_members (Phase 7 owns the save path)
//   - it does NOT re-normalize attendee emails (Phase 5 D-12 already did)
//   - it does NOT call Google Calendar — it consumes the BackfillFetcher seam
//   - it does NOT run a Louvain fallback — deferred to v1.2 per D-21
package teamdetection

import (
	"context"

	"github.com/google/uuid"
)

// DetectionResult is the DTO returned by Detector.Detect. CONTEXT.md D-08
// — field order + types are LOCKED. Phase 7's onboarding handler consumes
// this verbatim; changing the shape breaks the API contract.
//
// Status semantics (D-14):
//   - "success": Teams != []; EmptyReason == ""
//   - "degenerate": Teams == []; EmptyReason is one of the four
//     EmptyReason* constants (the column team_detection_runs.error_message
//     also carries the empty_reason string via Phase 6 convention — see
//     repository/team.go::UpdateDetectionRunFinish doc-comment, plan 06-03)
//   - "error": Teams == []; EmptyReason == ""; the underlying error is
//     returned alongside the DetectionResult (or *DetectionResult may be
//     nil — handler must tolerate both)
type DetectionResult struct {
	Status      string         `json:"status"`
	EmptyReason string         `json:"empty_reason"`
	Teams       []DetectedTeam `json:"teams"`
	RunID       uuid.UUID      `json:"run_id"`
}

// DetectedTeam is one suggested team. Phase 7 maps ConfidenceBucket to
// the TR copy Yüksek/Orta/Düşük. TopSeries titles are RAW (never
// normalized, never stopword-stripped) so the "Neden bu takım?" disclosure
// can render exactly what the user saw in their calendar.
type DetectedTeam struct {
	SuggestedName    string           `json:"suggested_name"`
	ConfidenceBucket string           `json:"confidence_bucket"`
	ConfidenceScore  float64          `json:"confidence_score"`
	TopSeries        []string         `json:"top_series"`
	Members          []DetectedMember `json:"members"`
}

// DetectedMember is one team member with TRUST-01 data prep: the count of
// meetings this member co-attended with at least one OTHER member of
// THIS team. Plan 06-02 implements the count; plan 06-03 wires the chip
// data through; Phase 7 renders "X toplantı".
type DetectedMember struct {
	Email              string `json:"email"`
	SharedMeetingCount int    `json:"shared_meeting_count"`
}

// Detector is the one-method seam Phase 7's handler uses to mock the
// service in unit tests. Production wires *Service (plan 06-03).
type Detector interface {
	Detect(ctx context.Context, userID uuid.UUID, userEmail string) (*DetectionResult, error)
}

// Status constants — values mirror team_detection_runs.status CHECK
// constraint in sql/migrations/017_team_detection_runs.sql.
const (
	StatusRunning    = "running"
	StatusSuccess    = "success"
	StatusDegenerate = "degenerate"
	StatusError      = "error"
)

// EmptyReason constants (CONTEXT.md D-13). The four reasons a run
// returns no teams. The first two fire BEFORE the algorithm runs
// (data-quality gate); the last two fire AFTER the algorithm runs
// (degenerate-output gate).
const (
	EmptyReasonInsufficientMeetings  = "insufficient_meetings"
	EmptyReasonInsufficientAttendees = "insufficient_attendees"
	EmptyReasonZeroCommunities       = "zero_communities"
	EmptyReasonDegenerateCluster     = "degenerate_cluster"
)
