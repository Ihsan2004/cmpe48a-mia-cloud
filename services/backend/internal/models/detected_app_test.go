package models

import "testing"

// Phase 2.5 B8 — detected_app allowlist invariant. The CHECK constraint
// in migration 013 mirrors this list; if either side drifts the recorder's
// `POST /api/meetings/desktop` will silently 5xx instead of 4xx-ing the
// invalid value at the handler.
func TestIsValidDetectedApp(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  bool
	}{
		// Allowlist (RECORDER_ROADMAP §2.5)
		{"zoom", true},
		{"google_meet", true},
		{"microsoft_teams", true},
		{"webex", true},
		{"slack_huddle", true},
		{"discord", true},
		{"facetime", true},
		{"other", true},
		// Rejected
		{"", false},
		{"Zoom", false},   // case-sensitive
		{"meet", false},   // alias not allowed
		{"teams", false},  // alias not allowed
		{"chrome", false}, // browser host, not a meeting app
		{"unknown", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.input, func(t *testing.T) {
			t.Parallel()
			if got := IsValidDetectedApp(c.input); got != c.want {
				t.Errorf("IsValidDetectedApp(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}
