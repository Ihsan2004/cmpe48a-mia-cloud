package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestTeamDetectionRunsTotal_Registered(t *testing.T) {
	// Increment once for each terminal state — verifies the metric is
	// queryable + the label set is correct (extra labels would panic).
	before := testutil.ToFloat64(TeamDetectionRunsTotal.WithLabelValues(StateSuccess))
	for _, s := range []string{StateSuccess, StateDegenerate, StateError} {
		TeamDetectionRunsTotal.WithLabelValues(s).Inc()
	}
	after := testutil.ToFloat64(TeamDetectionRunsTotal.WithLabelValues(StateSuccess))
	if after-before < 1 {
		t.Fatalf("StateSuccess counter not incremented: before=%f after=%f", before, after)
	}
}

func TestTeamDetectionDurationSeconds_Registered(t *testing.T) {
	// Observe one duration per stage.
	stages := []string{StageFetch, StageGraphBuild, StageCluster, StageScore, StagePersist}
	for _, s := range stages {
		TeamDetectionDurationSeconds.WithLabelValues(s).Observe(0.05)
	}
	// Sanity: 5 stages × 1 observation = 5 in CollectAndCount.
	if n := testutil.CollectAndCount(TeamDetectionDurationSeconds); n < len(stages) {
		t.Fatalf("CollectAndCount = %d, want >= %d", n, len(stages))
	}
}

func TestStageLabels_LiteralValues(t *testing.T) {
	cases := map[string]string{
		"StageFetch":      "fetch",
		"StageGraphBuild": "graph_build",
		"StageCluster":    "cluster",
		"StageScore":      "score",
		"StagePersist":    "persist",
	}
	got := map[string]string{
		"StageFetch":      StageFetch,
		"StageGraphBuild": StageGraphBuild,
		"StageCluster":    StageCluster,
		"StageScore":      StageScore,
		"StagePersist":    StagePersist,
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("%s = %q, want %q (CONTEXT.md D-15)", k, got[k], want)
		}
	}
}

func TestStateLabels_LiteralValues(t *testing.T) {
	cases := map[string]string{
		"StateSuccess":    "success",
		"StateDegenerate": "degenerate",
		"StateError":      "error",
	}
	got := map[string]string{
		"StateSuccess":    StateSuccess,
		"StateDegenerate": StateDegenerate,
		"StateError":      StateError,
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("%s = %q, want %q (CONTEXT.md D-16)", k, got[k], want)
		}
	}
}
