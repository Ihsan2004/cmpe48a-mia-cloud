package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/storage"

	"github.com/google/uuid"
)

// ---------- fake upload repo ----------

type fakeUploadRepo struct {
	mu        sync.Mutex
	sessions  map[uuid.UUID]*repository.UploadSession
	byMeeting map[uuid.UUID]uuid.UUID
}

func newFakeUploadRepo() *fakeUploadRepo {
	return &fakeUploadRepo{
		sessions:  map[uuid.UUID]*repository.UploadSession{},
		byMeeting: map[uuid.UUID]uuid.UUID{},
	}
}

func (f *fakeUploadRepo) GetActiveByMeeting(ctx context.Context, meetingID uuid.UUID) (*repository.UploadSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byMeeting[meetingID]
	if !ok {
		return nil, repository.ErrUploadSessionNotFound
	}
	s := f.sessions[id]
	if s == nil || s.Status != "in_progress" {
		return nil, repository.ErrUploadSessionNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeUploadRepo) Create(ctx context.Context, s *repository.UploadSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.ID = uuid.New()
	s.Status = "in_progress"
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()
	cp := *s
	f.sessions[s.ID] = &cp
	f.byMeeting[s.MeetingID] = s.ID
	return nil
}

func (f *fakeUploadRepo) AddReceivedBytes(ctx context.Context, sessionID uuid.UUID, delta int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok || s.Status != "in_progress" {
		return 0, repository.ErrUploadSessionNotFound
	}
	s.ReceivedBytes += delta
	return s.ReceivedBytes, nil
}

func (f *fakeUploadRepo) MarkCompleted(ctx context.Context, sessionID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok || s.Status != "in_progress" {
		return repository.ErrUploadSessionNotFound
	}
	now := time.Now()
	s.Status = "completed"
	s.CompletedAt = &now
	return nil
}

func (f *fakeUploadRepo) MarkAborted(ctx context.Context, sessionID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		s.Status = "aborted"
	}
	return nil
}

func (f *fakeUploadRepo) GetByID(ctx context.Context, sessionID uuid.UUID) (*repository.UploadSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, repository.ErrUploadSessionNotFound
	}
	cp := *s
	return &cp, nil
}

// ---------- fake meeting repo (distinct name from chain_matcher_test.go) ----------

type uploadFakeMeetingRepo struct {
	// Embed so MeetingRepository extensions (Phase 3 chain assignment etc.)
	// don't force per-fake stubs.
	repository.MeetingRepository
	mu              sync.Mutex
	owners          map[uuid.UUID]uuid.UUID
	fields          map[uuid.UUID]map[string]any
	updateFieldsErr error // test hook — if set, UpdateMeetingFields returns this
}

func newUploadFakeMeetingRepo() *uploadFakeMeetingRepo {
	return &uploadFakeMeetingRepo{
		owners: map[uuid.UUID]uuid.UUID{},
		fields: map[uuid.UUID]map[string]any{},
	}
}

func (f *uploadFakeMeetingRepo) VerifyMeetingOwnership(ctx context.Context, userID, meetingID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if owner, ok := f.owners[meetingID]; !ok || owner != userID {
		return errors.New("not owner")
	}
	return nil
}

func (f *uploadFakeMeetingRepo) UpdateMeetingFields(ctx context.Context, meetingID uuid.UUID, fields map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateFieldsErr != nil {
		return f.updateFieldsErr
	}
	cp := map[string]any{}
	for k, v := range fields {
		cp[k] = v
	}
	f.fields[meetingID] = cp
	return nil
}

func (f *uploadFakeMeetingRepo) GetMeetingByIDSystem(ctx context.Context, meetingID uuid.UUID) (*models.Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner, ok := f.owners[meetingID]
	if !ok {
		return nil, errors.New("not found")
	}
	path := "users/path/audio.webm"
	return &models.Meeting{ID: meetingID, UserID: owner, AudioStoragePath: &path}, nil
}

// Unused interface stubs — silence compile errors only.
func (f *uploadFakeMeetingRepo) GetMeetingByID(context.Context, uuid.UUID, uuid.UUID) (*models.Meeting, error) {
	return nil, errors.New("not implemented")
}
func (f *uploadFakeMeetingRepo) GetMeetingBySource(context.Context, uuid.UUID, string) (*models.Meeting, error) {
	return nil, nil
}
func (f *uploadFakeMeetingRepo) CreateMeeting(context.Context, *models.Meeting) error { return nil }
func (f *uploadFakeMeetingRepo) UpsertDesktopMeeting(_ context.Context, m *models.Meeting) (*models.Meeting, bool, error) {
	return m, true, nil
}
func (f *uploadFakeMeetingRepo) UpdateMeetingIngestion(context.Context, *models.Meeting) error {
	return nil
}
func (f *uploadFakeMeetingRepo) GetActiveMeetings(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	return nil, nil
}
func (f *uploadFakeMeetingRepo) GetUpcomingMeetings(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	return nil, nil
}
func (f *uploadFakeMeetingRepo) GetPastMeetingsLastMonth(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	return nil, nil
}
func (f *uploadFakeMeetingRepo) DeleteMeeting(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (f *uploadFakeMeetingRepo) DeleteMeetingsNotInGoogleEventIDs(context.Context, uuid.UUID, []string, time.Time, time.Time) (int, error) {
	return 0, nil
}
func (f *uploadFakeMeetingRepo) GetGoogleEventIDsForUser(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *uploadFakeMeetingRepo) UpdateAttendeeResponse(context.Context, uuid.UUID, uuid.UUID, []models.Attendee) error {
	return nil
}
func (f *uploadFakeMeetingRepo) GetUserIDByEmail(context.Context, string, *uuid.UUID) error {
	return nil
}
func (f *uploadFakeMeetingRepo) GetBotStatus(context.Context, uuid.UUID) (string, error) {
	return "", nil
}
func (f *uploadFakeMeetingRepo) UpdateBotStatusConnecting(context.Context, uuid.UUID) error {
	return nil
}
func (f *uploadFakeMeetingRepo) UpdateAutoBotEnabled(context.Context, uuid.UUID, bool) error {
	return nil
}
func (f *uploadFakeMeetingRepo) UpdateMeetingHeartbeat(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *uploadFakeMeetingRepo) UpdateBotEvent(context.Context, uuid.UUID, string) (int64, error) {
	return 0, nil
}
func (f *uploadFakeMeetingRepo) GetMeetingIDByURL(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *uploadFakeMeetingRepo) GetMeetingURLByID(context.Context, uuid.UUID) (string, error) {
	return "", nil
}
func (f *uploadFakeMeetingRepo) CountActiveBotMeetings(context.Context) (int, error) {
	return 0, nil
}
func (f *uploadFakeMeetingRepo) GetDesktopRelevantMeetings(context.Context) ([]models.Meeting, error) {
	return nil, nil
}
func (f *uploadFakeMeetingRepo) GetMeetingIDByTranscriptionJob(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *uploadFakeMeetingRepo) IncrementTranscriptionAttemptCount(context.Context, uuid.UUID) error {
	return nil
}
func (f *uploadFakeMeetingRepo) UpdateRecordingMethodOverride(context.Context, uuid.UUID, uuid.UUID, *string) (bool, error) {
	return true, nil
}

// ---------- fake storage server ----------

type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeStorage(t *testing.T) (*storage.Client, *memStorage) {
	t.Helper()
	mem := &memStorage{objects: map[string][]byte{}}
	srv := httptest.NewServer(mem)
	t.Cleanup(srv.Close)
	c, err := storage.New(srv.URL+"/storage/v1", "fake-key", "meeting-audio")
	if err != nil {
		t.Fatal(err)
	}
	return c, mem
}

func (m *memStorage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const prefix = "/storage/v1/object/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)

	switch {
	case strings.HasPrefix(rest, "list/meeting-audio"):
		var body struct {
			Prefix string `json:"prefix"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		out := []map[string]any{}
		for k, v := range m.objects {
			if strings.HasPrefix(k, body.Prefix) {
				name := strings.TrimPrefix(k, body.Prefix)
				out = append(out, map[string]any{
					"name":     name,
					"metadata": map[string]any{"size": len(v)},
				})
			}
		}
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(rest, "meeting-audio/"):
		key := strings.TrimPrefix(rest, "meeting-audio/")
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.objects[key] = body
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"Key":"` + key + `"}`))
		case http.MethodGet:
			m.mu.Lock()
			b, ok := m.objects[key]
			m.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodDelete:
			m.mu.Lock()
			delete(m.objects, key)
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func countStaging(m *memStorage, sessionID uuid.UUID) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	pref := fmt.Sprintf("_staging/%s/", sessionID)
	n := 0
	for k := range m.objects {
		if strings.HasPrefix(k, pref) {
			n++
		}
	}
	return n
}

// ---------- tests ----------

func TestInitUpload_VerifiesOwnership(t *testing.T) {
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	svc := NewUploadServiceWithDeps(repo, meetings, nil)

	user := uuid.New()
	meetingID := uuid.New()

	_, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm",
	})
	if !errors.Is(err, ErrMeetingNotFound) {
		t.Fatalf("expected ErrMeetingNotFound, got %v", err)
	}

	meetings.owners[meetingID] = user
	sess, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Status != "in_progress" {
		t.Errorf("status = %q", sess.Status)
	}
	if !strings.HasSuffix(sess.StoragePath, "/audio.webm") {
		t.Errorf("storage path = %q", sess.StoragePath)
	}
	f := meetings.fields[meetingID]
	if f["audio_source"] != "desktop" {
		t.Errorf("audio_source = %v", f["audio_source"])
	}
	if f["transcription_status"] != "uploading" {
		t.Errorf("transcription_status = %v", f["transcription_status"])
	}
}

func TestInitUpload_IdempotentResume(t *testing.T) {
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	svc := NewUploadServiceWithDeps(repo, meetings, nil)
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user

	first, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	second, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if first.ID != second.ID {
		t.Errorf("expected same session on resume, got %s vs %s", first.ID, second.ID)
	}
}

func TestInitUpload_SkipOwnerForBot(t *testing.T) {
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	svc := NewUploadServiceWithDeps(repo, meetings, nil)
	_, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: uuid.New(), MeetingID: uuid.New(), ContentType: "audio/webm",
		Source: UploadSourceBot, SkipOwner: true,
	})
	if err != nil {
		t.Fatalf("bot init should skip ownership, got %v", err)
	}
}

func TestAppendChunk_RejectsBadInput(t *testing.T) {
	repo := newFakeUploadRepo()
	svc := NewUploadServiceWithDeps(repo, newUploadFakeMeetingRepo(), nil)
	if _, err := svc.AppendChunk(context.Background(), uuid.New(), -1, strings.NewReader("x"), 1); err == nil {
		t.Error("negative start should error")
	}
	if _, err := svc.AppendChunk(context.Background(), uuid.New(), 0, strings.NewReader("x"), 0); err == nil {
		t.Error("zero chunk size should error")
	}
}

func TestFullChunkedRoundtrip(t *testing.T) {
	store, mem := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user

	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if err != nil {
		t.Fatal(err)
	}

	chunks := []string{"aaaaa", "bbbbb", "ccccc"}
	for i, c := range chunks {
		start := int64(len(c)) * int64(i)
		res, err := svc.AppendChunk(context.Background(), sess.ID, start, strings.NewReader(c), int64(len(c)))
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if res.Outcome != AppendChunkApplied {
			t.Errorf("chunk %d outcome = %v, want Applied", i, res.Outcome)
		}
		expected := int64(len(c)) * int64(i+1)
		if res.NewOffset != expected {
			t.Errorf("offset after chunk %d = %d, want %d", i, res.NewOffset, expected)
		}
	}

	if got := countStaging(mem, sess.ID); got != 3 {
		t.Errorf("staging count = %d, want 3", got)
	}

	err = svc.FinalizeUpload(context.Background(), sess.ID, FinalizeMeta{
		TotalBytes: int64(len("aaaaabbbbbccccc")), DurationSeconds: 42,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	finalPath := sess.StoragePath
	mem.mu.Lock()
	finalBytes, ok := mem.objects[finalPath]
	mem.mu.Unlock()
	if !ok {
		t.Fatalf("final object %s missing from storage", finalPath)
	}
	if string(finalBytes) != "aaaaabbbbbccccc" {
		t.Errorf("final payload = %q, want %q", finalBytes, "aaaaabbbbbccccc")
	}

	if got := countStaging(mem, sess.ID); got != 0 {
		t.Errorf("staging after finalize = %d, want 0", got)
	}

	f := meetings.fields[meetingID]
	if f["transcription_status"] != "pending" {
		t.Errorf("transcription_status = %v, want pending", f["transcription_status"])
	}
	if f["audio_duration_seconds"] != 42 {
		t.Errorf("duration = %v", f["audio_duration_seconds"])
	}

	s, _ := repo.GetByID(context.Background(), sess.ID)
	if s.Status != "completed" {
		t.Errorf("session status = %q, want completed", s.Status)
	}
}

// TestAppendChunk_IdempotentReplay — client retries a chunk after a lost
// ACK. The range is fully below received_bytes, so the service returns
// AppendChunkIdempotent with the current offset and DOES NOT re-stage.
func TestAppendChunk_IdempotentReplay(t *testing.T) {
	store, mem := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})

	// Apply chunk 0 normally
	if _, err := svc.AppendChunk(context.Background(), sess.ID, 0, strings.NewReader("AAAAA"), 5); err != nil {
		t.Fatal(err)
	}
	stagedBefore := countStaging(mem, sess.ID)

	// Client retries the same chunk — backend never advanced its ACK due to
	// a network blip. Should be no-op idempotent.
	res, err := svc.AppendChunk(context.Background(), sess.ID, 0, strings.NewReader("AAAAA"), 5)
	if err != nil {
		t.Fatalf("idempotent replay should not error: %v", err)
	}
	if res.Outcome != AppendChunkIdempotent {
		t.Errorf("outcome = %v, want Idempotent", res.Outcome)
	}
	if res.NewOffset != 5 {
		t.Errorf("new offset = %d, want 5", res.NewOffset)
	}
	if got := countStaging(mem, sess.ID); got != stagedBefore {
		t.Errorf("staging count changed on replay: %d → %d", stagedBefore, got)
	}
	s, _ := repo.GetByID(context.Background(), sess.ID)
	if s.ReceivedBytes != 5 {
		t.Errorf("received_bytes bumped on replay: %d, want 5", s.ReceivedBytes)
	}
}

// TestAppendChunk_GapConflict — client skips ahead (e.g. sent chunk 0,
// then tries to send chunk 2 before 1). Should respond Conflict with
// NewOffset pointing at what the server expects next.
func TestAppendChunk_GapConflict(t *testing.T) {
	store, mem := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	// Apply chunk 0
	if _, err := svc.AppendChunk(context.Background(), sess.ID, 0, strings.NewReader("AAAAA"), 5); err != nil {
		t.Fatal(err)
	}
	// Skip to start=10 (want: start=5). Gap.
	res, err := svc.AppendChunk(context.Background(), sess.ID, 10, strings.NewReader("CCCCC"), 5)
	if err != nil {
		t.Fatalf("gap should not error: %v", err)
	}
	if res.Outcome != AppendChunkConflict {
		t.Errorf("outcome = %v, want Conflict", res.Outcome)
	}
	if res.NewOffset != 5 {
		t.Errorf("new offset = %d, want 5", res.NewOffset)
	}
	if got := countStaging(mem, sess.ID); got != 1 {
		t.Errorf("gap should not stage: count = %d", got)
	}
}

// TestAppendChunk_PartialOverlap — chunk starts before received_bytes but
// extends past it. Neither pure replay nor gap. Treated as Conflict.
func TestAppendChunk_PartialOverlap(t *testing.T) {
	store, mem := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if _, err := svc.AppendChunk(context.Background(), sess.ID, 0, strings.NewReader("AAAAA"), 5); err != nil {
		t.Fatal(err)
	}
	// Retry with a chunk that starts at 3 (inside applied region) but
	// extends to byte 12 (past received_bytes=5). Ambiguous → Conflict.
	res, err := svc.AppendChunk(context.Background(), sess.ID, 3, strings.NewReader("overlapping"), 10)
	if err != nil {
		t.Fatalf("partial overlap should not error: %v", err)
	}
	if res.Outcome != AppendChunkConflict {
		t.Errorf("outcome = %v, want Conflict", res.Outcome)
	}
	if res.NewOffset != 5 {
		t.Errorf("new offset = %d, want 5", res.NewOffset)
	}
	if got := countStaging(mem, sess.ID); got != 1 {
		t.Errorf("overlap should not stage: count = %d", got)
	}
}

// TestFinalize_PartialUpload_Rejected — client finalizes but server has
// fewer bytes than the declared total. Integrity gate should refuse.
func TestFinalize_PartialUpload_Rejected(t *testing.T) {
	store, _ := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	// Only 5 bytes received
	if _, err := svc.AppendChunk(context.Background(), sess.ID, 0, strings.NewReader("AAAAA"), 5); err != nil {
		t.Fatal(err)
	}
	// Client claims 15-byte file
	err := svc.FinalizeUpload(context.Background(), sess.ID, FinalizeMeta{TotalBytes: 15})
	if err == nil {
		t.Fatal("expected error on partial upload")
	}
	if !strings.Contains(err.Error(), "refusing partial upload") {
		t.Errorf("error should mention partial upload, got: %v", err)
	}
	s, _ := repo.GetByID(context.Background(), sess.ID)
	if s.Status == "completed" {
		t.Error("session should NOT be marked completed after partial rejection")
	}
	// Meeting must not flip to transcription_status=pending either — still uploading.
	f := meetings.fields[meetingID]
	if f["transcription_status"] == "pending" {
		t.Error("meeting transcription_status must not become pending on partial upload")
	}
}

// TestInitUpload_MeetingUpdateFailure_AbortsSession — if pinning
// audio_source/transcription_status=uploading fails, the service must
// abort the session and surface the error rather than silently returning
// a live session with a mismatched meeting row.
func TestInitUpload_MeetingUpdateFailure_AbortsSession(t *testing.T) {
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	// Inject update failure
	meetings.updateFieldsErr = fmt.Errorf("simulated db failure")

	svc := NewUploadServiceWithDeps(repo, meetings, nil)
	sess, err := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	if err == nil {
		t.Fatal("expected error when meeting update fails")
	}
	if sess != nil {
		t.Errorf("session should be nil on failure, got %v", sess)
	}
	// Any created session row must have been marked aborted (scanning all rows).
	foundAborted := false
	repo.mu.Lock()
	for _, s := range repo.sessions {
		if s.Status == "aborted" {
			foundAborted = true
		}
	}
	repo.mu.Unlock()
	if !foundAborted {
		t.Error("session should be marked aborted after meeting update failure")
	}
}

func TestFinalize_NoChunksErrors(t *testing.T) {
	store, _ := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	svc := NewUploadServiceWithDeps(repo, meetings, store)

	sess, _ := svc.InitUpload(context.Background(), InitUploadOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm", Source: UploadSourceDesktop,
	})
	err := svc.FinalizeUpload(context.Background(), sess.ID, FinalizeMeta{TotalBytes: 0})
	if err == nil || !strings.Contains(err.Error(), "no chunks") {
		t.Errorf("expected 'no chunks' error, got %v", err)
	}
}

func TestUploadWholeFile_WritesAndUpdatesMeeting(t *testing.T) {
	store, mem := newFakeStorage(t)
	repo := newFakeUploadRepo()
	meetings := newUploadFakeMeetingRepo()
	user := uuid.New()
	meetingID := uuid.New()
	meetings.owners[meetingID] = user
	svc := NewUploadServiceWithDeps(repo, meetings, store)

	body := strings.NewReader("one-shot-audio")
	err := svc.UploadWholeFile(context.Background(), UploadWholeFileOptions{
		UserID: user, MeetingID: meetingID, ContentType: "audio/webm",
		Body: body, TotalBytes: int64(len("one-shot-audio")),
		DurationSeconds: 10, Source: UploadSourceBot, SkipOwner: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := fmt.Sprintf("%s/%s/audio.webm", user, meetingID)
	mem.mu.Lock()
	_, ok := mem.objects[want]
	mem.mu.Unlock()
	if !ok {
		t.Errorf("expected object at %s", want)
	}

	f := meetings.fields[meetingID]
	if f["audio_source"] != "bot" {
		t.Errorf("source = %v", f["audio_source"])
	}
	if f["transcription_status"] != "pending" {
		t.Errorf("status = %v", f["transcription_status"])
	}
}
