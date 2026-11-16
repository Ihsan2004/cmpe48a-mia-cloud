package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// fakeUpcomingMeetingsLister implements upcomingMeetingsLister.
type fakeUpcomingMeetingsLister struct {
	all []models.Meeting
}

func (f *fakeUpcomingMeetingsLister) GetUpcomingMeetings(_ context.Context, _ uuid.UUID, _ string) ([]models.Meeting, error) {
	return f.all, nil
}

// fakeUpcomingBriefLookup implements upcomingBriefLookup.
type fakeUpcomingBriefLookup struct {
	hasBriefFor map[uuid.UUID]bool
}

func (f *fakeUpcomingBriefLookup) GetBriefByMeetingAndUser(_ context.Context, meetingID, _ uuid.UUID) (*models.MeetingBrief, error) {
	if f.hasBriefFor[meetingID] {
		return &models.MeetingBrief{ID: uuid.New(), TargetMeetingID: meetingID}, nil
	}
	return nil, errors.New("no brief")
}

// fakeRecordingMethodResolver lets each test pin a method per meeting id.
type fakeRecordingMethodResolver struct {
	method  string
	perID   map[uuid.UUID]string
	failFor map[uuid.UUID]bool
}

func (f *fakeRecordingMethodResolver) GetEffectiveRecordingMethod(_ context.Context, meetingID uuid.UUID) (string, error) {
	if f.failFor[meetingID] {
		return "", errors.New("simulated lookup failure")
	}
	if m, ok := f.perID[meetingID]; ok {
		return m, nil
	}
	return f.method, nil
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func newUpcomingService(
	now time.Time,
	meetings []models.Meeting,
	briefs map[uuid.UUID]bool,
	resolver RecordingMethodResolver,
) *CalendarUpcomingService {
	s := &CalendarUpcomingService{
		meetings: &fakeUpcomingMeetingsLister{all: meetings},
		briefs:   &fakeUpcomingBriefLookup{hasBriefFor: briefs},
		resolver: resolver,
		profiles: nil, // tests don't read profile here
		now:      func() time.Time { return now },
	}
	return s
}

// Phase 2.4 B6: events outside the 6 h window must not appear.
func TestUpcoming_FiltersByHorizonAndPast(t *testing.T) {
	t.Parallel()

	now := mustParseTime("2026-05-02T10:00:00Z")
	userID := uuid.New()

	mIn := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Inside horizon",
		StartTime:     now.Add(2 * time.Hour),
		EndTime:       now.Add(3 * time.Hour),
		GoogleEventID: "evt_inside",
	}
	mPast := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Already over",
		StartTime:     now.Add(-2 * time.Hour),
		EndTime:       now.Add(-1 * time.Hour),
		GoogleEventID: "evt_past",
	}
	mFar := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Beyond horizon",
		StartTime:     now.Add(7 * time.Hour),
		EndTime:       now.Add(8 * time.Hour),
		GoogleEventID: "evt_far",
	}

	s := newUpcomingService(
		now,
		[]models.Meeting{mIn, mPast, mFar},
		nil,
		&fakeRecordingMethodResolver{method: models.RecordingMethodDesktop},
	)
	resp, err := s.GetUpcomingForRecorder(context.Background(), userID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event in window, got %d", len(resp.Events))
	}
	if resp.Events[0].Title != "Inside horizon" {
		t.Errorf("wrong event survived filter: %q", resp.Events[0].Title)
	}
}

// Phase 2.4 B5/B6: each event must carry the resolved effective_method.
func TestUpcoming_EmitsEffectiveMethodPerEvent(t *testing.T) {
	t.Parallel()

	now := mustParseTime("2026-05-02T10:00:00Z")
	userID := uuid.New()

	mDesktop := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Desktop record",
		StartTime:     now.Add(30 * time.Minute),
		EndTime:       now.Add(90 * time.Minute),
		GoogleEventID: "evt_desktop",
	}
	mBot := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Bot record",
		StartTime:     now.Add(2 * time.Hour),
		EndTime:       now.Add(3 * time.Hour),
		GoogleEventID: "evt_bot",
	}
	mSkip := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Skip me",
		StartTime:     now.Add(4 * time.Hour),
		EndTime:       now.Add(5 * time.Hour),
		GoogleEventID: "evt_skip",
	}

	resolver := &fakeRecordingMethodResolver{
		perID: map[uuid.UUID]string{
			mDesktop.ID: models.RecordingMethodDesktop,
			mBot.ID:     models.RecordingMethodBot,
			mSkip.ID:    models.RecordingMethodSkip,
		},
	}
	s := newUpcomingService(
		now,
		[]models.Meeting{mDesktop, mBot, mSkip},
		nil,
		resolver,
	)
	resp, err := s.GetUpcomingForRecorder(context.Background(), userID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(resp.Events))
	}
	gotMethods := map[string]string{}
	for _, ev := range resp.Events {
		gotMethods[ev.Title] = ev.EffectiveMethod
	}
	wantMethods := map[string]string{
		"Desktop record": models.RecordingMethodDesktop,
		"Bot record":     models.RecordingMethodBot,
		"Skip me":        models.RecordingMethodSkip,
	}
	for title, want := range wantMethods {
		if got := gotMethods[title]; got != want {
			t.Errorf("event %q effective_method=%q, want %q", title, got, want)
		}
	}
}

// Phase 2.4 B6: a per-event resolver failure must NOT blank the entire
// response — falls back to "bot" so the recorder sees a backend-managed
// event.
func TestUpcoming_ResolverFailureFallsBackToBot(t *testing.T) {
	t.Parallel()

	now := mustParseTime("2026-05-02T10:00:00Z")
	userID := uuid.New()

	mFail := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Resolver explodes",
		StartTime:     now.Add(30 * time.Minute),
		EndTime:       now.Add(90 * time.Minute),
		GoogleEventID: "evt_boom",
	}
	resolver := &fakeRecordingMethodResolver{
		failFor: map[uuid.UUID]bool{mFail.ID: true},
	}
	s := newUpcomingService(now, []models.Meeting{mFail}, nil, resolver)
	resp, err := s.GetUpcomingForRecorder(context.Background(), userID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event despite resolver failure, got %d", len(resp.Events))
	}
	if resp.Events[0].EffectiveMethod != models.RecordingMethodBot {
		t.Errorf("fallback should be bot, got %q", resp.Events[0].EffectiveMethod)
	}
}

// Phase 2.4 B6: brief presence flips brief_ready to true and populates
// brief_url.
func TestUpcoming_BriefPresencePopulatesURL(t *testing.T) {
	t.Parallel()

	now := mustParseTime("2026-05-02T10:00:00Z")
	userID := uuid.New()

	mWith := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Has brief",
		StartTime:     now.Add(30 * time.Minute),
		EndTime:       now.Add(90 * time.Minute),
		GoogleEventID: "evt_with_brief",
	}
	mWithout := models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "No brief",
		StartTime:     now.Add(2 * time.Hour),
		EndTime:       now.Add(3 * time.Hour),
		GoogleEventID: "evt_without_brief",
	}

	briefs := map[uuid.UUID]bool{mWith.ID: true}
	s := newUpcomingService(
		now,
		[]models.Meeting{mWith, mWithout},
		briefs,
		&fakeRecordingMethodResolver{method: models.RecordingMethodDesktop},
	)
	resp, _ := s.GetUpcomingForRecorder(context.Background(), userID, "")
	for _, ev := range resp.Events {
		switch ev.Title {
		case "Has brief":
			if !ev.BriefReady {
				t.Error("Has brief: brief_ready should be true")
			}
			if ev.BriefURL == nil {
				t.Error("Has brief: brief_url should be set")
			}
		case "No brief":
			if ev.BriefReady {
				t.Error("No brief: brief_ready should be false")
			}
			if ev.BriefURL != nil {
				t.Error("No brief: brief_url should be nil")
			}
		}
	}
}

// Phase 2.4 B6: synthetic desktop meetings (google_event_id starts with
// "desktop_") surface as provider="desktop" so the tray candidate queue
// can filter them out cleanly.
func TestUpcoming_DesktopSyntheticMeetingsTaggedSeparately(t *testing.T) {
	t.Parallel()

	now := mustParseTime("2026-05-02T10:00:00Z")
	userID := uuid.New()

	syntheticID := uuid.New()
	calendarID := uuid.New()
	all := []models.Meeting{
		{
			ID:            syntheticID,
			UserID:        userID,
			Title:         "Desktop adhoc",
			StartTime:     now.Add(30 * time.Minute),
			EndTime:       now.Add(90 * time.Minute),
			GoogleEventID: "desktop_01H8...",
		},
		{
			ID:            calendarID,
			UserID:        userID,
			Title:         "Calendar event",
			StartTime:     now.Add(2 * time.Hour),
			EndTime:       now.Add(3 * time.Hour),
			GoogleEventID: "real_google_event_id",
		},
	}
	s := newUpcomingService(now, all, nil, &fakeRecordingMethodResolver{method: models.RecordingMethodDesktop})
	resp, _ := s.GetUpcomingForRecorder(context.Background(), userID, "")
	got := map[string]string{}
	for _, ev := range resp.Events {
		got[ev.Title] = ev.Provider
	}
	if got["Desktop adhoc"] != "desktop" {
		t.Errorf("desktop synthetic meeting should be tagged provider=desktop, got %q", got["Desktop adhoc"])
	}
	if got["Calendar event"] != "google" {
		t.Errorf("calendar event should be tagged provider=google, got %q", got["Calendar event"])
	}
}
