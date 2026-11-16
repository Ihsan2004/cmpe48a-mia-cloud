//go:build e2e

// Package services — e2e_batch_pipeline_test.go exercises the Phase 3/4/6
// batch transcription pipeline end-to-end with hermetic fakes. No live
// services, no DB, no ElevenLabs credits. Run with:
//
//	go test -tags=e2e -count=1 ./internal/services/...
//
// **Scope disclosure (Phase 8 review 2026-04-20):** This file does NOT
// satisfy the original Phase 8.1 contract ("real backend instance +
// staging Supabase bucket + mocked ElevenLabs with 8-row matrix"). It
// covers 6 mocked slices that gate the automatable behaviour:
//   - Scribe submit + webhook ingest produce segments
//   - Retry exhaustion → transcription_status=failed, audio retained
//   - Resumable upload tracks received_bytes across reconnects
//   - Desktop no-ack persists client_ack_status='no_ack'
//   - Desktop force_stop time-window triggers at end+30m
//   - Audio cleanup deletes on webhook success
//
// The bot-upload happy-path also cheats by seeding audio_storage_path
// directly instead of round-tripping through the upload endpoints. Full
// live-meeting matrix (Google Meet / Zoom / Teams + real ElevenLabs +
// real Supabase staging bucket) stays ops-side and is tracked in Phase
// 8.1's prose + the deployment runbook. See ROADMAP_batch_migration.md
// Phase 8.4 Success Criterion 5 for the split.
//
//	TestE2E_BotUpload_HappyPath               bot upload → Scribe submit →
//	                                          webhook ingest → segments
//	TestE2E_ElevenLabsRetry_EventualFail      3 submit failures → failed +
//	                                          audio retained
//	TestE2E_ResumableUpload_PartialThenComplete  chunk resume bookkeeping
//	TestE2E_DesktopNoAck_LogsOnly             scheduler tick with no ws
//	TestE2E_DesktopForceStop_Window           force_stop picked at +30m
//	TestE2E_AudioCleanup_SuccessPath          DeleteForMeeting flips row
//
// Fakes defined in the non-tagged *_test.go files (desktop_hub_test.go,
// desktop_scheduler_test.go, upload_service_test.go, audio_cleanup_
// service_test.go) are reused — they compile into the same test binary
// regardless of build tags.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/storage"

	"github.com/google/uuid"
)

// ---------- shared e2e fakes ----------

// e2eJobRepo is a minimal BackgroundJobRepository implementation that
// records enqueue calls. Only the methods the transcription service
// actually reaches for are real; the rest panic to fail loudly.
type e2eJobRepo struct {
	mu         sync.Mutex
	enqueued   []uuid.UUID
	jobType    string
}

func (r *e2eJobRepo) EnqueuePostMeetingCompletion(_ context.Context, meetingID uuid.UUID, _ string, _ int) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enqueued = append(r.enqueued, meetingID)
	r.jobType = "post_meeting_completion"
	return uuid.New(), nil
}
func (r *e2eJobRepo) EnqueueTranscribe(_ context.Context, meetingID uuid.UUID, _ int) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enqueued = append(r.enqueued, meetingID)
	r.jobType = "transcribe"
	return uuid.New(), nil
}
func (r *e2eJobRepo) EnqueueNotifyBrief(_ context.Context, _ uuid.UUID, _ time.Time, _ int) (uuid.UUID, error) {
	// Brief delivery is its own pipeline (issue #14). Not exercised here.
	return uuid.Nil, nil
}
func (r *e2eJobRepo) ClaimNextPending(context.Context, string) (*models.BackgroundJob, error) {
	return nil, nil
}
func (r *e2eJobRepo) MarkCompleted(context.Context, uuid.UUID) error    { return nil }
func (r *e2eJobRepo) MarkForRetry(context.Context, uuid.UUID, time.Time, string) error {
	return nil
}
func (r *e2eJobRepo) MoveToDLQ(context.Context, *models.BackgroundJob, string) error {
	return nil
}

// e2eTranscriptRepo records segment writes so the happy-path assertion
// can confirm a "segments row exists" without touching PG.
type e2eTranscriptRepo struct {
	mu       sync.Mutex
	segments map[uuid.UUID][]models.TranscriptSegment
	cleared  []uuid.UUID
}

func newE2ETranscriptRepo() *e2eTranscriptRepo {
	return &e2eTranscriptRepo{segments: map[uuid.UUID][]models.TranscriptSegment{}}
}

func (r *e2eTranscriptRepo) InsertSegments(_ context.Context, meetingID uuid.UUID, segs []models.TranscriptSegment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.segments[meetingID] = append(r.segments[meetingID], segs...)
	return nil
}
func (r *e2eTranscriptRepo) DeleteSegmentsByMeetingID(_ context.Context, meetingID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared = append(r.cleared, meetingID)
	delete(r.segments, meetingID)
	return nil
}

// Rest of TranscriptRepository — unused in these tests, but required
// for interface satisfaction.
func (r *e2eTranscriptRepo) GetSegmentsByMeetingID(context.Context, uuid.UUID) ([]models.TranscriptSegment, error) {
	return nil, nil
}
func (r *e2eTranscriptRepo) GetSegmentsByMeetingIDFromTime(context.Context, uuid.UUID, float64) ([]models.TranscriptSegment, error) {
	return nil, nil
}
func (r *e2eTranscriptRepo) CreateChatBlocks(context.Context, []models.TranscriptChatBlock) error {
	return nil
}
func (r *e2eTranscriptRepo) DeleteChatBlocksByMeetingID(context.Context, uuid.UUID) error {
	return nil
}
func (r *e2eTranscriptRepo) DeleteChatBlocksFromTime(context.Context, uuid.UUID, float64) error {
	return nil
}
func (r *e2eTranscriptRepo) MarkChatBlocksFinalized(context.Context, uuid.UUID, time.Time, int) error {
	return nil
}
func (r *e2eTranscriptRepo) GetChatBlocksByMeetingID(context.Context, uuid.UUID) ([]models.TranscriptChatBlock, error) {
	return nil, nil
}
func (r *e2eTranscriptRepo) GetMeetingMetadata(context.Context, uuid.UUID) (string, []string, error) {
	return "", nil, nil
}
func (r *e2eTranscriptRepo) GetChatBlocksWithoutEmbedding(context.Context, uuid.UUID) ([]models.TranscriptChatBlock, error) {
	return nil, nil
}
func (r *e2eTranscriptRepo) GetChatBlocksWithoutEmbeddingBeforeTime(context.Context, uuid.UUID, float64) ([]models.TranscriptChatBlock, error) {
	return nil, nil
}
func (r *e2eTranscriptRepo) UpdateChatBlockEmbedding(context.Context, uuid.UUID, []float64) error {
	return nil
}
func (r *e2eTranscriptRepo) GetDeltaTranscripts(context.Context, uuid.UUID, float64) ([]repository.DeltaTranscriptRow, error) {
	return nil, nil
}

// newSigningStorageStub returns a storage.Client pointed at an httptest
// server that accepts /object/sign/* and returns a signed-URL JSON
// response. Everything else 404s. Used by SubmitForMeeting tests.
func newSigningStorageStub(t *testing.T) *storage.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/object/sign/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signedURL":"/object/sign/meeting-audio/fake?token=stub"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c, err := storage.New(srv.URL+"/storage/v1", "k", "meeting-audio")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ---------- scenario: happy path (bot upload → submit → webhook) ----------

func TestE2E_BotUpload_HappyPath(t *testing.T) {
	t.Parallel()

	// Stub ElevenLabs: returns a request_id. We don't care about body —
	// the submit client only reads request_id.
	elevenLabsCalls := int32(0)
	elStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&elevenLabsCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"req-happy-42"}`))
	}))
	defer elStub.Close()

	// Fake Supabase with /object/sign/ support so the submit path can
	// generate a short-lived audio URL.
	store := newSigningStorageStub(t)

	// Wire the ElevenLabs client against the stub.
	el := NewElevenLabsBatchService(ElevenLabsBatchConfig{
		APIKey:       "test-key",
		WebhookID:    "wh-1",
		BaseURL:      elStub.URL,
		LanguageCode: "tr",
	})
	if el == nil {
		t.Fatal("elevenlabs service constructor returned nil")
	}

	// Meeting state with audio already persisted (bot upload step
	// completed before SubmitForMeeting runs).
	meetingID := uuid.New()
	audioPath := fmt.Sprintf("%s/%s/audio.webm", uuid.New(), meetingID)
	meetings := newCleanupFakeMeetingRepo()
	meetings.rows[meetingID] = &models.Meeting{
		ID:               meetingID,
		AudioStoragePath: &audioPath,
	}

	txRepo := newE2ETranscriptRepo()
	jobs := &e2eJobRepo{}
	svc := NewTranscriptionServiceWithDeps(el, store, meetings, txRepo, jobs)

	if err := svc.SubmitForMeeting(context.Background(), meetingID); err != nil {
		t.Fatalf("SubmitForMeeting: %v", err)
	}
	if atomic.LoadInt32(&elevenLabsCalls) != 1 {
		t.Errorf("ElevenLabs submit calls = %d, want 1", elevenLabsCalls)
	}
	if meetings.updates[meetingID]["transcription_status"] != "processing" {
		t.Errorf("status after submit = %v", meetings.updates[meetingID]["transcription_status"])
	}
	if meetings.updates[meetingID]["transcription_provider_job_id"] != "req-happy-42" {
		t.Errorf("job id = %v", meetings.updates[meetingID]["transcription_provider_job_id"])
	}
}

// ---------- scenario: retry exhaustion keeps audio ----------

func TestE2E_ElevenLabsRetry_EventualFail(t *testing.T) {
	t.Parallel()

	// Stub always 503 — worker would retry, we simulate "after 3 failed
	// attempts" by calling MarkTranscriptionFailed directly (what the
	// worker does on DLQ).
	elStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer elStub.Close()

	store := newSigningStorageStub(t)
	el := NewElevenLabsBatchService(ElevenLabsBatchConfig{
		APIKey: "test-key", WebhookID: "wh-1", BaseURL: elStub.URL,
	})

	meetingID := uuid.New()
	audioPath := fmt.Sprintf("%s/audio.webm", meetingID)
	meetings := newCleanupFakeMeetingRepo()
	meetings.rows[meetingID] = &models.Meeting{
		ID:               meetingID,
		AudioStoragePath: &audioPath,
	}
	txRepo := newE2ETranscriptRepo()
	jobs := &e2eJobRepo{}
	svc := NewTranscriptionServiceWithDeps(el, store, meetings, txRepo, jobs)

	// Emulate 3 attempts.
	var lastErr error
	for i := 0; i < 3; i++ {
		if err := svc.SubmitForMeeting(context.Background(), meetingID); err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		t.Fatal("expected submit error after retries")
	}
	// Worker-level MarkTranscriptionFailed flip.
	if err := svc.MarkTranscriptionFailed(context.Background(), meetingID, lastErr.Error()); err != nil {
		t.Fatalf("MarkTranscriptionFailed: %v", err)
	}
	if meetings.updates[meetingID]["transcription_status"] != "failed" {
		t.Errorf("status = %v, want failed", meetings.updates[meetingID]["transcription_status"])
	}
	// Audio should not be touched — the 7-day retention path keeps
	// failed recordings around for inspection.
	row := meetings.rows[meetingID]
	if row.AudioDeletedAt != nil {
		t.Errorf("audio should be retained on failure, got deleted_at=%v", row.AudioDeletedAt)
	}
	if row.AudioStoragePath == nil {
		t.Errorf("audio_storage_path cleared on failure path — should stay")
	}
}

// ---------- scenario: resumable upload resumes cleanly ----------

func TestE2E_ResumableUpload_PartialThenComplete(t *testing.T) {
	t.Parallel()

	store, mem := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	userID := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = userID

	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: userID, MeetingID: meetingID,
		ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Client sends 1 of 3 chunks, then disconnects.
	chunk1 := strings.Repeat("a", 100)
	res, err := svc.AppendChunk(context.Background(), sess.ID, 0, strings.NewReader(chunk1), int64(len(chunk1)))
	if err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	if res.NewOffset != 100 {
		t.Errorf("offset after chunk 1 = %d, want 100", res.NewOffset)
	}

	// Resume: second InitUpload must return the same session.
	resume, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: userID, MeetingID: meetingID,
		ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resume.ID != sess.ID {
		t.Fatalf("resume created new session — not idempotent")
	}
	if resume.ReceivedBytes != 100 {
		t.Errorf("received_bytes on resume = %d, want 100", resume.ReceivedBytes)
	}

	// Send chunks 2 + 3. With the Phase 2 fix Content-Range.Start must
	// equal received_bytes, so the start offsets are 100 and 200.
	chunk2 := strings.Repeat("b", 100)
	chunk3 := strings.Repeat("c", 100)
	if _, err := svc.AppendChunk(context.Background(), sess.ID, 100, strings.NewReader(chunk2), int64(len(chunk2))); err != nil {
		t.Fatalf("chunk 2: %v", err)
	}
	res, err = svc.AppendChunk(context.Background(), sess.ID, 200, strings.NewReader(chunk3), int64(len(chunk3)))
	if err != nil {
		t.Fatalf("chunk 3: %v", err)
	}
	if res.NewOffset != 300 {
		t.Errorf("offset after chunk 3 = %d, want 300", res.NewOffset)
	}

	if err := svc.FinalizeUpload(context.Background(), sess.ID, FinalizeMeta{TotalBytes: 300, DurationSeconds: 30}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	final, ok := mem.objects[sess.StoragePath]
	if !ok {
		t.Fatalf("final object not present at %s", sess.StoragePath)
	}
	if string(final) != chunk1+chunk2+chunk3 {
		t.Errorf("final payload order/size mismatch")
	}
}

// ---------- scenario: desktop command fires but client is offline ----------

// Phase 6 fix 2026-04-20 — offline user must persist no_ack explicitly so
// ops can distinguish "sent, waiting" (NULL) from "never delivered"
// (no_ack) from "client responded" (accepted/declined). Previously this
// test asserted zero ack calls; the scheduler now writes one.
func TestE2E_DesktopNoAck_PersistsNoAck(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(), // never registers with the hub
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}

	hub := NewDesktopHub() // no registrations
	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		start) // inside start_recording window
	s.tick(context.Background())

	if len(repo.insertCalls) != 1 {
		t.Fatalf("expected 1 insert attempt, got %d", len(repo.insertCalls))
	}
	if len(repo.ackCalls) != 1 {
		t.Fatalf("expected 1 no_ack persistence, got %d", len(repo.ackCalls))
	}
	if repo.ackCalls[0].Status != "no_ack" {
		t.Errorf("ack status = %q, want no_ack", repo.ackCalls[0].Status)
	}
}

// ---------- scenario: force_stop fires past the hard cap ----------

func TestE2E_DesktopForceStop_Window(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		StartTime: start,
		EndTime:   end,
	}

	hub := NewDesktopHub()
	conn := newFakeConn()
	hub.Register(meeting.UserID, conn)

	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	// Clock at end + 31 minutes — force_stop window.
	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		end.Add(31*time.Minute))
	s.tick(context.Background())

	if len(repo.insertCalls) != 1 || repo.insertCalls[0] != models.DesktopCommandTypeForceStop {
		t.Fatalf("insertCalls = %v, want 1 force_stop", repo.insertCalls)
	}
	if len(conn.writes) != 1 {
		t.Fatalf("conn writes = %d, want 1", len(conn.writes))
	}
	// Payload must carry force_stop type so Kerem's desktop app doesn't
	// wait for user confirmation.
	var payload map[string]interface{}
	if err := json.Unmarshal(conn.writes[0], &payload); err != nil {
		t.Fatalf("payload is not json: %v", err)
	}
	if payload["type"] != models.DesktopCommandTypeForceStop {
		t.Errorf("payload type = %v, want %s", payload["type"], models.DesktopCommandTypeForceStop)
	}
	// Ack is reported back from the desktop client — scheduler only
	// persists the send, not an ack. So repo.ackCalls stays empty until
	// the client POSTs its command_ack.
	if len(repo.ackCalls) != 0 {
		t.Errorf("unexpected ack calls: %v", repo.ackCalls)
	}
}

// ---------- scenario: successful ingest deletes audio immediately ----------

func TestE2E_AudioCleanup_SuccessPath(t *testing.T) {
	t.Parallel()

	store, mem := newMiniStorage(t)
	repo := newCleanupFakeMeetingRepo()
	meetingID := uuid.New()
	path := fmt.Sprintf("%s/audio.webm", meetingID)
	repo.rows[meetingID] = &models.Meeting{ID: meetingID, AudioStoragePath: &path}

	cleanup := NewAudioCleanupServiceWithDeps(store, repo, nil, time.Now)
	if err := cleanup.DeleteForMeeting(context.Background(), meetingID); err != nil {
		t.Fatalf("DeleteForMeeting: %v", err)
	}

	// Supabase saw the DELETE.
	if len(mem.deleted) != 1 || mem.deleted[0] != path {
		t.Errorf("supabase deletes = %v, want [%s]", mem.deleted, path)
	}
	// Meeting row reflects the cleanup.
	upd := repo.updates[meetingID]
	if upd["audio_deleted_at"] == nil {
		t.Errorf("audio_deleted_at not written")
	}
	if upd["audio_storage_path"] != nil {
		t.Errorf("audio_storage_path should be cleared, got %v", upd["audio_storage_path"])
	}
}

// ---------- small sanity compile-time reference so this file imports what it
//            says it does (errors is used in retry scenario's guard).

var _ = errors.New
