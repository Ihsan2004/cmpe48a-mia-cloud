package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/google/uuid"
)

// ==================== Fakes ====================

type fakeDesktopRepo struct {
	mu             sync.Mutex
	insertCalls    []string // commandType
	insertInserted bool     // return value for InsertCommandIfMissing
	insertSeen     map[string]struct{}
	insertErr      error
	ackCalls       []ackCall
	ackErr         error
}

type ackCall struct {
	UserID    uuid.UUID
	MeetingID uuid.UUID
	Command   string
	Status    string
}

func newFakeDesktopRepo() *fakeDesktopRepo {
	return &fakeDesktopRepo{
		insertSeen:     map[string]struct{}{},
		insertInserted: true,
	}
}

func (f *fakeDesktopRepo) CreateSession(_ context.Context, _ uuid.UUID, _ string) (*models.DesktopSession, error) {
	return &models.DesktopSession{ID: uuid.New()}, nil
}
func (f *fakeDesktopRepo) UpdateHeartbeat(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeDesktopRepo) CloseSession(_ context.Context, _ uuid.UUID) error    { return nil }
func (f *fakeDesktopRepo) FindStaleSessions(_ context.Context, _ time.Time) ([]models.DesktopSession, error) {
	return nil, nil
}
func (f *fakeDesktopRepo) InsertCommandIfMissing(
	_ context.Context,
	meetingID, _ uuid.UUID,
	commandType string,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertCalls = append(f.insertCalls, commandType)
	if f.insertErr != nil {
		return false, f.insertErr
	}
	key := meetingID.String() + "|" + commandType
	if _, seen := f.insertSeen[key]; seen {
		return false, nil
	}
	f.insertSeen[key] = struct{}{}
	return f.insertInserted, nil
}
func (f *fakeDesktopRepo) MarkCommandAck(_ context.Context, userID, meetingID uuid.UUID, commandType, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackErr != nil {
		return f.ackErr
	}
	f.ackCalls = append(f.ackCalls, ackCall{UserID: userID, MeetingID: meetingID, Command: commandType, Status: status})
	return nil
}

// fakeMeetingRepoForDesktop only implements the slice of MeetingRepository
// that the desktop scheduler uses — GetDesktopRelevantMeetings. Other
// methods panic to fail loudly if the scheduler ever reaches for them.
type fakeMeetingRepoForDesktop struct {
	// Embed so future MeetingRepository additions don't force a stub here —
	// this fake only cares about GetDesktopRelevantMeetings.
	repository.MeetingRepository
	mu       sync.Mutex
	meetings []models.Meeting
	err      error
	updates  []meetingUpdate
}

func (f *fakeMeetingRepoForDesktop) GetDesktopRelevantMeetings(_ context.Context) ([]models.Meeting, error) {
	return f.meetings, f.err
}

type meetingUpdate struct {
	MeetingID uuid.UUID
	Fields    map[string]interface{}
}

// The rest of MeetingRepository — unused, but required for interface
// satisfaction.
func (f *fakeMeetingRepoForDesktop) GetMeetingByID(context.Context, uuid.UUID, uuid.UUID) (*models.Meeting, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetMeetingByIDSystem(context.Context, uuid.UUID) (*models.Meeting, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetMeetingBySource(context.Context, uuid.UUID, string) (*models.Meeting, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) CreateMeeting(context.Context, *models.Meeting) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpsertDesktopMeeting(context.Context, *models.Meeting) (*models.Meeting, bool, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateMeetingIngestion(context.Context, *models.Meeting) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetActiveMeetings(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetUpcomingMeetings(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetPastMeetingsLastMonth(context.Context, uuid.UUID, string) ([]models.Meeting, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) DeleteMeeting(context.Context, uuid.UUID, uuid.UUID) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) DeleteMeetingsNotInGoogleEventIDs(context.Context, uuid.UUID, []string, time.Time, time.Time) (int, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetGoogleEventIDsForUser(context.Context, uuid.UUID) ([]string, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateAttendeeResponse(context.Context, uuid.UUID, uuid.UUID, []models.Attendee) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetUserIDByEmail(context.Context, string, *uuid.UUID) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetBotStatus(context.Context, uuid.UUID) (string, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateBotStatusConnecting(context.Context, uuid.UUID) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateAutoBotEnabled(context.Context, uuid.UUID, bool) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateMeetingHeartbeat(context.Context, uuid.UUID) (int64, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateBotEvent(context.Context, uuid.UUID, string) (int64, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetMeetingIDByURL(context.Context, string) (uuid.UUID, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) VerifyMeetingOwnership(context.Context, uuid.UUID, uuid.UUID) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetMeetingURLByID(context.Context, uuid.UUID) (string, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateMeetingFields(_ context.Context, meetingID uuid.UUID, fields map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		copied[k] = v
	}
	f.updates = append(f.updates, meetingUpdate{MeetingID: meetingID, Fields: copied})
	return nil
}
func (f *fakeMeetingRepoForDesktop) CountActiveBotMeetings(context.Context) (int, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) GetMeetingIDByTranscriptionJob(context.Context, string) (uuid.UUID, error) {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) IncrementTranscriptionAttemptCount(context.Context, uuid.UUID) error {
	panic("unexpected")
}
func (f *fakeMeetingRepoForDesktop) UpdateRecordingMethodOverride(context.Context, uuid.UUID, uuid.UUID, *string) (bool, error) {
	panic("unexpected")
}

// fakeResolver always returns the configured method.
type fakeResolver struct {
	method string
	err    error
}

func (f fakeResolver) GetEffectiveRecordingMethod(_ context.Context, _ uuid.UUID) (string, error) {
	return f.method, f.err
}

type fakeDesktopQuota struct {
	status     quota.Status
	err        error
	checkCalls int
}

func (f *fakeDesktopQuota) Check(_ context.Context, _ uuid.UUID) (quota.Status, error) {
	f.checkCalls++
	if f.err != nil {
		return quota.Status{}, f.err
	}
	return f.status, nil
}

func (f *fakeDesktopQuota) RecordUsage(_ context.Context, _ repository.UsageLog) error {
	return nil
}

// ==================== pickCommand time-window tests ====================

func TestDesktopScheduler_PickCommand_TimeWindows(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     "Sprint Planning",
		StartTime: start,
		EndTime:   end,
	}

	cases := []struct {
		name        string
		now         time.Time
		wantCmd     string
		wantEmitted bool
	}{
		{
			name:        "3min before start — outside start window",
			now:         start.Add(-3 * time.Minute),
			wantEmitted: false,
		},
		{
			name:        "2min before start — start window lower bound",
			now:         start.Add(-2 * time.Minute),
			wantCmd:     models.DesktopCommandTypeStartRecording,
			wantEmitted: true,
		},
		{
			name:        "1min before start — inside start window",
			now:         start.Add(-1 * time.Minute),
			wantCmd:     models.DesktopCommandTypeStartRecording,
			wantEmitted: true,
		},
		{
			name:        "at start — inside start window",
			now:         start,
			wantCmd:     models.DesktopCommandTypeStartRecording,
			wantEmitted: true,
		},
		{
			name:        "5min after start — start window upper bound",
			now:         start.Add(5 * time.Minute),
			wantCmd:     models.DesktopCommandTypeStartRecording,
			wantEmitted: true,
		},
		{
			name:        "7min after start — between start and stop windows",
			now:         start.Add(7 * time.Minute),
			wantEmitted: false,
		},
		// Phase 2.4 B4: backend NEVER sends stop_recording at the
		// scheduled end — silence/process fusion on the recorder
		// owns that decision. The window between meeting end and the
		// force_stop boundary is intentionally a no-op zone.
		{
			name:        "at end — silence/fusion zone, no command",
			now:         end,
			wantEmitted: false,
		},
		{
			name:        "end + 10s — silence/fusion zone, no command",
			now:         end.Add(10 * time.Second),
			wantEmitted: false,
		},
		{
			name:        "end + 1min — silence/fusion zone, no command",
			now:         end.Add(1 * time.Minute),
			wantEmitted: false,
		},
		{
			name:        "end + 15min — silence/fusion zone, no command",
			now:         end.Add(15 * time.Minute),
			wantEmitted: false,
		},
		{
			name:        "end + 29min — just before force_stop boundary",
			now:         end.Add(29 * time.Minute),
			wantEmitted: false,
		},
		{
			name:        "end + 30min — force_stop boundary",
			now:         end.Add(30 * time.Minute),
			wantCmd:     models.DesktopCommandTypeForceStop,
			wantEmitted: true,
		},
		{
			name:        "end + 35min — inside force_stop zone",
			now:         end.Add(35 * time.Minute),
			wantCmd:     models.DesktopCommandTypeForceStop,
			wantEmitted: true,
		},
	}

	s := &DesktopMeetingScheduler{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _, ok := s.pickCommand(meeting, tc.now)
			if ok != tc.wantEmitted {
				t.Fatalf("emitted=%v, want %v (cmd=%q)", ok, tc.wantEmitted, cmd)
			}
			if ok && cmd != tc.wantCmd {
				t.Errorf("cmd=%q, want %q", cmd, tc.wantCmd)
			}
		})
	}
}

// Phase 2.4 B4 invariant: stop_recording must NEVER be returned by
// pickCommand for any time `now` across a meeting's lifecycle. Sweeps
// from start-10m to end+90m at 30s resolution and asserts.
func TestDesktopScheduler_PickCommand_NeverEmitsStopRecording(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(60 * time.Minute)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     "B4 invariant",
		StartTime: start,
		EndTime:   end,
	}

	s := &DesktopMeetingScheduler{}
	for t0 := start.Add(-10 * time.Minute); t0.Before(end.Add(90 * time.Minute)); t0 = t0.Add(30 * time.Second) {
		cmd, _, ok := s.pickCommand(meeting, t0)
		if ok && cmd == models.DesktopCommandTypeStopRecording {
			t.Fatalf("B4 violated: pickCommand returned stop_recording at %s", t0)
		}
	}
}

// ==================== tick integration tests ====================

func newSchedulerForTest(
	hub *DesktopHub,
	repo *fakeDesktopRepo,
	meetings *fakeMeetingRepoForDesktop,
	method string,
	now time.Time,
) *DesktopMeetingScheduler {
	s := NewDesktopMeetingScheduler(hub, repo, meetings, fakeResolver{method: method}, nil)
	s.SetNowFunc(func() time.Time { return now })
	return s
}

func TestDesktopScheduler_Tick_EmitsStartRecording(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     "Weekly Sync",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}

	hub := NewDesktopHub()
	conn := newFakeConn()
	hub.Register(meeting.UserID, conn)

	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		start.Add(-1*time.Minute))

	s.tick(context.Background())

	if len(repo.insertCalls) != 1 || repo.insertCalls[0] != models.DesktopCommandTypeStartRecording {
		t.Fatalf("insertCalls = %v", repo.insertCalls)
	}
	if conn.lastWrite() == nil {
		t.Fatal("no payload sent to conn")
	}
}

func TestDesktopScheduler_Tick_QuotaExceededSkipsStartRecording(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     "Quota-gated Desktop Sync",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}

	hub := NewDesktopHub()
	conn := newFakeConn()
	hub.Register(meeting.UserID, conn)

	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		start)
	q := &fakeDesktopQuota{status: quota.Status{
		Tier:     quota.TierFree,
		UsedPct:  100,
		Exceeded: true,
	}}
	s.SetQuotaService(q)

	s.tick(context.Background())

	if q.checkCalls != 1 {
		t.Fatalf("quota check calls = %d, want 1", q.checkCalls)
	}
	if len(repo.insertCalls) != 0 {
		t.Fatalf("quota-exceeded start must not insert command, got %v", repo.insertCalls)
	}
	if conn.lastWrite() != nil {
		t.Fatal("quota-exceeded start must not send websocket payload")
	}
	if len(meetings.updates) != 1 {
		t.Fatalf("expected one visible quota status update, got %d", len(meetings.updates))
	}
	if got := meetings.updates[0].Fields["bot_status"]; got != "quota_exceeded" {
		t.Fatalf("bot_status update = %v, want quota_exceeded", got)
	}
}

func TestDesktopScheduler_Tick_ForceStopBypassesQuota(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     "Long Desktop Recording",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}

	hub := NewDesktopHub()
	conn := newFakeConn()
	hub.Register(meeting.UserID, conn)

	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		meeting.EndTime.Add(forceStopAfter))
	q := &fakeDesktopQuota{status: quota.Status{Exceeded: true, UsedPct: 100}}
	s.SetQuotaService(q)

	s.tick(context.Background())

	if q.checkCalls != 0 {
		t.Fatalf("force_stop must bypass quota checks, got %d calls", q.checkCalls)
	}
	if len(repo.insertCalls) != 1 || repo.insertCalls[0] != models.DesktopCommandTypeForceStop {
		t.Fatalf("insertCalls = %v, want force_stop", repo.insertCalls)
	}
	if conn.lastWrite() == nil {
		t.Fatal("force_stop should still be sent even when quota is exceeded")
	}
}

func TestDesktopScheduler_Tick_IdempotentOnSecondCall(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     "Standup",
		StartTime: start,
		EndTime:   start.Add(15 * time.Minute),
	}

	hub := NewDesktopHub()
	conn := newFakeConn()
	hub.Register(meeting.UserID, conn)

	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		start) // inside start window

	s.tick(context.Background())
	s.tick(context.Background())

	if len(repo.insertCalls) != 2 {
		t.Fatalf("expected 2 insert attempts, got %d", len(repo.insertCalls))
	}
	// Only one actual send should have reached the conn — the 2nd insert
	// returns inserted=false.
	if len(conn.writes) != 1 {
		t.Errorf("expected exactly 1 WS write, got %d", len(conn.writes))
	}
}

func TestDesktopScheduler_Tick_SkipsNonDesktopMeetings(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		StartTime: start,
		EndTime:   start.Add(15 * time.Minute),
	}

	hub := NewDesktopHub()
	conn := newFakeConn()
	hub.Register(meeting.UserID, conn)

	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodBot,
		start)

	s.tick(context.Background())

	if len(repo.insertCalls) != 0 {
		t.Errorf("expected 0 insert calls for bot method, got %v", repo.insertCalls)
	}
	if conn.lastWrite() != nil {
		t.Error("should not have sent anything")
	}
}

// Phase 6 fix 2026-04-20 — an offline user must persist `no_ack` so ops
// can tell apart "sent, waiting" (NULL) from "delivered failed" (no_ack)
// from "client responded" (accepted/declined) in the SQL state.
func TestDesktopScheduler_Tick_OfflineUserPersistsNoAck(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	meeting := models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(), // never registered — hub.Send returns ErrUserNotConnected
		StartTime: start,
		EndTime:   start.Add(15 * time.Minute),
	}

	hub := NewDesktopHub()
	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{meetings: []models.Meeting{meeting}}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		start)

	s.tick(context.Background())

	if len(repo.insertCalls) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(repo.insertCalls))
	}
	if len(repo.ackCalls) != 1 {
		t.Fatalf("expected 1 no_ack persistence, got %d", len(repo.ackCalls))
	}
	got := repo.ackCalls[0]
	if got.Status != "no_ack" {
		t.Errorf("ack status = %q, want no_ack", got.Status)
	}
	if got.UserID != meeting.UserID {
		t.Errorf("ack user_id = %s, want %s (scheduler must scope by owner)", got.UserID, meeting.UserID)
	}
	if got.MeetingID != meeting.ID {
		t.Errorf("ack meeting_id = %s, want %s", got.MeetingID, meeting.ID)
	}
}

func TestDesktopScheduler_Tick_NoMeetings(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		time.Now())

	s.tick(context.Background()) // must not panic
}

func TestDesktopScheduler_Tick_MeetingLoadErrorHandled(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	repo := newFakeDesktopRepo()
	meetings := &fakeMeetingRepoForDesktop{err: errors.New("db down")}

	s := newSchedulerForTest(hub, repo, meetings, models.RecordingMethodDesktop,
		time.Now())

	s.tick(context.Background()) // must not panic
	if len(repo.insertCalls) != 0 {
		t.Error("no inserts expected when meeting load errors")
	}
}
