package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/storage"

	"github.com/google/uuid"
)

// Phase 4 fix 2026-04-20 regression: transcription_attempt_count must
// advance on EVERY submit attempt, success or failure. The old code only
// bumped after a successful provider response, so an exhausted 3-retry
// cycle could leave the meeting row at 0 while background_jobs.attempts
// climbed to 3 — ops lost the ability to diagnose failed transcriptions
// from the meetings table.

// countingMeetingRepo wraps cleanupFakeMeetingRepo with an IncrementTranscriptionAttemptCount
// counter so the test can assert the bump happened.
type countingMeetingRepo struct {
	*cleanupFakeMeetingRepo
	incrementCalls []uuid.UUID
}

func (c *countingMeetingRepo) IncrementTranscriptionAttemptCount(_ context.Context, meetingID uuid.UUID) error {
	c.incrementCalls = append(c.incrementCalls, meetingID)
	return nil
}

func TestSubmitForMeeting_BumpsAttemptCounter_EvenOnSubmitFailure(t *testing.T) {
	meetingID := uuid.New()
	path := "users/path/audio.webm"
	base := newCleanupFakeMeetingRepo()
	base.rows[meetingID] = &models.Meeting{
		ID:               meetingID,
		AudioStoragePath: &path,
	}
	counting := &countingMeetingRepo{cleanupFakeMeetingRepo: base}

	// Storage stub that returns a signed URL. We don't care about the
	// signed URL destination; ElevenLabs stub below controls the failure.
	storageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Signed URL endpoint returns a tiny JSON blob.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"signedURL":"/storage/v1/object/sign/placeholder","signedUrl":"/storage/v1/object/sign/placeholder"}`))
	}))
	defer storageSrv.Close()
	storeClient, err := storage.New(storageSrv.URL+"/storage/v1", "k", "meeting-audio")
	if err != nil {
		t.Fatal(err)
	}

	// ElevenLabs stub that always 500s — exercises the error branch of SubmitForMeeting.
	elSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"simulated 500"}`))
	}))
	defer elSrv.Close()
	el := NewElevenLabsBatchService(ElevenLabsBatchConfig{
		APIKey:    "test-key",
		ModelID:   "scribe_v2",
		BaseURL:   elSrv.URL,
		WebhookID: "hook-1",
	})

	svc := NewTranscriptionServiceWithDeps(el, storeClient, counting, nil, nil)

	err = svc.SubmitForMeeting(context.Background(), meetingID)
	if err == nil {
		t.Fatal("expected SubmitForMeeting to return an error when ElevenLabs 500s")
	}
	if len(counting.incrementCalls) != 1 {
		t.Fatalf("attempt counter should have bumped exactly once on a failed submit, got %d calls", len(counting.incrementCalls))
	}
	if counting.incrementCalls[0] != meetingID {
		t.Errorf("counter bump for wrong meeting: got %s, want %s", counting.incrementCalls[0], meetingID)
	}
}

func TestSubmitForMeeting_SkipsCounter_WhenNoAudioPath(t *testing.T) {
	// Pre-condition failure (no audio path) should still NOT bump the
	// counter — the attempt never left the building.
	meetingID := uuid.New()
	base := newCleanupFakeMeetingRepo()
	base.rows[meetingID] = &models.Meeting{ID: meetingID} // no AudioStoragePath
	counting := &countingMeetingRepo{cleanupFakeMeetingRepo: base}

	storeClient, _ := storage.New("http://unused", "k", "meeting-audio")
	el := NewElevenLabsBatchService(ElevenLabsBatchConfig{APIKey: "x", ModelID: "scribe_v2"})

	svc := NewTranscriptionServiceWithDeps(el, storeClient, counting, nil, nil)

	err := svc.SubmitForMeeting(context.Background(), meetingID)
	if err == nil {
		t.Fatal("expected error for missing audio path")
	}
	if len(counting.incrementCalls) != 0 {
		t.Errorf("counter should NOT bump on pre-submit validation failure, got %d calls", len(counting.incrementCalls))
	}
	if !errors.Is(err, err) { // tautological — just silence the errors import if unused
		return
	}
}
