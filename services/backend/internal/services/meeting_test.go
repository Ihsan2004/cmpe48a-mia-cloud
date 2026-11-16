package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== Mock Repository ====================

type mockMeetingRepo struct {
	// Embed so additions to MeetingRepository (Phase 3 chain assignment,
	// future ones) don't require touching this fake unless a test
	// actually exercises the new method.
	repository.MeetingRepository
	meetings       map[uuid.UUID]*models.Meeting
	meetingsByEvent map[string]*models.Meeting // googleEventID -> meeting
	usersByEmail   map[string]uuid.UUID
	createErr      error
	updateErr      error
}

func newMockMeetingRepo() *mockMeetingRepo {
	return &mockMeetingRepo{
		meetings:       make(map[uuid.UUID]*models.Meeting),
		meetingsByEvent: make(map[string]*models.Meeting),
		usersByEmail:   make(map[string]uuid.UUID),
	}
}

func (m *mockMeetingRepo) GetMeetingByID(_ context.Context, userID, meetingID uuid.UUID) (*models.Meeting, error) {
	meeting, ok := m.meetings[meetingID]
	if !ok {
		return nil, fmt.Errorf("no rows in result set")
	}
	if meeting.UserID != userID {
		return nil, fmt.Errorf("no rows in result set")
	}
	return meeting, nil
}

func (m *mockMeetingRepo) GetMeetingByIDSystem(_ context.Context, meetingID uuid.UUID) (*models.Meeting, error) {
	meeting, ok := m.meetings[meetingID]
	if !ok {
		return nil, fmt.Errorf("meeting not found")
	}
	return meeting, nil
}

func (m *mockMeetingRepo) GetMeetingBySource(_ context.Context, userID uuid.UUID, googleEventID string) (*models.Meeting, error) {
	meeting, ok := m.meetingsByEvent[googleEventID]
	if !ok {
		return nil, fmt.Errorf("no rows in result set")
	}
	return meeting, nil
}

func (m *mockMeetingRepo) CreateMeeting(_ context.Context, meeting *models.Meeting) error {
	if m.createErr != nil {
		return m.createErr
	}
	if meeting.ID == uuid.Nil {
		meeting.ID = uuid.New()
	}
	m.meetings[meeting.ID] = meeting
	m.meetingsByEvent[meeting.GoogleEventID] = meeting
	return nil
}

// UpsertDesktopMeeting mirrors the production atomic upsert: returns the
// existing row on conflict (isNew=false) or inserts and returns the new
// row (isNew=true). Backed by the same in-memory map as CreateMeeting so
// existing test wiring keeps working.
func (m *mockMeetingRepo) UpsertDesktopMeeting(
	_ context.Context, meeting *models.Meeting,
) (*models.Meeting, bool, error) {
	if existing, ok := m.meetingsByEvent[meeting.GoogleEventID]; ok && existing.UserID == meeting.UserID {
		return existing, false, nil
	}
	if m.createErr != nil {
		return nil, false, m.createErr
	}
	if meeting.ID == uuid.Nil {
		meeting.ID = uuid.New()
	}
	m.meetings[meeting.ID] = meeting
	m.meetingsByEvent[meeting.GoogleEventID] = meeting
	return meeting, true, nil
}

func (m *mockMeetingRepo) UpdateMeetingIngestion(_ context.Context, meeting *models.Meeting) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.meetings[meeting.ID] = meeting
	return nil
}

func (m *mockMeetingRepo) GetUserIDByEmail(_ context.Context, email string, userID *uuid.UUID) error {
	id, ok := m.usersByEmail[email]
	if !ok {
		return fmt.Errorf("user not found")
	}
	*userID = id
	return nil
}

func (m *mockMeetingRepo) GetActiveMeetings(_ context.Context, _ uuid.UUID, _ string) ([]models.Meeting, error) {
	return nil, nil
}
func (m *mockMeetingRepo) GetUpcomingMeetings(_ context.Context, _ uuid.UUID, _ string) ([]models.Meeting, error) {
	return nil, nil
}
func (m *mockMeetingRepo) GetPastMeetingsLastMonth(_ context.Context, _ uuid.UUID, _ string) ([]models.Meeting, error) {
	return nil, nil
}
func (m *mockMeetingRepo) DeleteMeeting(_ context.Context, _, _ uuid.UUID) error { return nil }
func (m *mockMeetingRepo) DeleteMeetingsNotInGoogleEventIDs(_ context.Context, _ uuid.UUID, _ []string, _, _ time.Time) (int, error) {
	return 0, nil
}
func (m *mockMeetingRepo) GetGoogleEventIDsForUser(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}
func (m *mockMeetingRepo) UpdateAttendeeResponse(_ context.Context, _, _ uuid.UUID, _ []models.Attendee) error {
	return nil
}
func (m *mockMeetingRepo) GetBotStatus(_ context.Context, _ uuid.UUID) (string, error) {
	return "", nil
}
func (m *mockMeetingRepo) UpdateBotStatusConnecting(_ context.Context, _ uuid.UUID) error {
	return nil
}
func (m *mockMeetingRepo) UpdateAutoBotEnabled(_ context.Context, _ uuid.UUID, _ bool) error {
	return nil
}
func (m *mockMeetingRepo) UpdateMeetingHeartbeat(_ context.Context, _ uuid.UUID) (int64, error) {
	return 1, nil
}
func (m *mockMeetingRepo) UpdateBotEvent(_ context.Context, _ uuid.UUID, _ string) (int64, error) {
	return 1, nil
}
func (m *mockMeetingRepo) GetMeetingIDByURL(_ context.Context, _ string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (m *mockMeetingRepo) VerifyMeetingOwnership(_ context.Context, _, _ uuid.UUID) error {
	return nil
}
func (m *mockMeetingRepo) GetMeetingURLByID(_ context.Context, _ uuid.UUID) (string, error) {
	return "", nil
}
func (m *mockMeetingRepo) UpdateMeetingFields(_ context.Context, _ uuid.UUID, _ map[string]interface{}) error {
	return nil
}
func (m *mockMeetingRepo) CountActiveBotMeetings(_ context.Context) (int, error) {
	return 0, nil
}
func (m *mockMeetingRepo) GetDesktopRelevantMeetings(_ context.Context) ([]models.Meeting, error) {
	return nil, nil
}
func (m *mockMeetingRepo) GetMeetingIDByTranscriptionJob(_ context.Context, _ string) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (m *mockMeetingRepo) IncrementTranscriptionAttemptCount(_ context.Context, _ uuid.UUID) error {
	return nil
}
func (m *mockMeetingRepo) UpdateRecordingMethodOverride(_ context.Context, _, _ uuid.UUID, _ *string) (bool, error) {
	return true, nil
}

// ==================== UpsertMeeting Tests ====================

func TestUpsertMeeting_NewMeeting(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	meeting := &models.Meeting{
		UserID:        uuid.New(),
		Title:         "Test Meeting",
		GoogleEventID: "event_123",
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repo.meetings) != 1 {
		t.Errorf("expected 1 meeting in repo, got %d", len(repo.meetings))
	}
}

func TestUpsertMeeting_ExistingMeeting_PreservesFields(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	existingID := uuid.New()
	botStatus := "recording"
	existing := &models.Meeting{
		ID:            existingID,
		UserID:        uuid.New(),
		Title:         "Old Title",
		GoogleEventID: "event_456",
		BotStatus:     &botStatus,
		CreatedAt:     time.Now().Add(-24 * time.Hour),
	}
	repo.meetings[existingID] = existing
	repo.meetingsByEvent["event_456"] = existing

	updated := &models.Meeting{
		UserID:        existing.UserID,
		Title:         "New Title",
		GoogleEventID: "event_456",
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(updated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if updated.ID != existingID {
		t.Errorf("expected ID to be preserved as %s, got %s", existingID, updated.ID)
	}
	if updated.BotStatus == nil || *updated.BotStatus != "recording" {
		t.Error("expected bot_status to be preserved as 'recording'")
	}
}

func TestUpsertMeeting_OrganizerOwnership(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	originalUserID := uuid.New()
	organizerUserID := uuid.New()
	organizerEmail := "organizer@company.com"

	repo.usersByEmail[organizerEmail] = organizerUserID

	meeting := &models.Meeting{
		UserID:         originalUserID,
		Title:          "Meeting with Organizer",
		GoogleEventID:  "event_789",
		OrganizerEmail: &organizerEmail,
		StartTime:      time.Now().Add(1 * time.Hour),
		EndTime:        time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meeting.UserID != organizerUserID {
		t.Errorf("expected UserID to change to organizer %s, got %s", organizerUserID, meeting.UserID)
	}
}

func TestUpsertMeeting_OrganizerNotFound_KeepsOriginalUser(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	originalUserID := uuid.New()
	organizerEmail := "unknown@company.com"

	meeting := &models.Meeting{
		UserID:         originalUserID,
		Title:          "Meeting with Unknown Organizer",
		GoogleEventID:  "event_unknown",
		OrganizerEmail: &organizerEmail,
		StartTime:      time.Now().Add(1 * time.Hour),
		EndTime:        time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meeting.UserID != originalUserID {
		t.Errorf("expected UserID to stay as %s, got %s", originalUserID, meeting.UserID)
	}
}

func TestUpsertMeeting_NilOrganizerEmail(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	userID := uuid.New()
	meeting := &models.Meeting{
		UserID:         userID,
		Title:          "No Organizer",
		GoogleEventID:  "event_nil_org",
		OrganizerEmail: nil,
		StartTime:      time.Now().Add(1 * time.Hour),
		EndTime:        time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meeting.UserID != userID {
		t.Errorf("expected UserID unchanged, got %s", meeting.UserID)
	}
}

func TestUpsertMeeting_CreateError(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	repo.createErr = fmt.Errorf("database connection failed")
	svc := &MeetingService{repo: repo}

	meeting := &models.Meeting{
		UserID:        uuid.New(),
		Title:         "Failing Meeting",
		GoogleEventID: "event_fail",
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(meeting)
	if err == nil {
		t.Fatal("expected error from CreateMeeting")
	}
}

func TestUpsertMeeting_NewMeeting_ChainIDPersisted(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	chainID := uuid.New()
	meeting := &models.Meeting{
		UserID:        uuid.New(),
		Title:         "Chained Meeting",
		GoogleEventID: "event_chain_new",
		ChainID:       &chainID,
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(meeting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify chain_id is stored in repo
	stored := repo.meetings[meeting.ID]
	if stored.ChainID == nil || *stored.ChainID != chainID {
		t.Errorf("expected chain_id %s persisted, got %v", chainID, stored.ChainID)
	}
}

func TestUpsertMeeting_ExistingMeeting_ChainIDPersisted(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	existingID := uuid.New()
	existing := &models.Meeting{
		ID:            existingID,
		UserID:        uuid.New(),
		Title:         "Old Title",
		GoogleEventID: "event_chain_upd",
		CreatedAt:     time.Now().Add(-24 * time.Hour),
	}
	repo.meetings[existingID] = existing
	repo.meetingsByEvent["event_chain_upd"] = existing

	chainID := uuid.New()
	updated := &models.Meeting{
		UserID:        existing.UserID,
		Title:         "Updated Title",
		GoogleEventID: "event_chain_upd",
		ChainID:       &chainID,
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
	}

	err := svc.UpsertMeeting(updated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stored := repo.meetings[existingID]
	if stored.ChainID == nil || *stored.ChainID != chainID {
		t.Errorf("expected chain_id %s persisted on update, got %v", chainID, stored.ChainID)
	}
}

// ==================== EditMeeting Tests ====================

func TestEditMeeting_NotOwner(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	ownerID := uuid.New()
	otherUserID := uuid.New()
	meetingID := uuid.New()

	repo.meetings[meetingID] = &models.Meeting{
		ID:        meetingID,
		UserID:    ownerID,
		StartTime: time.Now().Add(1 * time.Hour),
	}

	_, err := svc.EditMeeting(context.Background(), otherUserID, meetingID, map[string]interface{}{"title": "Hacked"})
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if err.Error() != "permission denied: only the meeting owner can edit" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEditMeeting_PastMeeting(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	userID := uuid.New()
	meetingID := uuid.New()

	repo.meetings[meetingID] = &models.Meeting{
		ID:        meetingID,
		UserID:    userID,
		StartTime: time.Now().Add(-1 * time.Hour), // past
	}

	_, err := svc.EditMeeting(context.Background(), userID, meetingID, map[string]interface{}{"title": "Updated"})
	if err == nil {
		t.Fatal("expected past meeting error")
	}
	if err.Error() != "past meetings cannot be edited" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEditMeeting_NotFound(t *testing.T) {
	t.Parallel()

	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo}

	_, err := svc.EditMeeting(context.Background(), uuid.New(), uuid.New(), map[string]interface{}{"title": "X"})
	if err == nil {
		t.Fatal("expected not found error")
	}
}

// ==================== GetMeetingsForUser Tests ====================

func TestGetMeetingsForUser_CombinesUpcomingAndPast(t *testing.T) {
	t.Parallel()

	repo := &mockMeetingRepoWithData{
		upcoming: []models.Meeting{{Title: "Upcoming"}},
		past:     []models.Meeting{{Title: "Past1"}, {Title: "Past2"}},
	}
	svc := &MeetingService{repo: repo}

	meetings, err := svc.GetMeetingsForUser(context.Background(), uuid.New(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(meetings) != 3 {
		t.Errorf("expected 3 meetings (1 upcoming + 2 past), got %d", len(meetings))
	}
}

// Extended mock for GetMeetingsForUser test
type mockMeetingRepoWithData struct {
	mockMeetingRepo
	upcoming []models.Meeting
	past     []models.Meeting
}

func (m *mockMeetingRepoWithData) GetUpcomingMeetings(_ context.Context, _ uuid.UUID, _ string) ([]models.Meeting, error) {
	return m.upcoming, nil
}

func (m *mockMeetingRepoWithData) GetPastMeetingsLastMonth(_ context.Context, _ uuid.UUID, _ string) ([]models.Meeting, error) {
	return m.past, nil
}

// ==================== UpsertDesktopMeeting tests (Phase 1 B1) ====================

// TestUpsertDesktopMeeting_NewSession verifies the first call for a given
// (user, local_session_id) pair stores the row and reports isNew=true.
func TestUpsertDesktopMeeting_NewSession(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo, profileService: nil}

	userID := uuid.New()
	audioSource := "desktop"
	autoBot := false
	m := &models.Meeting{
		ID:                  uuid.New(),
		UserID:              userID,
		Title:               "Desktop Recording 2026-04-29 10:00",
		StartTime:           time.Now(),
		EndTime:             time.Now().Add(4 * time.Hour),
		GoogleEventID:       "desktop_01HV0000000000000000000000",
		AudioSource:         &audioSource,
		AutoBotEnabled:      &autoBot,
		TranscriptionStatus: "pending",
		CreatedAt:           time.Now(),
	}

	stored, isNew, err := svc.UpsertDesktopMeeting(m)
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}
	if !isNew {
		t.Errorf("first upsert should be isNew=true, got false")
	}
	if stored.ID != m.ID {
		t.Errorf("stored.ID = %v, want %v", stored.ID, m.ID)
	}
}

// TestUpsertDesktopMeeting_IdempotentResubmit verifies a second call with
// the same google_event_id returns the original row unchanged and
// isNew=false. Models the orchestrator Start-time create racing the
// upload worker fallback create.
func TestUpsertDesktopMeeting_IdempotentResubmit(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo, profileService: nil}

	userID := uuid.New()
	eventID := "desktop_01HV1111111111111111111111"

	first := &models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "first",
		StartTime:     time.Now(),
		EndTime:       time.Now().Add(time.Hour),
		GoogleEventID: eventID,
		CreatedAt:     time.Now(),
	}
	stored1, isNew1, err := svc.UpsertDesktopMeeting(first)
	if err != nil || !isNew1 {
		t.Fatalf("first upsert: err=%v isNew=%v", err, isNew1)
	}

	// Second call with the same event id but a different attempted ID/title
	// (simulating worker retry building a fresh meeting struct).
	second := &models.Meeting{
		ID:            uuid.New(), // different id
		UserID:        userID,
		Title:         "second-attempt",
		StartTime:     time.Now(),
		EndTime:       time.Now().Add(time.Hour),
		GoogleEventID: eventID, // SAME google_event_id
		CreatedAt:     time.Now(),
	}
	stored2, isNew2, err := svc.UpsertDesktopMeeting(second)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if isNew2 {
		t.Errorf("second upsert should be isNew=false (idempotent), got true")
	}
	if stored2.ID != stored1.ID {
		t.Errorf("idempotent upsert returned different ID: %v vs %v", stored2.ID, stored1.ID)
	}
	if stored2.Title != "first" {
		t.Errorf("idempotent upsert clobbered title: got %q want %q", stored2.Title, "first")
	}
}

// TestUpsertDesktopMeeting_RejectsCalendarPrefix guards the "this method
// is for desktop synthetic meetings only" precondition. Calling with a
// non-desktop google_event_id must error rather than risk muddling with
// real calendar rows.
func TestUpsertDesktopMeeting_RejectsCalendarPrefix(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo, profileService: nil}

	m := &models.Meeting{
		ID:            uuid.New(),
		UserID:        uuid.New(),
		Title:         "real google calendar event",
		StartTime:     time.Now(),
		EndTime:       time.Now().Add(time.Hour),
		GoogleEventID: "abc123googlecalendarid", // no "desktop_" prefix
		CreatedAt:     time.Now(),
	}
	_, _, err := svc.UpsertDesktopMeeting(m)
	if err == nil {
		t.Errorf("expected error for non-desktop google_event_id, got nil")
	}
}

// TestSetDesktopEndTime_RefusesCalendarMeeting guards the dedicated
// /desktop-end PATCH endpoint from running against a real Google
// Calendar meeting. The check is on the synthetic prefix; without it,
// a misrouted call could clobber end_time on a calendar event without
// the matching Google API update.
func TestSetDesktopEndTime_RefusesCalendarMeeting(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo, profileService: nil}

	userID := uuid.New()
	meetingID := uuid.New()
	calendarMeeting := &models.Meeting{
		ID:            meetingID,
		UserID:        userID,
		Title:         "Standup",
		StartTime:     time.Now(),
		EndTime:       time.Now().Add(time.Hour),
		GoogleEventID: "abc123_real_google_event_id",
	}
	repo.meetings[meetingID] = calendarMeeting

	err := svc.SetDesktopEndTime(context.Background(), userID, meetingID, time.Now().Add(2*time.Hour))
	if err != ErrMeetingNotDesktop {
		t.Errorf("expected ErrMeetingNotDesktop, got %v", err)
	}
}

// TestSetDesktopEndTime_RefusesEndBeforeStart enforces the temporal
// invariant. Without this, recorder clock skew or a race in orphan
// recovery could backdate end_time below start_time and break
// downstream timeline rendering.
func TestSetDesktopEndTime_RefusesEndBeforeStart(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo, profileService: nil}

	userID := uuid.New()
	meetingID := uuid.New()
	startedAt := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	desktopMeeting := &models.Meeting{
		ID:            meetingID,
		UserID:        userID,
		StartTime:     startedAt,
		EndTime:       startedAt.Add(4 * time.Hour),
		GoogleEventID: "desktop_01HV2222222222222222222222",
	}
	repo.meetings[meetingID] = desktopMeeting

	err := svc.SetDesktopEndTime(
		context.Background(),
		userID,
		meetingID,
		startedAt.Add(-time.Minute), // before start_time
	)
	if err == nil {
		t.Errorf("expected ended_at < start_time to error, got nil")
	}
}

// TestSetDesktopEndTime_RejectsOtherUser ensures cross-tenant calls
// translate to "not found" rather than leaking row existence.
func TestSetDesktopEndTime_RejectsOtherUser(t *testing.T) {
	repo := newMockMeetingRepo()
	svc := &MeetingService{repo: repo, profileService: nil}

	owner := uuid.New()
	intruder := uuid.New()
	meetingID := uuid.New()
	repo.meetings[meetingID] = &models.Meeting{
		ID:            meetingID,
		UserID:        owner, // not intruder
		StartTime:     time.Now(),
		EndTime:       time.Now().Add(time.Hour),
		GoogleEventID: "desktop_01HV3333333333333333333333",
	}

	err := svc.SetDesktopEndTime(context.Background(), intruder, meetingID, time.Now().Add(2*time.Hour))
	if err != ErrMeetingNotOwned {
		t.Errorf("expected ErrMeetingNotOwned, got %v", err)
	}
}
