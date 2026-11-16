package metrics

// Phase 6 (v1.1 team detection) Prometheus collectors. Mirrors the
// brief_delivery_* precedent: package-level vars registered against
// DefaultRegisterer via promauto so any handler hitting promhttp.Handler()
// at /metrics will surface them automatically.
//
// Metric names + label values are LOCKED by CONTEXT.md D-15 and D-16 —
// Phase 7's dashboards and alert rules will key off these strings.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Stage label values for TeamDetectionDurationSeconds. CONTEXT.md D-15 —
// five stages, no "total" label (Prometheus consumers sum across stages).
const (
	StageFetch      = "fetch"
	StageGraphBuild = "graph_build"
	StageCluster    = "cluster"
	StageScore      = "score"
	StagePersist    = "persist"
)

// State label values for TeamDetectionRunsTotal. CONTEXT.md D-16 — only
// the three terminal statuses are counted (the transient "running" state
// is not — it's incremented to a terminal value before the metric
// records once).
const (
	StateSuccess    = "success"
	StateDegenerate = "degenerate"
	StateError      = "error"
)

// TeamDetectionRunsTotal — OBS-02 counter. Incremented exactly once per
// Service.Detect call by service.go (in the defer that fires after
// FinalizeDetectionRun). Label "state" carries the terminal status.
var TeamDetectionRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "team_detection_runs_total",
	Help: "Team-detection runs by terminal state. State in {success, degenerate, error}; see CONTEXT.md D-16.",
}, []string{"state"})

// TeamDetectionDurationSeconds — OBS-02 histogram. Observes one duration
// per stage per Service.Detect call. CONTEXT.md D-15 stages:
// fetch / graph_build / cluster / score / persist.
//
// Bucket choice rationale: the algorithm itself (graph + cluster + score)
// is sub-second for any realistic v1.1 user (< 200 events, < 50 distinct
// attendees). Fetch is the long pole — Phase 5's p95 is ~80ms but
// degraded Google response can push 5-15s (Phase 5 success criterion 5
// budget). Persist is two simple DB writes — sub-100ms. Hand-picked
// buckets cover all stages with reasonable resolution.
var TeamDetectionDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "team_detection_duration_seconds",
	Help:    "Per-stage wall-clock duration of a team detection run. Stage in {fetch, graph_build, cluster, score, persist}; see CONTEXT.md D-15.",
	Buckets: []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
}, []string{"stage"})
