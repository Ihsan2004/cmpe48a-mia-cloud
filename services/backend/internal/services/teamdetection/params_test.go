package teamdetection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshot_Deterministic(t *testing.T) {
	first := SnapshotParameters()
	for i := 0; i < 100; i++ {
		got := SnapshotParameters()
		if !bytes.Equal(first, got) {
			t.Fatalf("SnapshotParameters not deterministic at iter %d (D-20)", i)
		}
	}
}

func TestSnapshot_KeysCovered(t *testing.T) {
	// Verify every const in params.go is represented in the snapshot —
	// guards against "added a new const, forgot to update SnapshotParameters".
	var m map[string]any
	if err := json.Unmarshal(SnapshotParameters(), &m); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	expectedKeys := []string{
		"algorithm_version",
		"k",
		"max_meeting_size",
		"min_meetings",
		"min_attendees",
		"min_meetings_per_edge",
		"degenerate_ratio",
		"confidence_weight_median",
		"confidence_weight_density",
		"confidence_median_norm_const",
		"confidence_threshold_high",
		"confidence_threshold_mid",
	}
	for _, k := range expectedKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("snapshot missing key %q (params.go added a const without updating SnapshotParameters)", k)
		}
	}
}

func TestSnapshot_SizeUnder1KB(t *testing.T) {
	// T-06-03 defensive ceiling — parameters_jsonb should be < 1KB
	// even as the const list grows (the schema's NULL JSONB column has
	// no hard ceiling, but we don't want runaway entries either).
	s := SnapshotParameters()
	if len(s) > 1024 {
		t.Fatalf("snapshot size = %d bytes, want < 1024 (T-06-03)", len(s))
	}
}

func TestAlgorithmVersion_IsExpectedConstant(t *testing.T) {
	if got := AlgorithmVersion(); got != "cpm-k3-v2" {
		t.Errorf("AlgorithmVersion = %q, want %q (D-18)", got, "cpm-k3-v2")
	}
}

func TestSnapshot_StringDoesNotLeakEmails(t *testing.T) {
	// T-06-01 belt-and-braces: the parameters snapshot is built from
	// const primitives only; no PII should ever appear in it.
	s := string(SnapshotParameters())
	if strings.Contains(s, "@") {
		t.Fatalf("snapshot contains @ character — possible PII leak: %s", s)
	}
}
