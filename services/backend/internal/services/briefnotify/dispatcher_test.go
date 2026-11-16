package briefnotify

import (
	"context"
	"errors"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== Fakes ====================

type fakeDeliveryRepo struct {
	store       map[uuid.UUID]*models.BriefDelivery
	byMeetChan  map[string]*models.BriefDelivery
	failCreate  error
	increments  int
	statusCalls []statusCall
}

type statusCall struct {
	id       uuid.UUID
	status   string
	msgID    string
	lastErr  string
}

func newFakeDeliveryRepo() *fakeDeliveryRepo {
	return &fakeDeliveryRepo{
		store:      map[uuid.UUID]*models.BriefDelivery{},
		byMeetChan: map[string]*models.BriefDelivery{},
	}
}

func meetChanKey(meetingID uuid.UUID, channel string) string {
	return meetingID.String() + "|" + channel
}

func (r *fakeDeliveryRepo) Create(ctx context.Context, d *models.BriefDelivery) error {
	if r.failCreate != nil {
		return r.failCreate
	}
	key := meetChanKey(d.MeetingID, d.Channel)
	if _, exists := r.byMeetChan[key]; exists {
		return repository.ErrBriefDeliveryDuplicate
	}
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	clone := *d
	r.store[d.ID] = &clone
	r.byMeetChan[key] = &clone
	return nil
}

func (r *fakeDeliveryRepo) GetByID(ctx context.Context, id uuid.UUID) (*models.BriefDelivery, error) {
	d, ok := r.store[id]
	if !ok {
		return nil, nil
	}
	clone := *d
	return &clone, nil
}

func (r *fakeDeliveryRepo) GetByMeetingChannel(ctx context.Context, meetingID uuid.UUID, channel string) (*models.BriefDelivery, error) {
	d, ok := r.byMeetChan[meetChanKey(meetingID, channel)]
	if !ok {
		return nil, nil
	}
	clone := *d
	return &clone, nil
}

func (r *fakeDeliveryRepo) ListPendingDue(ctx context.Context, now time.Time, limit int) ([]models.BriefDelivery, error) {
	var out []models.BriefDelivery
	for _, d := range r.store {
		if d.Status == models.BriefDeliveryStatusPending && !d.ScheduledFor.After(now) {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (r *fakeDeliveryRepo) ListRecent(ctx context.Context, filter repository.BriefDeliveryFilter) ([]models.BriefDelivery, error) {
	var out []models.BriefDelivery
	for _, d := range r.store {
		if filter.MeetingID != uuid.Nil && d.MeetingID != filter.MeetingID {
			continue
		}
		if filter.UserID != uuid.Nil && d.UserID != filter.UserID {
			continue
		}
		if filter.Channel != "" && d.Channel != filter.Channel {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		out = append(out, *d)
	}
	return out, nil
}

func (r *fakeDeliveryRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status, providerMsgID, lastErr string) error {
	r.statusCalls = append(r.statusCalls, statusCall{id: id, status: status, msgID: providerMsgID, lastErr: lastErr})
	d, ok := r.store[id]
	if !ok {
		return nil
	}
	d.Status = status
	if providerMsgID != "" {
		tmp := providerMsgID
		d.ProviderMessageID = &tmp
	}
	if lastErr != "" {
		tmp := lastErr
		d.LastError = &tmp
	}
	if status == models.BriefDeliveryStatusSent {
		now := time.Now().UTC()
		d.SentAt = &now
	}
	return nil
}

func (r *fakeDeliveryRepo) IncrementAttempts(ctx context.Context, id uuid.UUID) error {
	r.increments++
	if d, ok := r.store[id]; ok {
		d.Attempts++
	}
	return nil
}

type fakePrefs struct {
	prefs BriefDeliveryPreferences
	err   error
}

func (f *fakePrefs) LoadBriefDeliveryPreferences(ctx context.Context, userID uuid.UUID) (BriefDeliveryPreferences, error) {
	return f.prefs, f.err
}

type fakeJobEnqueuer struct {
	calls []uuid.UUID
	err   error
}

func (f *fakeJobEnqueuer) EnqueueNotifyBrief(ctx context.Context, deliveryID uuid.UUID, scheduledAt time.Time, maxAttempts int) (uuid.UUID, error) {
	f.calls = append(f.calls, deliveryID)
	if f.err != nil {
		return uuid.Nil, f.err
	}
	return uuid.New(), nil
}

type fakeDeliverer struct {
	name        string
	msgID       string
	err         error
	deliverCall int
}

func (f *fakeDeliverer) Name() string { return f.name }

func (f *fakeDeliverer) Deliver(ctx context.Context, dc BriefDeliverContext) (string, error) {
	f.deliverCall++
	return f.msgID, f.err
}

type fakeContextLoader struct {
	dc  BriefDeliverContext
	err error
}

func (f *fakeContextLoader) Load(ctx context.Context, delivery *models.BriefDelivery) (BriefDeliverContext, error) {
	if f.err != nil {
		return BriefDeliverContext{}, f.err
	}
	f.dc.Delivery = delivery
	return f.dc, nil
}

// ==================== Dispatcher.ScheduleDeliveries ====================

func TestDispatcher_ScheduleDeliveries_EmailOnly(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	prefs := &fakePrefs{prefs: BriefDeliveryPreferences{EmailEnabled: true}}
	jobs := &fakeJobEnqueuer{}

	d := NewDispatcher(repo, prefs, jobs)
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelSlack})

	meetingID := uuid.New()
	userID := uuid.New()
	scheduledFor := time.Now().Add(15 * time.Minute)

	if err := d.ScheduleDeliveries(context.Background(), meetingID, userID, scheduledFor); err != nil {
		t.Fatalf("ScheduleDeliveries returned error: %v", err)
	}

	// Only email row should be created — Slack toggle is off.
	if _, ok := repo.byMeetChan[meetChanKey(meetingID, models.BriefDeliveryChannelEmail)]; !ok {
		t.Error("expected an email delivery row")
	}
	if _, ok := repo.byMeetChan[meetChanKey(meetingID, models.BriefDeliveryChannelSlack)]; ok {
		t.Error("did not expect a slack delivery row")
	}
	if len(jobs.calls) != 1 {
		t.Errorf("expected exactly 1 job enqueued, got %d", len(jobs.calls))
	}
}

func TestDispatcher_ScheduleDeliveries_BothChannels(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	prefs := &fakePrefs{prefs: BriefDeliveryPreferences{EmailEnabled: true, SlackEnabled: true}}
	jobs := &fakeJobEnqueuer{}

	d := NewDispatcher(repo, prefs, jobs)
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelSlack})

	if err := d.ScheduleDeliveries(context.Background(), uuid.New(), uuid.New(), time.Now().Add(15*time.Minute)); err != nil {
		t.Fatalf("ScheduleDeliveries: %v", err)
	}

	if len(repo.store) != 2 {
		t.Errorf("expected 2 delivery rows, got %d", len(repo.store))
	}
	if len(jobs.calls) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(jobs.calls))
	}
}

func TestDispatcher_ScheduleDeliveries_EmailUnsubscribed_Skipped(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	now := time.Now()
	prefs := &fakePrefs{prefs: BriefDeliveryPreferences{
		EmailEnabled:        true,
		EmailUnsubscribedAt: &now, // unsubscribed → email channel skipped
	}}
	jobs := &fakeJobEnqueuer{}

	d := NewDispatcher(repo, prefs, jobs)
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})

	if err := d.ScheduleDeliveries(context.Background(), uuid.New(), uuid.New(), time.Now().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(repo.store) != 0 {
		t.Errorf("expected zero rows when user is unsubscribed, got %d", len(repo.store))
	}
}

func TestDispatcher_ScheduleDeliveries_NoDeliverers_NoRows(t *testing.T) {
	t.Parallel()

	// Phase 1 default: dispatcher exists but no transports registered.
	// ScheduleDeliveries MUST be a no-op — otherwise we'd enqueue jobs
	// the worker can never complete.
	repo := newFakeDeliveryRepo()
	prefs := &fakePrefs{prefs: BriefDeliveryPreferences{EmailEnabled: true, SlackEnabled: true}}
	jobs := &fakeJobEnqueuer{}

	d := NewDispatcher(repo, prefs, jobs)

	if err := d.ScheduleDeliveries(context.Background(), uuid.New(), uuid.New(), time.Now().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(repo.store) != 0 || len(jobs.calls) != 0 {
		t.Errorf("expected zero rows and zero jobs, got rows=%d jobs=%d", len(repo.store), len(jobs.calls))
	}
}

func TestDispatcher_ScheduleDeliveries_ChannelDisabledSkippedEvenIfDelivererRegistered(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	// User opted into email only, Slack toggle off.
	prefs := &fakePrefs{prefs: BriefDeliveryPreferences{EmailEnabled: true, SlackEnabled: false}}
	jobs := &fakeJobEnqueuer{}

	d := NewDispatcher(repo, prefs, jobs)
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelSlack})

	_ = d.ScheduleDeliveries(context.Background(), uuid.New(), uuid.New(), time.Now().Add(15*time.Minute))

	if len(repo.store) != 1 {
		t.Fatalf("expected 1 delivery row (email only), got %d", len(repo.store))
	}
	for _, d := range repo.store {
		if d.Channel != models.BriefDeliveryChannelEmail {
			t.Errorf("expected email channel, got %q", d.Channel)
		}
	}
}

func TestDispatcher_ScheduleDeliveries_IdempotentOnDuplicate(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	prefs := &fakePrefs{prefs: BriefDeliveryPreferences{EmailEnabled: true}}
	jobs := &fakeJobEnqueuer{}

	d := NewDispatcher(repo, prefs, jobs)
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})

	meetingID := uuid.New()
	userID := uuid.New()
	sf := time.Now().Add(15 * time.Minute)

	// First call → row created + job enqueued.
	if err := d.ScheduleDeliveries(context.Background(), meetingID, userID, sf); err != nil {
		t.Fatal(err)
	}
	// Second call → unique violation treated as no-op.
	if err := d.ScheduleDeliveries(context.Background(), meetingID, userID, sf); err != nil {
		t.Fatalf("second call must swallow duplicate: %v", err)
	}

	if len(repo.store) != 1 {
		t.Errorf("expected 1 delivery row after duplicate, got %d", len(repo.store))
	}
	if len(jobs.calls) != 1 {
		t.Errorf("expected 1 job enqueue (second duplicate skips), got %d", len(jobs.calls))
	}
}

func TestDispatcher_ScheduleDeliveries_ValidatesArgs(t *testing.T) {
	t.Parallel()
	d := NewDispatcher(newFakeDeliveryRepo(), &fakePrefs{}, &fakeJobEnqueuer{})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})

	if err := d.ScheduleDeliveries(context.Background(), uuid.Nil, uuid.New(), time.Now()); err == nil {
		t.Error("expected error on nil meetingID")
	}
	if err := d.ScheduleDeliveries(context.Background(), uuid.New(), uuid.Nil, time.Now()); err == nil {
		t.Error("expected error on nil userID")
	}
	if err := d.ScheduleDeliveries(context.Background(), uuid.New(), uuid.New(), time.Time{}); err == nil {
		t.Error("expected error on zero scheduledFor")
	}
}

// ==================== Dispatcher.Execute ====================

func TestDispatcher_Execute_HappyPath_Sent(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})
	deliverer := &fakeDeliverer{name: models.BriefDeliveryChannelEmail, msgID: "msg-123"}
	d.Register(deliverer)

	// Seed a pending delivery row.
	delivery := &models.BriefDelivery{
		MeetingID:    uuid.New(),
		UserID:       uuid.New(),
		Channel:      models.BriefDeliveryChannelEmail,
		Status:       models.BriefDeliveryStatusPending,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)

	loader := &fakeContextLoader{}
	if err := d.Execute(context.Background(), delivery.ID, loader); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if deliverer.deliverCall != 1 {
		t.Errorf("expected Deliver called once, got %d", deliverer.deliverCall)
	}
	if got := repo.store[delivery.ID].Status; got != models.BriefDeliveryStatusSent {
		t.Errorf("expected status=sent, got %s", got)
	}
	if got := repo.store[delivery.ID].ProviderMessageID; got == nil || *got != "msg-123" {
		t.Errorf("expected provider_message_id=msg-123, got %v", got)
	}
}

func TestDispatcher_Execute_RetryableError_StaysPending(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})
	deliverer := &fakeDeliverer{name: models.BriefDeliveryChannelEmail, err: Retryable(errors.New("smtp 421"))}
	d.Register(deliverer)

	delivery := &models.BriefDelivery{
		MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)

	err := d.Execute(context.Background(), delivery.ID, &fakeContextLoader{})
	if err == nil || !IsRetryable(err) {
		t.Fatalf("expected retryable error to bubble up, got %v", err)
	}
	if got := repo.store[delivery.ID].Status; got != models.BriefDeliveryStatusPending {
		t.Errorf("expected status stays pending, got %s", got)
	}
}

func TestDispatcher_Execute_PermanentError_MarkedFailed(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail, err: errors.New("smtp 550 bad address")})

	delivery := &models.BriefDelivery{
		MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)

	err := d.Execute(context.Background(), delivery.ID, &fakeContextLoader{})
	if err == nil {
		t.Fatal("expected non-retryable error to bubble up")
	}
	if got := repo.store[delivery.ID].Status; got != models.BriefDeliveryStatusFailed {
		t.Errorf("expected status=failed, got %s", got)
	}
}

func TestDispatcher_Execute_SkipSentinel_MarkedSkipped(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelSlack, err: ErrNoActiveIntegration})

	delivery := &models.BriefDelivery{
		MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelSlack, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)

	if err := d.Execute(context.Background(), delivery.ID, &fakeContextLoader{}); err != nil {
		t.Fatalf("skip sentinel should not bubble up as error, got %v", err)
	}
	if got := repo.store[delivery.ID].Status; got != models.BriefDeliveryStatusSkipped {
		t.Errorf("expected status=skipped, got %s", got)
	}
}

func TestDispatcher_Execute_NoDeliverer_Skipped(t *testing.T) {
	t.Parallel()

	// Scenario: a stale notify_brief row exists (from a previous deploy)
	// but the deliverer for its channel is no longer registered. We
	// mark it skipped rather than loop forever.
	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})

	delivery := &models.BriefDelivery{
		MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)

	if err := d.Execute(context.Background(), delivery.ID, &fakeContextLoader{}); err != nil {
		t.Fatalf("expected no error for orphaned channel, got %v", err)
	}
	if got := repo.store[delivery.ID].Status; got != models.BriefDeliveryStatusSkipped {
		t.Errorf("expected status=skipped, got %s", got)
	}
}

func TestDispatcher_Execute_TerminalStatus_NoOp(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})
	deliverer := &fakeDeliverer{name: models.BriefDeliveryChannelEmail, msgID: "x"}
	d.Register(deliverer)

	// Pre-mark the row as 'sent'. Execute must not call Deliver again.
	delivery := &models.BriefDelivery{
		MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusSent,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)
	repo.store[delivery.ID].Status = models.BriefDeliveryStatusSent

	if err := d.Execute(context.Background(), delivery.ID, &fakeContextLoader{}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if deliverer.deliverCall != 0 {
		t.Errorf("expected Deliver not called for terminal delivery, got %d", deliverer.deliverCall)
	}
}

func TestDispatcher_Execute_ContextLoadError_Failed(t *testing.T) {
	t.Parallel()

	repo := newFakeDeliveryRepo()
	d := NewDispatcher(repo, &fakePrefs{}, &fakeJobEnqueuer{})
	d.Register(&fakeDeliverer{name: models.BriefDeliveryChannelEmail})

	delivery := &models.BriefDelivery{
		MeetingID: uuid.New(), UserID: uuid.New(),
		Channel: models.BriefDeliveryChannelEmail, Status: models.BriefDeliveryStatusPending,
		ScheduledFor: time.Now(),
	}
	_ = repo.Create(context.Background(), delivery)

	loader := &fakeContextLoader{err: errors.New("db down")}
	err := d.Execute(context.Background(), delivery.ID, loader)
	if err == nil {
		t.Fatal("expected context-load error to bubble up")
	}
	if got := repo.store[delivery.ID].Status; got != models.BriefDeliveryStatusFailed {
		t.Errorf("expected status=failed after loader err, got %s", got)
	}
}

// ==================== Retryable helpers ====================

func TestRetryable_WrapsAndUnwraps(t *testing.T) {
	t.Parallel()

	inner := errors.New("upstream 429")
	err := Retryable(inner)

	if err == nil || !IsRetryable(err) {
		t.Error("Retryable(err) must be identified as retryable")
	}
	if !errors.Is(err, inner) {
		t.Error("Retryable must preserve errors.Is() chain")
	}

	if Retryable(nil) != nil {
		t.Error("Retryable(nil) must return nil")
	}
}

func TestIsRetryable_Negative(t *testing.T) {
	t.Parallel()
	if IsRetryable(nil) {
		t.Error("IsRetryable(nil) should be false")
	}
	if IsRetryable(errors.New("plain")) {
		t.Error("IsRetryable(plain error) should be false")
	}
}
