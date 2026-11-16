package handlers

import (
	"testing"

	"mia-website/backend/internal/models"
)

// TestBuildProfileResponse_TeamsOnboardingStatus verifies that the
// /api/profile JSON response surface for v1.1 Phase 4 honors:
//   - Each of the four valid states round-trips through buildProfileResponse
//     without mutation (D-04 vocabulary).
//   - The defensive empty-string default at the handler layer mirrors the
//     COALESCE default in ProfileService.GetProfile (T-04-11 mitigation):
//     even if a future refactor drops the SQL COALESCE, the wire response
//     is never the empty string for this field.
func TestBuildProfileResponse_TeamsOnboardingStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pending", "pending"},
		{"completed", "completed"},
		{"skipped", "skipped"},
		{"empty_state_skipped", "empty_state_skipped"},
		{"", "pending"}, // defensive default at handler layer
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			p := &models.Profile{TeamsOnboardingStatus: tc.in}
			resp := buildProfileResponse(p)
			got, ok := resp["teams_onboarding_status"].(string)
			if !ok || got != tc.want {
				t.Fatalf("teams_onboarding_status=%q ok=%v; want %q", got, ok, tc.want)
			}
		})
	}
}

// TestBuildProfileResponse_PreservesExistingKeys asserts the full set of keys
// produced by buildProfileResponse so a future edit cannot silently drop a
// key the frontend already consumes (e.g. is_google_connected backwards-
// compat alias). Count is exactly 21: 20 pre-Phase-4 keys + the new
// teams_onboarding_status. If this fails after an intentional addition,
// update both the count and the expectedKeys map below.
func TestBuildProfileResponse_PreservesExistingKeys(t *testing.T) {
	t.Parallel()

	resp := buildProfileResponse(&models.Profile{
		TeamsOnboardingStatus: models.TeamsOnboardingStatusPending,
	})

	expectedKeys := []string{
		"id",
		"email",
		"full_name",
		"job_title",
		"company",
		"timezone",
		"default_meeting_duration",
		"work_start_hour",
		"work_end_hour",
		"bot_name",
		"bot_language",
		"is_calendar_connected",
		"is_google_connected",
		"google_email",
		"interface_language",
		"brief_lead_time_minutes",
		"recording_method",
		"brief_deliver_via_email",
		"brief_deliver_via_slack",
		"email_unsubscribed_at",
		"teams_onboarding_status",
	}

	if len(resp) != len(expectedKeys) {
		t.Errorf("buildProfileResponse returned %d keys, want %d", len(resp), len(expectedKeys))
	}
	for _, k := range expectedKeys {
		if _, ok := resp[k]; !ok {
			t.Errorf("buildProfileResponse missing expected key %q", k)
		}
	}
}

// TestBuildProfileResponse_RecordingMethodDefault confirms the pre-existing
// recording_method "" -> "bot" defaulting still applies after the Phase 4
// refactor that extracted buildProfileResponse from the inline handler.
func TestBuildProfileResponse_RecordingMethodDefault(t *testing.T) {
	t.Parallel()

	resp := buildProfileResponse(&models.Profile{
		TeamsOnboardingStatus: models.TeamsOnboardingStatusPending,
		RecordingMethod:       "",
	})
	got, ok := resp["recording_method"].(string)
	if !ok || got != models.RecordingMethodBot {
		t.Fatalf("recording_method=%q ok=%v; want %q", got, ok, models.RecordingMethodBot)
	}
}
