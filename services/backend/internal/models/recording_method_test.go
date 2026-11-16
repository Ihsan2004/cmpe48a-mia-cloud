package models

import "testing"

// Phase 2.4 B5 — recording method enum + effective-method resolver tests.

func TestIsValidRecordingMethod(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  bool
	}{
		{RecordingMethodBot, true},
		{RecordingMethodDesktop, true},
		{RecordingMethodSkip, false}, // skip is override-only, not profile-level
		{"", false},
		{"BOT", false},
		{"unknown", false},
	}
	for _, c := range cases {
		if got := IsValidRecordingMethod(c.input); got != c.want {
			t.Errorf("IsValidRecordingMethod(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestIsValidRecordingMethodOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  bool
	}{
		{RecordingMethodBot, true},
		{RecordingMethodDesktop, true},
		{RecordingMethodSkip, true},
		{"", false},
		{"unknown", false},
	}
	for _, c := range cases {
		if got := IsValidRecordingMethodOverride(c.input); got != c.want {
			t.Errorf("IsValidRecordingMethodOverride(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestResolveEffectiveMethod(t *testing.T) {
	t.Parallel()

	strPtr := func(s string) *string { return &s }

	cases := []struct {
		name           string
		override       *string
		profileDefault string
		want           string
	}{
		// Override wins regardless of profile.
		{"override desktop wins over profile bot", strPtr("desktop"), "bot", "desktop"},
		{"override skip wins over profile desktop", strPtr("skip"), "desktop", "skip"},
		{"override bot wins over profile desktop", strPtr("bot"), "desktop", "bot"},

		// No override: profile decides.
		{"no override falls to profile desktop", nil, "desktop", "desktop"},
		{"no override falls to profile bot", nil, "bot", "bot"},

		// Empty override pointer-to-empty-string also falls through (no
		// override in practice; defensive).
		{"empty override falls to profile", strPtr(""), "desktop", "desktop"},

		// No override, no profile: hard default to bot.
		{"both empty defaults to bot", nil, "", "bot"},
		{"empty override + empty profile defaults to bot", strPtr(""), "", "bot"},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveEffectiveMethod(c.override, c.profileDefault)
			if got != c.want {
				t.Errorf(
					"ResolveEffectiveMethod(%v, %q) = %q, want %q",
					c.override, c.profileDefault, got, c.want,
				)
			}
		})
	}
}
