package services

import (
	"context"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/briefnotify"

	"github.com/google/uuid"
)

// ==================== Fakes for two-phase scheduler ====================

type fakeDeliveryRepoMem struct {
	store map[uuid.UUID]*models.BriefDelivery
}

func newFakeDeliveryRepoMem() *fakeDeliveryRepoMem {
	return &fakeDeliveryRepoMem{store: map[uuid.UUID]*models.BriefDelivery{}}
}

func (f *fakeDeliveryRepoMem) Create(ctx context.Context, d *models.BriefDelivery) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	f.store[d.ID] = d
	return nil
}
func (f *fakeDeliveryRepoMem) GetByID(ctx context.Context, id uuid.UUID) (*models.BriefDelivery, error) {
	return f.store[id], nil
}
func (f *fakeDeliveryRepoMem) GetByMeetingChannel(ctx context.Context, meetingID uuid.UUID, channel string) (*models.BriefDelivery, error) {
	for _, d := range f.store {
		if d.MeetingID == meetingID && d.Channel == channel {
			return d, nil
		}
	}
	return nil, nil
}
func (f *fakeDeliveryRepoMem) ListRecent(ctx context.Context, _ repository.BriefDeliveryFilter) ([]models.BriefDelivery, error) {
	var out []models.BriefDelivery
	for _, d := range f.store {
		out = append(out, *d)
	}
	return out, nil
}

func (f *fakeDeliveryRepoMem) ListPendingDue(ctx context.Context, now time.Time, limit int) ([]models.BriefDelivery, error) {
	var out []models.BriefDelivery
	for _, d := range f.store {
		if d.Status == models.BriefDeliveryStatusPending && !d.ScheduledFor.After(now) {
			out = append(out, *d)
		}
	}
	return out, nil
}
func (f *fakeDeliveryRepoMem) UpdateStatus(ctx context.Context, id uuid.UUID, status, providerMsgID, lastErr string) error {
	if d, ok := f.store[id]; ok {
		d.Status = status
	}
	return nil
}
func (f *fakeDeliveryRepoMem) IncrementAttempts(ctx context.Context, id uuid.UUID) error { return nil }

type fakeJobRepoMem struct {
	notifyBriefCalls []notifyCall
}

type notifyCall struct {
	deliveryID  uuid.UUID
	scheduledAt time.Time
}

func (f *fakeJobRepoMem) EnqueueNotifyBrief(ctx context.Context, deliveryID uuid.UUID, scheduledAt time.Time, maxAttempts int) (uuid.UUID, error) {
	f.notifyBriefCalls = append(f.notifyBriefCalls, notifyCall{deliveryID: deliveryID, scheduledAt: scheduledAt})
	return uuid.New(), nil
}

// The other BackgroundJobRepository methods aren't exercised by the
// two-phase scheduler — return zero values so the interface compiles.
func (f *fakeJobRepoMem) EnqueuePostMeetingCompletion(ctx context.Context, _ uuid.UUID, _ string, _ int) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeJobRepoMem) EnqueueTranscribe(ctx context.Context, _ uuid.UUID, _ int) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeJobRepoMem) ClaimNextPending(ctx context.Context, _ string) (*models.BackgroundJob, error) {
	return nil, nil
}
func (f *fakeJobRepoMem) MarkCompleted(ctx context.Context, _ uuid.UUID) error { return nil }
func (f *fakeJobRepoMem) MarkForRetry(ctx context.Context, _ uuid.UUID, _ time.Time, _ string) error {
	return nil
}
func (f *fakeJobRepoMem) MoveToDLQ(ctx context.Context, _ *models.BackgroundJob, _ string) error {
	return nil
}

// ==================== Buffer constant ====================

func TestBriefScheduler_GenerateBufferIs20Minutes(t *testing.T) {
	t.Parallel()
	// Roadmap Karar #17: brief generation fires start-20min; delivery
	// at start-leadTime. Locking the constant makes the roadmap
	// contract visible in code.
	if briefGenerateBufferMinutes != 20 {
		t.Errorf("briefGenerateBufferMinutes must be 20 per roadmap, got %d", briefGenerateBufferMinutes)
	}
}

// ==================== schedulePerChannelDeliveries — lead-time math ====================

func TestSchedulePerChannelDeliveries_UsesLeadTimeForScheduledFor(t *testing.T) {
	t.Parallel()

	// Verify that scheduled_for = meeting.start_time - lead_time. The
	// roadmap's contract: brief gets generated 20min out, but the
	// worker only sends at the user's lead-time setting.
	deliveryRepo := newFakeDeliveryRepoMem()
	jobRepo := &fakeJobRepoMem{}
	dispatcher := briefnotify.NewDispatcher(
		deliveryRepo,
		&stubPrefs{prefs: briefnotify.BriefDeliveryPreferences{EmailEnabled: true}},
		jobRepo,
	)
	dispatcher.Register(&stubDeliverer{name: models.BriefDeliveryChannelEmail})

	s := &BriefScheduler{
		deliveryRepo: deliveryRepo,
		jobRepo:      jobRepo,
		dispatcher:   dispatcher,
		clock:        func() time.Time { return time.Now().UTC() },
	}

	startTime := time.Now().UTC().Add(30 * time.Minute)
	meeting := &models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		StartTime: startTime,
	}
	leadTime := 15

	s.schedulePerChannelDeliveries(context.Background(), meeting, leadTime)

	if len(deliveryRepo.store) != 1 {
		t.Fatalf("expected 1 delivery row, got %d", len(deliveryRepo.store))
	}
	for _, d := range deliveryRepo.store {
		want := startTime.Add(-time.Duration(leadTime) * time.Minute)
		if !d.ScheduledFor.Equal(want) {
			t.Errorf("scheduled_for = %v, want %v (start-leadTime)", d.ScheduledFor, want)
		}
	}
}

func TestSchedulePerChannelDeliveries_ZeroLeadTime_DefaultsTo10(t *testing.T) {
	t.Parallel()

	// Defensive default: if profile.brief_lead_time_minutes somehow
	// came back as 0, we fall back to the product default (10m) rather
	// than scheduling the delivery for exactly meeting.start_time.
	deliveryRepo := newFakeDeliveryRepoMem()
	dispatcher := briefnotify.NewDispatcher(
		deliveryRepo,
		&stubPrefs{prefs: briefnotify.BriefDeliveryPreferences{EmailEnabled: true}},
		&fakeJobRepoMem{},
	)
	dispatcher.Register(&stubDeliverer{name: models.BriefDeliveryChannelEmail})

	s := &BriefScheduler{
		deliveryRepo: deliveryRepo,
		jobRepo:      &fakeJobRepoMem{},
		dispatcher:   dispatcher,
		clock:        func() time.Time { return time.Now().UTC() },
	}

	startTime := time.Now().UTC().Add(30 * time.Minute)
	meeting := &models.Meeting{ID: uuid.New(), UserID: uuid.New(), StartTime: startTime}

	s.schedulePerChannelDeliveries(context.Background(), meeting, 0)

	var got time.Time
	for _, d := range deliveryRepo.store {
		got = d.ScheduledFor
	}
	want := startTime.Add(-10 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("expected fallback lead_time=10 → scheduled_for=%v, got %v", want, got)
	}
}

func TestSchedulePerChannelDeliveries_NoDispatcher_NoOp(t *testing.T) {
	t.Parallel()

	// If WithDispatcher wasn't called, the scheduler must silently
	// skip fan-out (preserves legacy-only deployments).
	s := &BriefScheduler{
		dispatcher: nil,
		clock:      func() time.Time { return time.Now().UTC() },
	}

	meeting := &models.Meeting{
		ID: uuid.New(), UserID: uuid.New(),
		StartTime: time.Now().Add(30 * time.Minute),
	}
	// Should not panic; no-op is the contract.
	s.schedulePerChannelDeliveries(context.Background(), meeting, 10)
}

// ==================== dispatchDueDeliveries ====================

func TestDispatchDueDeliveries_EnqueuesOnlyDueRows(t *testing.T) {
	t.Parallel()

	deliveryRepo := newFakeDeliveryRepoMem()
	jobRepo := &fakeJobRepoMem{}
	dispatcher := briefnotify.NewDispatcher(
		deliveryRepo,
		&stubPrefs{},
		jobRepo,
	)
	dispatcher.Register(&stubDeliverer{name: models.BriefDeliveryChannelEmail})

	now := time.Now().UTC()
	s := &BriefScheduler{
		deliveryRepo: deliveryRepo,
		jobRepo:      jobRepo,
		dispatcher:   dispatcher,
		clock:        func() time.Time { return now },
	}

	// Due row (scheduled_for already past).
	dueID := uuid.New()
	deliveryRepo.store[dueID] = &models.BriefDelivery{
		ID: dueID, MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: now.Add(-5 * time.Minute),
	}
	// Future row — not yet due.
	futID := uuid.New()
	deliveryRepo.store[futID] = &models.BriefDelivery{
		ID: futID, MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: now.Add(5 * time.Minute),
	}
	// Already-sent row — must not be re-enqueued.
	sentID := uuid.New()
	deliveryRepo.store[sentID] = &models.BriefDelivery{
		ID: sentID, MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusSent,
		ScheduledFor: now.Add(-10 * time.Minute),
	}

	s.dispatchDueDeliveries(context.Background())

	if len(jobRepo.notifyBriefCalls) != 1 {
		t.Fatalf("expected exactly 1 enqueue, got %d", len(jobRepo.notifyBriefCalls))
	}
	if jobRepo.notifyBriefCalls[0].deliveryID != dueID {
		t.Errorf("expected due row %s enqueued, got %s", dueID, jobRepo.notifyBriefCalls[0].deliveryID)
	}
}

func TestDispatchDueDeliveries_NoDispatcher_NoOp(t *testing.T) {
	t.Parallel()

	jobRepo := &fakeJobRepoMem{}
	s := &BriefScheduler{
		deliveryRepo: newFakeDeliveryRepoMem(),
		jobRepo:      jobRepo,
		dispatcher:   nil,
		clock:        func() time.Time { return time.Now().UTC() },
	}
	s.dispatchDueDeliveries(context.Background())
	if len(jobRepo.notifyBriefCalls) != 0 {
		t.Errorf("dispatcher=nil must not enqueue, got %d", len(jobRepo.notifyBriefCalls))
	}
}

// ==================== local stubs ====================

type stubPrefs struct{ prefs briefnotify.BriefDeliveryPreferences }

func (s *stubPrefs) LoadBriefDeliveryPreferences(ctx context.Context, _ uuid.UUID) (briefnotify.BriefDeliveryPreferences, error) {
	return s.prefs, nil
}

type stubDeliverer struct{ name string }

func (s *stubDeliverer) Name() string { return s.name }
func (s *stubDeliverer) Deliver(_ context.Context, _ briefnotify.BriefDeliverContext) (string, error) {
	return "", nil
}
