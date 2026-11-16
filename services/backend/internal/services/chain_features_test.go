package services

import (
	"testing"
	"time"
)

// ==================== WeekdayMatch ====================

func TestWeekdayMatch_SameDay(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)  // Mon
	b := time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC) // Mon
	if got := WeekdayMatch(a, b); got != 1 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestWeekdayMatch_DifferentDay(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC) // Mon
	b := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC) // Tue
	if got := WeekdayMatch(a, b); got != 0 {
		t.Errorf("expected 0.0, got %f", got)
	}
}

func TestWeekdayMatch_ZeroTime(t *testing.T) {
	t.Parallel()
	if got := WeekdayMatch(time.Time{}, time.Now()); got != 0 {
		t.Errorf("expected 0 for zero time, got %f", got)
	}
}

// ==================== TimeProximity ====================

func TestTimeProximity_ExactSameTime(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 10, 30, 0, 0, time.UTC)
	b := time.Date(2026, 5, 11, 10, 30, 0, 0, time.UTC)
	if got := TimeProximity(a, b); got != 1.0 {
		t.Errorf("expected 1.0 for same hour/minute, got %f", got)
	}
}

func TestTimeProximity_Within1Hour(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	b := time.Date(2026, 5, 11, 10, 45, 0, 0, time.UTC)
	if got := TimeProximity(a, b); got != 0.5 {
		t.Errorf("expected 0.5 for 45-min delta, got %f", got)
	}
}

func TestTimeProximity_ExactlyAtHourBoundary(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	b := time.Date(2026, 5, 11, 11, 0, 0, 0, time.UTC) // exactly 60 min
	if got := TimeProximity(a, b); got != 0.5 {
		t.Errorf("expected 0.5 at exactly 60 min, got %f", got)
	}
}

func TestTimeProximity_Within3Hours(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	b := time.Date(2026, 5, 11, 12, 30, 0, 0, time.UTC) // 150 min
	if got := TimeProximity(a, b); got != 0.2 {
		t.Errorf("expected 0.2 for 150-min delta, got %f", got)
	}
}

func TestTimeProximity_OutsideWindow(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	b := time.Date(2026, 5, 11, 14, 0, 0, 0, time.UTC) // 5 hours
	if got := TimeProximity(a, b); got != 0 {
		t.Errorf("expected 0 for >3h delta, got %f", got)
	}
}

func TestTimeProximity_WrapAroundMidnight(t *testing.T) {
	t.Parallel()
	// 23:30 vs 00:15 — wall clock 45 min apart across midnight
	a := time.Date(2026, 5, 4, 23, 30, 0, 0, time.UTC)
	b := time.Date(2026, 5, 12, 0, 15, 0, 0, time.UTC)
	if got := TimeProximity(a, b); got != 0.5 {
		t.Errorf("expected 0.5 for wrapped 45-min delta, got %f", got)
	}
}

// ==================== RecencyDecay ====================

func TestRecencyDecay_VeryRecent(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	candidate := current.Add(-5 * 24 * time.Hour) // 5 days ago
	if got := RecencyDecay(candidate, current); got != 1.0 {
		t.Errorf("expected 1.0 for 5-day-old candidate, got %f", got)
	}
}

func TestRecencyDecay_AtFlatBoundary(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	candidate := current.Add(-15 * 24 * time.Hour)
	if got := RecencyDecay(candidate, current); got != 1.0 {
		t.Errorf("expected 1.0 at 15-day boundary, got %f", got)
	}
}

func TestRecencyDecay_HalfwayThroughDecay(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	candidate := current.Add(-30 * 24 * time.Hour) // 15 days into 30-day decay window
	got := RecencyDecay(candidate, current)
	if got < 0.49 || got > 0.51 {
		t.Errorf("expected ~0.5 at midpoint of decay, got %f", got)
	}
}

func TestRecencyDecay_AtCutoff(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	candidate := current.Add(-45 * 24 * time.Hour)
	if got := RecencyDecay(candidate, current); got != 0 {
		t.Errorf("expected 0 at 45-day cutoff, got %f", got)
	}
}

func TestRecencyDecay_BeyondCutoff(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	candidate := current.Add(-90 * 24 * time.Hour)
	if got := RecencyDecay(candidate, current); got != 0 {
		t.Errorf("expected 0 beyond cutoff, got %f", got)
	}
}

func TestRecencyDecay_FutureCandidateRejected(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	candidate := current.Add(24 * time.Hour) // future
	if got := RecencyDecay(candidate, current); got != 0 {
		t.Errorf("expected 0 for future candidate, got %f", got)
	}
}

// ==================== DurationBucket ====================

func TestDurationBucket_Brief(t *testing.T) {
	t.Parallel()
	cases := []time.Duration{5 * time.Minute, 15 * time.Minute, 24 * time.Minute}
	for _, d := range cases {
		if got := DurationBucket(d); got != "brief" {
			t.Errorf("%v: expected brief, got %s", d, got)
		}
	}
}

func TestDurationBucket_Standard(t *testing.T) {
	t.Parallel()
	cases := []time.Duration{25 * time.Minute, 30 * time.Minute, 55 * time.Minute}
	for _, d := range cases {
		if got := DurationBucket(d); got != "standard" {
			t.Errorf("%v: expected standard, got %s", d, got)
		}
	}
}

func TestDurationBucket_Extended(t *testing.T) {
	t.Parallel()
	cases := []time.Duration{56 * time.Minute, 60 * time.Minute, 120 * time.Minute}
	for _, d := range cases {
		if got := DurationBucket(d); got != "extended" {
			t.Errorf("%v: expected extended, got %s", d, got)
		}
	}
}

func TestDurationBucketMatch_SameBucket(t *testing.T) {
	t.Parallel()
	if got := DurationBucketMatch(30*time.Minute, 45*time.Minute); got != 1.0 {
		t.Errorf("expected 1.0 for same bucket, got %f", got)
	}
}

func TestDurationBucketMatch_DifferentBucket(t *testing.T) {
	t.Parallel()
	if got := DurationBucketMatch(15*time.Minute, 60*time.Minute); got != 0 {
		t.Errorf("expected 0 across buckets, got %f", got)
	}
}

func TestDurationBucketMatch_NegativeDuration(t *testing.T) {
	t.Parallel()
	if got := DurationBucketMatch(-1*time.Minute, 30*time.Minute); got != 0 {
		t.Errorf("expected 0 for invalid duration, got %f", got)
	}
}
