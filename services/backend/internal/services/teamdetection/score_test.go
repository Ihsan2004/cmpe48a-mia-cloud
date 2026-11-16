package teamdetection

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/services"
)

func TestScore_ConfidenceFormula_SmallDense(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	g, idx := BuildGraph(input)
	comms, reason := ClusterCommunities(g, idx)
	if reason != "" {
		t.Fatalf("small_dense: cluster should succeed, got reason=%q", reason)
	}
	teams := ScoreCommunities(comms, input, g, idx)
	if len(teams) == 0 {
		t.Fatalf("want ≥ 1 team")
	}
	if teams[0].ConfidenceScore < ConfidenceThresholdHigh {
		t.Fatalf("small_dense: want score ≥ %.2f (high), got %.4f",
			ConfidenceThresholdHigh, teams[0].ConfidenceScore)
	}
	if teams[0].ConfidenceBucket != "high" {
		t.Fatalf("small_dense: want bucket high, got %q", teams[0].ConfidenceBucket)
	}
}

func TestScore_ConfidenceScore_InRange(t *testing.T) {
	for _, fixture := range []string{"small_dense", "multi_team_overlap", "interview_panel_noise", "all_hands_plus_small"} {
		t.Run(fixture, func(t *testing.T) {
			input, _ := loadFixture(t, fixture)
			g, idx := BuildGraph(input)
			comms, reason := ClusterCommunities(g, idx)
			if reason != "" {
				t.Skipf("fixture %s: cluster returned reason=%q", fixture, reason)
			}
			teams := ScoreCommunities(comms, input, g, idx)
			for i, team := range teams {
				if team.ConfidenceScore < 0 || team.ConfidenceScore > 1 {
					t.Fatalf("%s team[%d]: ConfidenceScore = %.4f out of [0,1]", fixture, i, team.ConfidenceScore)
				}
				if team.ConfidenceBucket != "high" && team.ConfidenceBucket != "mid" && team.ConfidenceBucket != "low" {
					t.Fatalf("%s team[%d]: invalid bucket %q", fixture, i, team.ConfidenceBucket)
				}
			}
		})
	}
}

func TestScore_ConfidenceBucket_Thresholds(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0.70, "high"},
		{0.65, "high"}, // boundary inclusive
		{0.45, "mid"},
		{0.35, "mid"}, // boundary inclusive
		{0.20, "low"},
		{0.00, "low"},
	}
	for _, c := range cases {
		if got := bucketFor(c.score); got != c.want {
			t.Fatalf("bucketFor(%.2f) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestScore_SuggestedName_StopwordStrip(t *testing.T) {
	// Build a synthetic team where 3 meetings titled "Backend Weekly",
	// "Backend Sync", "Backend Standup" should produce "Backend" as the
	// surviving most-frequent token.
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("e1", "s", "Backend Weekly", w, "alice@x", "bob@x", "carol@x"),
		mkEvent("e2", "s", "Backend Sync", w.AddDate(0, 0, 7), "alice@x", "bob@x", "carol@x"),
		mkEvent("e3", "s2", "Backend Standup", w.AddDate(0, 0, 14), "alice@x", "bob@x", "carol@x"),
	}
	g, idx := BuildGraph(events)
	comms, _ := ClusterCommunities(g, idx)
	if len(comms) == 0 {
		t.Skip("clusterer returned no communities — score test cannot run")
	}
	teams := ScoreCommunities(comms, events, g, idx)
	if len(teams) == 0 {
		t.Fatal("want ≥ 1 team")
	}
	if !strings.EqualFold(teams[0].SuggestedName, "Backend") {
		t.Fatalf("SuggestedName: want 'Backend' (case-insensitive), got %q", teams[0].SuggestedName)
	}
}

func TestScore_SuggestedName_FallbackTakım1(t *testing.T) {
	// All meeting titles are stopwords-only — fallback "Takım 1" expected.
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("e1", "s", "Weekly Sync", w, "alice@x", "bob@x", "carol@x"),
		mkEvent("e2", "s", "Daily Standup", w.AddDate(0, 0, 7), "alice@x", "bob@x", "carol@x"),
	}
	g, idx := BuildGraph(events)
	comms, _ := ClusterCommunities(g, idx)
	if len(comms) == 0 {
		t.Skip("clusterer returned no communities — score test cannot run")
	}
	teams := ScoreCommunities(comms, events, g, idx)
	if len(teams) == 0 {
		t.Fatal("want ≥ 1 team")
	}
	if teams[0].SuggestedName != "Takım 1" {
		t.Fatalf("SuggestedName fallback: want 'Takım 1', got %q", teams[0].SuggestedName)
	}
}

func TestScore_TopSeries_RankedByEdgeWeight(t *testing.T) {
	// Two series contribute different weights to the same team — verify
	// the heavier one comes first in TopSeries.
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	// Series A: 4 weekly meetings across 4 distinct weeks with 3 people.
	// Series B: 2 weekly meetings across 2 distinct weeks with 3 people.
	events := []services.BackfillEvent{
		mkEvent("a1", "A", "Alpha Sync", w, "alice@x", "bob@x", "carol@x"),
		mkEvent("a2", "A", "Alpha Sync", w.AddDate(0, 0, 7), "alice@x", "bob@x", "carol@x"),
		mkEvent("a3", "A", "Alpha Sync", w.AddDate(0, 0, 14), "alice@x", "bob@x", "carol@x"),
		mkEvent("a4", "A", "Alpha Sync", w.AddDate(0, 0, 21), "alice@x", "bob@x", "carol@x"),
		mkEvent("b1", "B", "Beta Sync", w, "alice@x", "bob@x", "carol@x"),
		mkEvent("b2", "B", "Beta Sync", w.AddDate(0, 0, 7), "alice@x", "bob@x", "carol@x"),
	}
	g, idx := BuildGraph(events)
	comms, reason := ClusterCommunities(g, idx)
	if reason != "" {
		t.Skipf("cluster returned reason=%q", reason)
	}
	teams := ScoreCommunities(comms, events, g, idx)
	if len(teams) == 0 || len(teams[0].TopSeries) == 0 {
		t.Fatal("want ≥ 1 team with non-empty TopSeries")
	}
	if teams[0].TopSeries[0] != "Alpha Sync" {
		t.Fatalf("TopSeries[0]: want 'Alpha Sync' (4 meetings), got %q", teams[0].TopSeries[0])
	}
}

func TestScore_TopSeries_LexicographicTieBreak(t *testing.T) {
	// Two series with identical structure (and therefore identical
	// edge-weight contribution) — alphabetically earlier title should
	// win the tie.
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("a1", "A", "Alpha Sync", w, "alice@x", "bob@x", "carol@x"),
		mkEvent("a2", "A", "Alpha Sync", w.AddDate(0, 0, 7), "alice@x", "bob@x", "carol@x"),
		mkEvent("b1", "B", "Beta Sync", w, "alice@x", "bob@x", "carol@x"),
		mkEvent("b2", "B", "Beta Sync", w.AddDate(0, 0, 7), "alice@x", "bob@x", "carol@x"),
	}
	g, idx := BuildGraph(events)
	comms, reason := ClusterCommunities(g, idx)
	if reason != "" {
		t.Skipf("cluster returned reason=%q", reason)
	}
	teams := ScoreCommunities(comms, events, g, idx)
	if len(teams) == 0 || len(teams[0].TopSeries) < 2 {
		t.Fatal("want ≥ 1 team with ≥ 2 TopSeries entries")
	}
	if teams[0].TopSeries[0] != "Alpha Sync" {
		t.Fatalf("TopSeries[0] lex tie-break: want 'Alpha Sync', got %q", teams[0].TopSeries[0])
	}
}

func TestScore_TopSeries_LimitedToThree(t *testing.T) {
	// 5 distinct series → TopSeries has exactly 3 entries.
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{}
	for i, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		series := name + "-series"
		events = append(events,
			mkEvent("evt-"+name+"-1", series, name+" Sync", w.AddDate(0, 0, i*2), "alice@x", "bob@x", "carol@x"),
			mkEvent("evt-"+name+"-2", series, name+" Sync", w.AddDate(0, 0, i*2+7), "alice@x", "bob@x", "carol@x"),
		)
	}
	g, idx := BuildGraph(events)
	comms, reason := ClusterCommunities(g, idx)
	if reason != "" {
		t.Skipf("cluster returned reason=%q", reason)
	}
	teams := ScoreCommunities(comms, events, g, idx)
	if len(teams) == 0 {
		t.Fatal("want ≥ 1 team")
	}
	if len(teams[0].TopSeries) != 3 {
		t.Fatalf("TopSeries: want exactly 3 entries, got %d (%v)", len(teams[0].TopSeries), teams[0].TopSeries)
	}
}

func TestScore_SharedMeetingCount_PerMember(t *testing.T) {
	// TRUST-01: a member's SharedMeetingCount equals the number of
	// meetings they co-attended with at least one OTHER member of THIS team.
	// Synthetic team: alice + bob + carol; 4 meetings.
	w := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	events := []services.BackfillEvent{
		mkEvent("m1", "s", "T", w, "alice@x", "bob@x", "carol@x"),           // all 3 → count 1 each
		mkEvent("m2", "s", "T", w.AddDate(0, 0, 7), "alice@x", "bob@x"),     // alice, bob only → +1 each, carol unchanged
		mkEvent("m3", "s2", "T", w.AddDate(0, 0, 14), "alice@x", "carol@x"), // alice, carol → +1 each, bob unchanged
		mkEvent("m4", "s3", "T", w.AddDate(0, 0, 21), "alice@x"),            // alice alone → no in-team co-attendance
	}
	g, idx := BuildGraph(events)
	comms, reason := ClusterCommunities(g, idx)
	if reason != "" {
		t.Skipf("cluster returned reason=%q", reason)
	}
	teams := ScoreCommunities(comms, events, g, idx)
	if len(teams) == 0 {
		t.Fatal("want ≥ 1 team")
	}
	// Find the team containing all three.
	want := map[string]int{
		"alice@x": 3, // m1, m2, m3
		"bob@x":   2, // m1, m2
		"carol@x": 2, // m1, m3
	}
	found := false
	for _, team := range teams {
		if len(team.Members) < 3 {
			continue
		}
		found = true
		for _, m := range team.Members {
			expected, ok := want[m.Email]
			if !ok {
				continue
			}
			if m.SharedMeetingCount != expected {
				t.Fatalf("TRUST-01: %s SharedMeetingCount = %d, want %d", m.Email, m.SharedMeetingCount, expected)
			}
		}
	}
	if !found {
		t.Fatal("TRUST-01: did not find a team containing all three members; cluster may have split them")
	}
}

func TestScore_EmptyInput_ReturnsEmptySlice(t *testing.T) {
	g, idx := BuildGraph(nil)
	teams := ScoreCommunities([][]string{}, nil, g, idx)
	if teams == nil {
		t.Fatal("ScoreCommunities must return empty slice, NOT nil (D-08)")
	}
	if len(teams) != 0 {
		t.Fatalf("want 0 teams, got %d", len(teams))
	}
}

func TestScore_DeterministicAcross5Runs(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	g, idx := BuildGraph(input)
	comms, _ := ClusterCommunities(g, idx)
	first, err := json.Marshal(ScoreCommunities(comms, input, g, idx))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 5; i++ {
		gN, idxN := BuildGraph(input)
		commsN, _ := ClusterCommunities(gN, idxN)
		got, _ := json.Marshal(ScoreCommunities(commsN, input, gN, idxN))
		if !bytesEqual(first, got) {
			t.Fatalf("non-deterministic on iter %d:\nfirst=%s\ngot  =%s", i, first, got)
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestScore_DetectedTeam_FieldsAllPopulated(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	g, idx := BuildGraph(input)
	comms, _ := ClusterCommunities(g, idx)
	teams := ScoreCommunities(comms, input, g, idx)
	if len(teams) == 0 {
		t.Fatal("want ≥ 1 team")
	}
	team := teams[0]
	if team.SuggestedName == "" {
		t.Error("SuggestedName empty (must always be set — fallback Takım N)")
	}
	if team.ConfidenceBucket == "" {
		t.Error("ConfidenceBucket empty")
	}
	if team.ConfidenceScore < 0 || team.ConfidenceScore > 1 || math.IsNaN(team.ConfidenceScore) {
		t.Errorf("ConfidenceScore out of range or NaN: %v", team.ConfidenceScore)
	}
	if len(team.Members) == 0 {
		t.Error("Members empty")
	}
	for _, m := range team.Members {
		if m.Email == "" {
			t.Error("DetectedMember.Email empty")
		}
		if m.SharedMeetingCount < 0 {
			t.Errorf("SharedMeetingCount negative: %d", m.SharedMeetingCount)
		}
	}
}
