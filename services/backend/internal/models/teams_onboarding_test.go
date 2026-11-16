package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// Phase 4 v1.1 — TeamsOnboardingStatus four-state vocabulary tests.
// Mirrors the recording_method_test.go pattern.

func TestIsValidTeamsOnboardingStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  bool
	}{
		{"pending", true},
		{"completed", true},
		{"skipped", true},
		{"empty_state_skipped", true},
		{"", false},
		{"PENDING", false},
		{"not_a_state", false},
	}
	for _, c := range cases {
		if got := IsValidTeamsOnboardingStatus(c.input); got != c.want {
			t.Errorf("IsValidTeamsOnboardingStatus(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestTeamsOnboardingStatusConstants(t *testing.T) {
	t.Parallel()

	if TeamsOnboardingStatusPending != "pending" {
		t.Errorf("TeamsOnboardingStatusPending = %q, want %q", TeamsOnboardingStatusPending, "pending")
	}
	if TeamsOnboardingStatusCompleted != "completed" {
		t.Errorf("TeamsOnboardingStatusCompleted = %q, want %q", TeamsOnboardingStatusCompleted, "completed")
	}
	if TeamsOnboardingStatusSkipped != "skipped" {
		t.Errorf("TeamsOnboardingStatusSkipped = %q, want %q", TeamsOnboardingStatusSkipped, "skipped")
	}
	if TeamsOnboardingStatusEmptyStateSkipped != "empty_state_skipped" {
		t.Errorf(
			"TeamsOnboardingStatusEmptyStateSkipped = %q, want %q",
			TeamsOnboardingStatusEmptyStateSkipped, "empty_state_skipped",
		)
	}
}

func TestProfileTeamsOnboardingStatusJSONTag(t *testing.T) {
	t.Parallel()

	p := Profile{TeamsOnboardingStatus: "pending"}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal(Profile) error: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"teams_onboarding_status":"pending"`) {
		t.Errorf("expected JSON to contain `\"teams_onboarding_status\":\"pending\"`, got: %s", got)
	}
}
