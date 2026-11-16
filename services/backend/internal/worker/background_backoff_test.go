package worker

import (
	"testing"
	"time"

	"mia-website/backend/internal/models"
)

func TestComputeNotifyBriefBackoff_RoadmapSchedule(t *testing.T) {
	t.Parallel()
	// Brief delivery roadmap Karar #14: 30s → 2min → 10min across the
	// three allowed attempts. attempt=1 is the first retry after the
	// initial send, so that's where the 30s appears.
	cases := map[int]time.Duration{
		0: 30 * time.Second,      // defensive default
		1: 30 * time.Second,      // attempt 1 → 2
		2: 2 * time.Minute,       // attempt 2 → 3
		3: 10 * time.Minute,      // attempt 3 → DLQ
		5: 10 * time.Minute,      // anything beyond stays capped
	}
	for attempt, want := range cases {
		got := computeNotifyBriefBackoff(attempt)
		if got != want {
			t.Errorf("attempt %d: got %v, want %v", attempt, got, want)
		}
	}
}

func TestComputeBackoffForJob_NotifyBriefRouting(t *testing.T) {
	t.Parallel()
	// Confirm the job-type dispatcher routes notify_brief to its own
	// backoff (not the default exponential). Guards against someone
	// adding a new job type and accidentally dropping the notify_brief
	// case from the switch.
	job := &models.BackgroundJob{JobType: models.BackgroundJobTypeNotifyBrief, Attempts: 2}
	if got := computeBackoffForJob(job); got != 2*time.Minute {
		t.Errorf("notify_brief attempt=2 should yield 2min, got %v", got)
	}
}

func TestComputeTranscribeBackoff_FollowsFourPower(t *testing.T) {
	// Roadmap Phase 4 karar #6: 1s → 4s → 16s.
	cases := map[int]time.Duration{
		0: 1 * time.Second,
		1: 1 * time.Second,
		2: 4 * time.Second,
		3: 16 * time.Second,
		4: 64 * time.Second,
	}
	for attempt, want := range cases {
		got := computeTranscribeBackoff(attempt)
		if got != want {
			t.Errorf("attempt %d: got %v, want %v", attempt, got, want)
		}
	}
}

func TestComputeTranscribeBackoff_CapsAtMax(t *testing.T) {
	// attempt 20 would overflow; ensure we return maxBackoffDelay instead.
	got := computeTranscribeBackoff(20)
	if got != maxBackoffDelay {
		t.Errorf("attempt 20 should cap at maxBackoffDelay, got %v", got)
	}
}

func TestComputeBackoffForJob_UsesTranscribeScheduleForTranscribeJobs(t *testing.T) {
	jobT := &models.BackgroundJob{JobType: models.BackgroundJobTypeTranscribe, Attempts: 2}
	if got := computeBackoffForJob(jobT); got != 4*time.Second {
		t.Errorf("transcribe attempt 2 should be 4s, got %v", got)
	}
}

func TestComputeBackoffForJob_FallsBackToLegacyForOtherJobs(t *testing.T) {
	jobPM := &models.BackgroundJob{JobType: models.BackgroundJobTypePostMeetingCompletion, Attempts: 1}
	// Legacy schedule: 15s on first retry.
	if got := computeBackoffForJob(jobPM); got != 15*time.Second {
		t.Errorf("post_meeting attempt 1 should follow legacy 15s, got %v", got)
	}
}

func TestComputeBackoffForJob_NilSafe(t *testing.T) {
	if got := computeBackoffForJob(nil); got != 15*time.Second {
		t.Errorf("nil job should return default, got %v", got)
	}
}
