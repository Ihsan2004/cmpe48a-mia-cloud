package services

import (
	"context"
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

// cleanupFakeMeetingRepo is a minimal MeetingRepository implementation
// scoped to the calls the cleanup service makes (GetMeetingByIDSystem +
// UpdateMeetingFields). Separate from upload/chain fakes so tests stay
// independent.
type cleanupFakeMeetingRepo struct {
	mu       sync.Mutex
	rows     map[uuid.UUID]*models.Meeting
	updates  map[uuid.UUID]map[string]any
}

func newCleanupFakeMeetingRepo() *cleanupFakeMeetingRepo {
	return &cleanupFakeMeetingRepo{
		rows:    map[uuid.UUID]*models.Meeting{},
		updates: map[uuid.UUID]map[string]any{},
	}
}

func (f *cleanupFakeMeetingRepo) GetMeetingByIDSystem(ctx context.Context, id uuid.UUID) (*models.Meeting, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.rows[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *m
	return &cp, nil
}

func (f *cleanupFakeMeetingRepo) UpdateMeetingFields(ctx context.Context, id uuid.UUID, fields map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[string]any{}
	for k, v := range fields {
		cp[k] = v
	}
	f.updates[id] = cp
	// Also mutate the stored row so subsequent Gets reflect the change.
	if m, ok := f.rows[id]; ok {
		if path, ok := fields["audio_storage_path"]; ok && path == nil {
			m.AudioStoragePath = nil
		}
		if t, ok := fields["audio_deleted_at"]; ok {
			if tm, ok := t.(time.Time); ok {
				m.AudioDeletedAt = &tm
			}
		}
	}
	return nil
}

// Unused interface methods — fulfilled to satisfy MeetingRepository.
func (f *cleanupFakeMeetingRepo) GetMeetingByID(context.Context, uuid.UUID, uuid.UUID) (*models.Meeting, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) GetMeetingBySource(context.Context, uuid.UUID, string) (*models.Meeting, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) CreateMeeting(context.Context, *models.Meeting) error { return nil }
func (f *cleanupFakeMeetingRepo) UpsertDesktopMeeting(_ context.Context, m *models.Meeting) (*models.Meeting, bool, error) {
	return m, true, nil
}
func (f *cleanupFakeMeetingRepo) UpdateMeetingIngestion(context.Context, *models.Meeting) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) GetActiveMeetings(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) GetUpcomingMeetings(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) GetPastMeetingsLastMonth(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) DeleteMeeting(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *cleanupFakeMeetingRepo) DeleteMeetingsNotInGoogleEventIDs(context.Context, uuid.UUID, []string, time.Time, time.Time) (int, error) {
	return 0, nil
}
func (f *cleanupFakeMeetingRepo) GetGoogleEventIDsForUser(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) UpdateAttendeeResponse(context.Context, uuid.UUID, uuid.UUID, []models.Attendee) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) GetUserIDByEmail(context.Context, string, *uuid.UUID) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) GetBotStatus(context.Context, uuid.UUID) (string, error) {
	return "", nil
}
func (f *cleanupFakeMeetingRepo) UpdateBotStatusConnecting(context.Context, uuid.UUID) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) UpdateAutoBotEnabled(context.Context, uuid.UUID, bool) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) UpdateMeetingHeartbeat(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}
func (f *cleanupFakeMeetingRepo) UpdateBotEvent(context.Context, uuid.UUID, string) (int64, error) {
	return 0, nil
}
func (f *cleanupFakeMeetingRepo) GetMeetingIDByURL(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *cleanupFakeMeetingRepo) VerifyMeetingOwnership(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) GetMeetingURLByID(context.Context, uuid.UUID) (string, error) {
	return "", nil
}
func (f *cleanupFakeMeetingRepo) CountActiveBotMeetings(context.Context) (int, error) {
	return 0, nil
}
func (f *cleanupFakeMeetingRepo) GetDesktopRelevantMeetings(context.Context) ([]models.Meeting, error) {
	return nil, nil
}
func (f *cleanupFakeMeetingRepo) GetMeetingIDByTranscriptionJob(context.Context, string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *cleanupFakeMeetingRepo) IncrementTranscriptionAttemptCount(context.Context, uuid.UUID) error {
	return nil
}
func (f *cleanupFakeMeetingRepo) UpdateRecordingMethodOverride(context.Context, uuid.UUID, uuid.UUID, *string) (bool, error) {
	return true, nil
}
func (f *cleanupFakeMeetingRepo) SetChainAssignment(context.Context, uuid.UUID, uuid.UUID, repository.SetChainAssignmentInput) (bool, error) {
	return true, nil
}
func (f *cleanupFakeMeetingRepo) ClearChainAssignmentLock(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (f *cleanupFakeMeetingRepo) RepointMeetingChain(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

// miniStorage is a tiny httptest-backed Supabase stub: tracks delete calls.
type miniStorage struct {
	mu      sync.Mutex
	deleted []string
}

func newMiniStorage(t *testing.T) (*storage.Client, *miniStorage) {
	m := &miniStorage{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/object/meeting-audio/") {
			key := strings.TrimPrefix(r.URL.Path, "/storage/v1/object/meeting-audio/")
			m.mu.Lock()
			m.deleted = append(m.deleted, key)
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c, err := storage.New(srv.URL+"/storage/v1", "k", "meeting-audio")
	if err != nil {
		t.Fatal(err)
	}
	return c, m
}

// ---------- tests ----------

func TestDeleteForMeeting_Happy(t *testing.T) {
	store, mem := newMiniStorage(t)
	repo := newCleanupFakeMeetingRepo()
	meetingID := uuid.New()
	path := fmt.Sprintf("%s/audio.webm", meetingID)
	repo.rows[meetingID] = &models.Meeting{ID: meetingID, AudioStoragePath: &path}
	svc := NewAudioCleanupServiceWithDeps(store, repo, nil, time.Now)

	if err := svc.DeleteForMeeting(context.Background(), meetingID); err != nil {
		t.Fatalf("DeleteForMeeting: %v", err)
	}
	if len(mem.deleted) != 1 || mem.deleted[0] != path {
		t.Errorf("expected delete of %q, got %v", path, mem.deleted)
	}
	upd := repo.updates[meetingID]
	if upd["audio_deleted_at"] == nil {
		t.Errorf("audio_deleted_at not set")
	}
	if upd["audio_storage_path"] != nil {
		t.Errorf("audio_storage_path should be nil, got %v", upd["audio_storage_path"])
	}
}

func TestDeleteForMeeting_NoOpWhenAlreadyDeleted(t *testing.T) {
	store, mem := newMiniStorage(t)
	repo := newCleanupFakeMeetingRepo()
	meetingID := uuid.New()
	now := time.Now()
	repo.rows[meetingID] = &models.Meeting{ID: meetingID, AudioDeletedAt: &now}
	svc := NewAudioCleanupServiceWithDeps(store, repo, nil, time.Now)

	if err := svc.DeleteForMeeting(context.Background(), meetingID); err != nil {
		t.Fatal(err)
	}
	if len(mem.deleted) != 0 {
		t.Errorf("already-deleted row should not hit storage; got %v", mem.deleted)
	}
}

func TestDeleteForMeeting_NoOpWhenNoPath(t *testing.T) {
	store, mem := newMiniStorage(t)
	repo := newCleanupFakeMeetingRepo()
	meetingID := uuid.New()
	repo.rows[meetingID] = &models.Meeting{ID: meetingID} // no audio path
	svc := NewAudioCleanupServiceWithDeps(store, repo, nil, time.Now)

	if err := svc.DeleteForMeeting(context.Background(), meetingID); err != nil {
		t.Fatal(err)
	}
	if len(mem.deleted) != 0 {
		t.Errorf("no-path row should not hit storage")
	}
}

func TestRunCleanup_DispatchesByStatus(t *testing.T) {
	store, mem := newMiniStorage(t)
	repo := newCleanupFakeMeetingRepo()

	okID := uuid.New()
	failID := uuid.New()
	okPath, failPath := "okfile", "failfile"
	repo.rows[okID] = &models.Meeting{ID: okID, AudioStoragePath: &okPath}
	repo.rows[failID] = &models.Meeting{ID: failID, AudioStoragePath: &failPath}

	lister := func(ctx context.Context, cutoff CleanupCutoffs) ([]AudioCleanupCandidate, error) {
		return []AudioCleanupCandidate{
			{MeetingID: okID, Status: "completed", AudioStoragePath: okPath},
			{MeetingID: failID, Status: "failed", AudioStoragePath: failPath},
		}, nil
	}
	fixedNow := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	svc := NewAudioCleanupServiceWithDeps(store, repo, lister, func() time.Time { return fixedNow })

	report, err := svc.RunCleanup(context.Background())
	if err != nil {
		t.Fatalf("RunCleanup: %v", err)
	}
	if report.DeletedSuccessful != 1 || report.DeletedFailed != 1 || report.Errors != 0 {
		t.Errorf("report = %+v", report)
	}
	if len(mem.deleted) != 2 {
		t.Errorf("expected 2 deletes, got %v", mem.deleted)
	}
}

func TestRunCleanup_PassesCorrectCutoffs(t *testing.T) {
	store, _ := newMiniStorage(t)
	repo := newCleanupFakeMeetingRepo()

	var captured CleanupCutoffs
	lister := func(ctx context.Context, cutoff CleanupCutoffs) ([]AudioCleanupCandidate, error) {
		captured = cutoff
		return nil, nil
	}
	fixedNow := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	svc := NewAudioCleanupServiceWithDeps(store, repo, lister, func() time.Time { return fixedNow })

	if _, err := svc.RunCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectSuccess := fixedNow.Add(-SuccessRetention)
	expectFailed := fixedNow.Add(-FailedRetention)
	if !captured.SuccessBefore.Equal(expectSuccess) {
		t.Errorf("SuccessBefore = %v, want %v", captured.SuccessBefore, expectSuccess)
	}
	if !captured.FailedBefore.Equal(expectFailed) {
		t.Errorf("FailedBefore = %v, want %v", captured.FailedBefore, expectFailed)
	}
}

func TestRunCleanup_LogsButContinuesOnErrors(t *testing.T) {
	// Storage server always returns 500 → every delete fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	store, err := storage.New(srv.URL+"/storage/v1", "k", "meeting-audio")
	if err != nil {
		t.Fatal(err)
	}

	repo := newCleanupFakeMeetingRepo()
	id1, id2 := uuid.New(), uuid.New()
	p := "x"
	repo.rows[id1] = &models.Meeting{ID: id1, AudioStoragePath: &p}
	repo.rows[id2] = &models.Meeting{ID: id2, AudioStoragePath: &p}

	lister := func(ctx context.Context, _ CleanupCutoffs) ([]AudioCleanupCandidate, error) {
		return []AudioCleanupCandidate{
			{MeetingID: id1, Status: "completed", AudioStoragePath: p},
			{MeetingID: id2, Status: "completed", AudioStoragePath: p},
		}, nil
	}
	svc := NewAudioCleanupServiceWithDeps(store, repo, lister, time.Now)

	report, err := svc.RunCleanup(context.Background())
	if err != nil {
		t.Fatalf("RunCleanup should not return top-level error on per-candidate failures, got %v", err)
	}
	if report.Errors != 2 || report.DeletedSuccessful != 0 {
		t.Errorf("report = %+v", report)
	}
}
