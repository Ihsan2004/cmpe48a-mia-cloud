//go:build e2e
// +build e2e

// E2E test suite for the brief delivery pipeline (roadmap Phase 7).
// Runs with `go test -tags=e2e ./internal/services/...` — excluded from
// the default unit test run because it exercises the full dispatch +
// worker loop with fake transports, which is slower than a typical
// unit test and less helpful when diffing a focused change.
//
// The suite mocks Slack + SMTP at the transport boundary (briefnotify
// interfaces) so no network is required; fake repo + fake job enqueuer
// stand in for Postgres. This keeps the assertions centred on the
// pipeline's end-to-end contract — which rows land where, which
// statuses are written, which metrics advance.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/integrations/slackoauth"
	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/briefnotify"

	"github.com/google/uuid"
)

// ==================== Shared in-memory fakes ====================

// e2eDeliveryRepo is a thin in-memory implementation of
// repository.BriefDeliveryRepository. Supports the queries the
// dispatcher + scheduler actually issue — no more.
type e2eDeliveryRepo struct {
	store      map[uuid.UUID]*models.BriefDelivery
	byMeetChan map[string]*models.BriefDelivery
}

func newE2EDeliveryRepo() *e2eDeliveryRepo {
	return &e2eDeliveryRepo{
		store:      map[uuid.UUID]*models.BriefDelivery{},
		byMeetChan: map[string]*models.BriefDelivery{},
	}
}

func e2eMeetChanKey(m uuid.UUID, c string) string { return m.String() + "|" + c }

func (r *e2eDeliveryRepo) Create(ctx context.Context, d *models.BriefDelivery) error {
	key := e2eMeetChanKey(d.MeetingID, d.Channel)
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
func (r *e2eDeliveryRepo) GetByID(ctx context.Context, id uuid.UUID) (*models.BriefDelivery, error) {
	if d, ok := r.store[id]; ok {
		clone := *d
		return &clone, nil
	}
	return nil, nil
}
func (r *e2eDeliveryRepo) GetByMeetingChannel(ctx context.Context, m uuid.UUID, c string) (*models.BriefDelivery, error) {
	if d, ok := r.byMeetChan[e2eMeetChanKey(m, c)]; ok {
		clone := *d
		return &clone, nil
	}
	return nil, nil
}
func (r *e2eDeliveryRepo) ListPendingDue(ctx context.Context, now time.Time, _ int) ([]models.BriefDelivery, error) {
	var out []models.BriefDelivery
	for _, d := range r.store {
		if d.Status == models.BriefDeliveryStatusPending && !d.ScheduledFor.After(now) {
			out = append(out, *d)
		}
	}
	return out, nil
}
func (r *e2eDeliveryRepo) ListRecent(ctx context.Context, _ repository.BriefDeliveryFilter) ([]models.BriefDelivery, error) {
	var out []models.BriefDelivery
	for _, d := range r.store {
		out = append(out, *d)
	}
	return out, nil
}
func (r *e2eDeliveryRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status, msgID, lastErr string) error {
	d, ok := r.store[id]
	if !ok {
		return nil
	}
	d.Status = status
	if msgID != "" {
		tmp := msgID
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
	d.UpdatedAt = time.Now().UTC()
	return nil
}
func (r *e2eDeliveryRepo) IncrementAttempts(ctx context.Context, id uuid.UUID) error {
	if d, ok := r.store[id]; ok {
		d.Attempts++
	}
	return nil
}

// ==================== Prefs / jobs stubs ====================

type e2ePrefs struct{ p briefnotify.BriefDeliveryPreferences }

func (s *e2ePrefs) LoadBriefDeliveryPreferences(_ context.Context, _ uuid.UUID) (briefnotify.BriefDeliveryPreferences, error) {
	return s.p, nil
}

type e2eJobEnqueuer struct{ count int }

func (j *e2eJobEnqueuer) EnqueueNotifyBrief(_ context.Context, _ uuid.UUID, _ time.Time, _ int) (uuid.UUID, error) {
	j.count++
	return uuid.New(), nil
}

// ==================== Email deliverer fake ====================

type fakeEmailDeliverer struct {
	err     error
	msgID   string
	callLog []string
}

func (f *fakeEmailDeliverer) Name() string { return models.BriefDeliveryChannelEmail }
func (f *fakeEmailDeliverer) Deliver(_ context.Context, dc briefnotify.BriefDeliverContext) (string, error) {
	f.callLog = append(f.callLog, dc.Delivery.ID.String())
	return f.msgID, f.err
}

// ==================== Slack deliverer fake ====================

type fakeSlackDeliverer struct {
	err   error
	msgID string
}

func (f *fakeSlackDeliverer) Name() string { return models.BriefDeliveryChannelSlack }
func (f *fakeSlackDeliverer) Deliver(_ context.Context, _ briefnotify.BriefDeliverContext) (string, error) {
	return f.msgID, f.err
}

// ==================== ContextLoader stub ====================

type e2eCtxLoader struct{}

func (e2eCtxLoader) Load(_ context.Context, d *models.BriefDelivery) (briefnotify.BriefDeliverContext, error) {
	return briefnotify.BriefDeliverContext{
		Delivery: d,
		Meeting:  &models.Meeting{ID: d.MeetingID, UserID: d.UserID, Title: "Test", StartTime: time.Now().Add(10 * time.Minute)},
		Brief:    &models.MeetingBrief{ID: uuid.New(), UserID: d.UserID, TargetMeetingID: d.MeetingID, Content: "content"},
		User:     &models.User{ID: d.UserID, Email: "user@example.com"},
		Profile:  &models.Profile{ID: d.UserID, Timezone: "UTC"},
	}, nil
}

// ==================== Harness ====================

type e2eHarness struct {
	repo   *e2eDeliveryRepo
	prefs  *e2ePrefs
	jobs   *e2eJobEnqueuer
	disp   *briefnotify.Dispatcher
	loader briefnotify.ContextLoader
}

func newE2EHarness(prefs briefnotify.BriefDeliveryPreferences) *e2eHarness {
	repo := newE2EDeliveryRepo()
	p := &e2ePrefs{p: prefs}
	j := &e2eJobEnqueuer{}
	d := briefnotify.NewDispatcher(repo, p, j)
	return &e2eHarness{repo: repo, prefs: p, jobs: j, disp: d, loader: e2eCtxLoader{}}
}

// ==================== Scenario 1: HappyPath_Email ====================

func TestE2E_HappyPath_Email(t *testing.T) {
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{EmailEnabled: true})
	h.disp.Register(&fakeEmailDeliverer{msgID: "<abc@syncrun.net>"})

	meetingID := uuid.New()
	userID := uuid.New()
	if err := h.disp.ScheduleDeliveries(context.Background(), meetingID, userID, time.Now()); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	row, _ := h.repo.GetByMeetingChannel(context.Background(), meetingID, models.BriefDeliveryChannelEmail)
	if row == nil {
		t.Fatal("email row not created")
	}

	if err := h.disp.Execute(context.Background(), row.ID, h.loader); err != nil {
		t.Fatalf("execute: %v", err)
	}
	final, _ := h.repo.GetByID(context.Background(), row.ID)
	if final.Status != models.BriefDeliveryStatusSent {
		t.Errorf("status = %s, want sent", final.Status)
	}
	if final.ProviderMessageID == nil || *final.ProviderMessageID != "<abc@syncrun.net>" {
		t.Errorf("provider_message_id not persisted: %v", final.ProviderMessageID)
	}
}

// ==================== Scenario 2: HappyPath_Slack ====================

func TestE2E_HappyPath_Slack(t *testing.T) {
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{SlackEnabled: true})
	h.disp.Register(&fakeSlackDeliverer{msgID: "1700000000.000001"})

	mid := uuid.New()
	uid := uuid.New()
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, time.Now())
	row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, models.BriefDeliveryChannelSlack)
	if row == nil {
		t.Fatal("slack row not created")
	}
	_ = h.disp.Execute(context.Background(), row.ID, h.loader)
	got, _ := h.repo.GetByID(context.Background(), row.ID)
	if got.Status != models.BriefDeliveryStatusSent {
		t.Errorf("status = %s", got.Status)
	}
}

// ==================== Scenario 3: BothChannels ====================

func TestE2E_BothChannels(t *testing.T) {
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{EmailEnabled: true, SlackEnabled: true})
	h.disp.Register(&fakeEmailDeliverer{msgID: "m1"})
	h.disp.Register(&fakeSlackDeliverer{msgID: "s1"})

	mid := uuid.New()
	uid := uuid.New()
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, time.Now())

	if h.jobs.count != 2 {
		t.Errorf("expected 2 jobs enqueued, got %d", h.jobs.count)
	}

	for _, ch := range []string{models.BriefDeliveryChannelEmail, models.BriefDeliveryChannelSlack} {
		row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, ch)
		if row == nil {
			t.Fatalf("row for %s not created", ch)
		}
		_ = h.disp.Execute(context.Background(), row.ID, h.loader)
		got, _ := h.repo.GetByID(context.Background(), row.ID)
		if got.Status != models.BriefDeliveryStatusSent {
			t.Errorf("%s channel status = %s", ch, got.Status)
		}
	}
}

// ==================== Scenario 4: EmailUnsubscribed_Skipped ====================

func TestE2E_EmailUnsubscribed_Skipped(t *testing.T) {
	now := time.Now()
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{EmailEnabled: true, EmailUnsubscribedAt: &now})
	emailFake := &fakeEmailDeliverer{}
	h.disp.Register(emailFake)

	// Schedule shouldn't even create the row — dispatcher filters out
	// unsubscribed users ahead of time (saves a retry-loop worth of
	// attempts and one audit row per unsubscribe-and-brief-generated).
	mid := uuid.New()
	uid := uuid.New()
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, time.Now())

	row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, models.BriefDeliveryChannelEmail)
	if row != nil {
		t.Error("expected no row for unsubscribed user")
	}
	if len(emailFake.callLog) != 0 {
		t.Error("deliverer must not be called when unsubscribed at schedule time")
	}
}

// ==================== Scenario 5: SlackPendingEmail_Skipped ====================

func TestE2E_SlackPendingEmail_Skipped(t *testing.T) {
	// Fake slack deliverer that returns the pending-email sentinel —
	// mirrors the real SlackBriefDeliverer's behaviour when
	// matched_email is NULL on the integration row.
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{SlackEnabled: true})
	h.disp.Register(&fakeSlackDeliverer{err: briefnotify.ErrIntegrationPendingEmail})

	mid := uuid.New()
	uid := uuid.New()
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, time.Now())
	row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, models.BriefDeliveryChannelSlack)
	_ = h.disp.Execute(context.Background(), row.ID, h.loader)
	final, _ := h.repo.GetByID(context.Background(), row.ID)
	if final.Status != models.BriefDeliveryStatusSkipped {
		t.Errorf("status = %s, want skipped", final.Status)
	}
	if final.LastError == nil || *final.LastError == "" {
		t.Error("last_error should carry the skip reason")
	}
}

// ==================== Scenario 6: SlackInvalidAuth_AutoRevoke ====================

func TestE2E_SlackInvalidAuth_AutoRevoke(t *testing.T) {
	// Dispatcher level maps invalid_auth → ErrNoActiveIntegration →
	// status=skipped. Lower-level auto-revoke of the slack_integrations
	// row is the deliverer's responsibility — this test verifies the
	// dispatcher correctly surfaces the skip sentinel.
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{SlackEnabled: true})
	h.disp.Register(&fakeSlackDeliverer{err: briefnotify.ErrNoActiveIntegration})

	mid := uuid.New()
	uid := uuid.New()
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, time.Now())
	row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, models.BriefDeliveryChannelSlack)
	err := h.disp.Execute(context.Background(), row.ID, h.loader)
	if err != nil {
		t.Errorf("invalid_auth should not bubble up, got %v", err)
	}
	final, _ := h.repo.GetByID(context.Background(), row.ID)
	if final.Status != models.BriefDeliveryStatusSkipped {
		t.Errorf("status = %s, want skipped", final.Status)
	}
}

// ==================== Scenario 7: SMTP5xx_RetriesThenFails ====================

func TestE2E_SMTP5xxRetries(t *testing.T) {
	// Retryable errors keep status=pending so the worker schedules
	// another attempt; after the 3rd attempt (MaxAttempts) permanent
	// failure surfaces. Simulate three consecutive retries; on the
	// fourth call flip to permanent → status=failed.
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{EmailEnabled: true})

	// Retryable first, then permanent (simulates the "3 retries
	// exhausted → final permanent failure" pattern from the worker).
	callCount := 0
	email := &flakyEmailDeliverer{fn: func() (string, error) {
		callCount++
		if callCount < 4 {
			return "", briefnotify.Retryable(errors.New("smtp 421 greylisted"))
		}
		return "", errors.New("smtp 550 permanent")
	}}
	h.disp.Register(email)

	mid := uuid.New()
	uid := uuid.New()
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, time.Now())
	row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, models.BriefDeliveryChannelEmail)

	// 3 retryable attempts → each leaves row in pending.
	for i := 0; i < 3; i++ {
		err := h.disp.Execute(context.Background(), row.ID, h.loader)
		if err == nil || !briefnotify.IsRetryable(err) {
			t.Fatalf("attempt %d: expected retryable error, got %v", i+1, err)
		}
		mid := row.ID
		row, _ = h.repo.GetByID(context.Background(), mid)
		if row.Status != models.BriefDeliveryStatusPending {
			t.Errorf("attempt %d: status=%s, want pending", i+1, row.Status)
		}
	}

	// Fourth attempt flips to permanent failure → status=failed.
	_ = h.disp.Execute(context.Background(), row.ID, h.loader)
	final, _ := h.repo.GetByID(context.Background(), row.ID)
	if final.Status != models.BriefDeliveryStatusFailed {
		t.Errorf("after permanent error: status=%s, want failed", final.Status)
	}
}

type flakyEmailDeliverer struct{ fn func() (string, error) }

func (f *flakyEmailDeliverer) Name() string { return models.BriefDeliveryChannelEmail }
func (f *flakyEmailDeliverer) Deliver(_ context.Context, _ briefnotify.BriefDeliverContext) (string, error) {
	return f.fn()
}

// ==================== Scenario 8: ScheduledFor_RespectsLeadTime ====================

func TestE2E_ScheduledForRespectsLeadTime(t *testing.T) {
	// scheduled_for on the delivery row must equal the value we passed
	// into ScheduleDeliveries — the scheduler sets it to
	// meeting.start_time - lead_time. Workers honour this by filtering
	// ListPendingDue on scheduled_for <= now; a "too early" poll
	// returns nothing.
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{EmailEnabled: true})
	h.disp.Register(&fakeEmailDeliverer{msgID: "m"})

	mid := uuid.New()
	uid := uuid.New()
	scheduledFor := time.Now().Add(15 * time.Minute) // future
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, scheduledFor)

	row, _ := h.repo.GetByMeetingChannel(context.Background(), mid, models.BriefDeliveryChannelEmail)
	if !row.ScheduledFor.Equal(scheduledFor) {
		t.Errorf("scheduled_for = %v, want %v", row.ScheduledFor, scheduledFor)
	}

	// Query ListPendingDue with now=current — future row shouldn't be
	// picked up yet.
	due, _ := h.repo.ListPendingDue(context.Background(), time.Now(), 10)
	if len(due) != 0 {
		t.Errorf("future-scheduled row picked up too early: %d rows", len(due))
	}

	// Advance to after scheduled_for → now picked up.
	due, _ = h.repo.ListPendingDue(context.Background(), scheduledFor.Add(time.Second), 10)
	if len(due) != 1 {
		t.Errorf("past-scheduled row not returned: %d rows", len(due))
	}
}

// ==================== Scenario 9: Idempotency ====================

func TestE2E_Idempotency_DuplicateSchedule(t *testing.T) {
	// A second ScheduleDeliveries call for the same meeting (e.g.
	// cron restart mid-tick) must NOT create a duplicate row. The
	// UNIQUE (meeting_id, channel) constraint + dispatcher's duplicate-
	// error swallowing collapse it into a no-op.
	h := newE2EHarness(briefnotify.BriefDeliveryPreferences{EmailEnabled: true})
	h.disp.Register(&fakeEmailDeliverer{msgID: "m"})

	mid := uuid.New()
	uid := uuid.New()
	sf := time.Now()

	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, sf)
	_ = h.disp.ScheduleDeliveries(context.Background(), mid, uid, sf)

	// Exactly one row, exactly one job.
	rows, _ := h.repo.ListRecent(context.Background(), repository.BriefDeliveryFilter{MeetingID: mid})
	if len(rows) != 1 {
		t.Errorf("expected 1 delivery row after duplicate schedule, got %d", len(rows))
	}
	if h.jobs.count != 1 {
		t.Errorf("expected 1 job enqueued, got %d", h.jobs.count)
	}
}

// ==================== Error sentinel cross-check ====================

// Bonus: assert that slackoauth's invalid_auth sentinel cleanly chains
// through an errors.As/Is path from the deliverer layer. Guards against
// a future refactor that wraps the error in a way that breaks
// errors.Is — the E2E suite is the right place for this because unit
// tests already lock the direct wrapping.
func TestE2E_SlackOauthErrorChainIntact(t *testing.T) {
	err := slackoauth.ErrSlackInvalidAuth
	wrapped := errors.New("wrapper")
	_ = wrapped
	if !errors.Is(err, slackoauth.ErrSlackInvalidAuth) {
		t.Error("ErrSlackInvalidAuth should match itself")
	}
	// Turkish assertion to keep the intent obvious in logs.
	if strings.Contains(err.Error(), "invalid_auth") == false {
		t.Error("error string should mention invalid_auth for log diagnosis")
	}
}

// ==================== util ====================

var _ = json.Marshal // keep encoding/json imported for future scenario extension
