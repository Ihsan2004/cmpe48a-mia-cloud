package services

import "time"

// Stage 2 metadata features for chain matcher v3.
//
// Each helper is a pure function over plain inputs so the unit tests can
// pin numeric boundaries without any DB / matcher plumbing. Weights and
// thresholds live in chain_matcher_v3.go alongside the score combiner.
//
// Roadmap reference: docs/CHAIN_DETECTION_ROADMAP.md §Stage 2.

// WeekdayMatch returns 1.0 when both timestamps fall on the same weekday
// (Monday vs Monday), 0.0 otherwise. Compared in each timestamp's own
// location — recurring meetings keep their organiser's weekday across
// daylight-saving shifts even when the wall-clock time drifts.
func WeekdayMatch(a, b time.Time) float64 {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	if a.Weekday() == b.Weekday() {
		return 1
	}
	return 0
}

// TimeProximity scores how close two start times are on the wall clock,
// ignoring date. Buckets:
//
//	exact same hour-of-day & minute-of-day → 1.0
//	within ±60 minutes                     → 0.5
//	within ±180 minutes                    → 0.2
//	otherwise                              → 0.0
//
// Uses absolute minute-of-day delta with wraparound (so 23:30 vs 00:15 is
// 45 min apart, not 23h15m). The wraparound matters for late-night EU
// teams whose recurring meetings straddle midnight UTC.
func TimeProximity(a, b time.Time) float64 {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	minA := a.Hour()*60 + a.Minute()
	minB := b.Hour()*60 + b.Minute()
	delta := minA - minB
	if delta < 0 {
		delta = -delta
	}
	if delta > 720 {
		delta = 1440 - delta
	}
	switch {
	case delta == 0:
		return 1
	case delta <= 60:
		return 0.5
	case delta <= 180:
		return 0.2
	default:
		return 0
	}
}

// RecencyDecay rewards candidates that happened recently relative to the
// current meeting. Piecewise:
//
//	candidate within 15 days  → 1.0
//	15–45 days                → linear ramp 1.0 → 0.0
//	older than 45 days        → 0.0
//	candidate in the future   → 0.0 (defensive — should not happen given
//	                             the 45-day window query, but a future
//	                             candidate is not useful evidence)
//
// `current` is the new meeting's start time, `candidate` is the past
// meeting's start time.
func RecencyDecay(candidate, current time.Time) float64 {
	if candidate.IsZero() || current.IsZero() {
		return 0
	}
	delta := current.Sub(candidate)
	if delta < 0 {
		return 0
	}
	const flat = 15 * 24 * time.Hour
	const cutoff = 45 * 24 * time.Hour
	if delta <= flat {
		return 1
	}
	if delta >= cutoff {
		return 0
	}
	frac := float64(delta-flat) / float64(cutoff-flat)
	return 1 - frac
}

// Duration bucket boundaries (in minutes).
//
// Boundaries inclusive on the upper side: a 25-min meeting is Standard,
// a 55-min meeting is Standard, a 56-min meeting is Extended. Roadmap
// uses < 25 / 25–55 / > 55 — the inclusive form here matches typical
// 30-min and 60-min calendar conventions ("a 25-min" rounds to Standard,
// not Brief).
const (
	durationBriefMaxMinutes    = 25
	durationStandardMaxMinutes = 55
)

// DurationBucket returns "brief", "standard" or "extended" for the
// scheduled duration. Calendar-side duration only — actual recorded
// duration must NOT feed this so the bucket is stable across re-syncs.
func DurationBucket(d time.Duration) string {
	mins := int(d.Minutes())
	switch {
	case mins < durationBriefMaxMinutes:
		return "brief"
	case mins <= durationStandardMaxMinutes:
		return "standard"
	default:
		return "extended"
	}
}

// DurationBucketMatch returns 1.0 when two scheduled durations fall in
// the same bucket, 0.0 otherwise. A 0-or-negative duration on either
// side returns 0 — defensive against bad calendar payloads where
// end_time precedes start_time.
func DurationBucketMatch(a, b time.Duration) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if DurationBucket(a) == DurationBucket(b) {
		return 1
	}
	return 0
}
