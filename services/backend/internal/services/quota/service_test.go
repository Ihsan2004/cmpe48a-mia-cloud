package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// fakeRepo is a minimal in-memory UserQuotaRepository for service tests.
// Only the fields Check actually reads are populated. Embed the
// interface so future additions (admin overrides etc.) don't require
// per-test stubs unless the test exercises them.
type fakeRepo struct {
	repository.UserQuotaRepository
	snapshot  repository.UserQuotaSnapshot
	err       error
	gotAt     time.Time
	recorded  []repository.UsageLog
	recordErr error
}

func (r *fakeRepo) GetSnapshot(_ context.Context, userID uuid.UUID, at time.Time) (repository.UserQuotaSnapshot, error) {
	r.gotAt = at
	if r.err != nil {
		return repository.UserQuotaSnapshot{}, r.err
	}
	s := r.snapshot
	s.UserID = userID
	return s, nil
}

func (r *fakeRepo) RecordUsageAtomic(_ context.Context, log repository.UsageLog) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	r.recorded = append(r.recorded, log)
	return nil
}

func ptrFloat(v float64) *float64 { return &v }

func TestCheck_FreeTierUnderLimit(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	t.Setenv("QUOTA_POC_USD", "")
	t.Setenv("QUOTA_INTERNAL_USD", "")
	ResetForTest()

	cycleStart := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	cycleEnd := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:           TierFree,
		CycleAnchorDay: 17,
		CycleStartAt:   cycleStart,
		CycleEndAt:     cycleEnd,
		UsedUSD:        0.50, // 25% of $2
	}}
	svc := NewService(repo)

	status, err := svc.Check(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Tier != TierFree {
		t.Errorf("tier: got %q want %q", status.Tier, TierFree)
	}
	if status.UsedPct != 25 {
		t.Errorf("used_pct: got %d want 25", status.UsedPct)
	}
	if status.Exceeded {
		t.Errorf("should not be exceeded at 25%%")
	}
	if !status.CycleStartAt.Equal(cycleStart) || !status.CycleEndAt.Equal(cycleEnd) {
		t.Errorf("cycle window mismatch: got [%v, %v]", status.CycleStartAt, status.CycleEndAt)
	}
}

func TestCheck_FreeTierAtLimit(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:    TierFree,
		UsedUSD: 2.0, // exactly at cap
	}}
	status, err := NewService(repo).Check(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Exceeded {
		t.Errorf("used == cap should be Exceeded=true")
	}
	if status.UsedPct != 100 {
		t.Errorf("used_pct: got %d want 100", status.UsedPct)
	}
}

func TestCheck_OverrideBeatsTierDefault(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:                  TierFree,
		MonthlyUSDCapOverride: ptrFloat(10.0), // override > $2 free default
		UsedUSD:               5.0,
	}}
	status, err := NewService(repo).Check(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.UsedPct != 50 {
		t.Errorf("override $10 + used $5 → 50%%, got %d", status.UsedPct)
	}
	if status.Exceeded {
		t.Errorf("should not be exceeded at 50%%")
	}
	if status.CapUSD == nil || *status.CapUSD != 10.0 {
		t.Errorf("cap should be override 10.0, got %v", status.CapUSD)
	}
}

func TestCheck_InternalIsUnlimited(t *testing.T) {
	t.Setenv("QUOTA_INTERNAL_USD", "")
	ResetForTest()

	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:    TierInternal,
		UsedUSD: 1_000_000.0,
	}}
	status, err := NewService(repo).Check(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Exceeded {
		t.Errorf("internal tier with no cap should never be exceeded")
	}
	if status.UsedPct != 0 {
		t.Errorf("internal tier UsedPct should be 0 (unlimited), got %d", status.UsedPct)
	}
	if status.CapUSD != nil {
		t.Errorf("internal tier cap should be nil, got %v", *status.CapUSD)
	}
}

func TestCheck_OverrideZeroBlocks(t *testing.T) {
	t.Setenv("QUOTA_INTERNAL_USD", "")
	ResetForTest()

	zero := 0.0
	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:                  TierInternal,
		MonthlyUSDCapOverride: &zero,
		UsedUSD:               0.0,
	}}
	status, _ := NewService(repo).Check(context.Background(), uuid.New())
	if !status.Exceeded {
		t.Errorf("override=0 must block even at 0 usage")
	}
	if status.UsedPct != 100 {
		t.Errorf("override=0 → UsedPct=100, got %d", status.UsedPct)
	}
}

func TestCheck_PctCeil(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	// 0.01/2.0 = 0.5% → ceil → 1%
	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:    TierFree,
		UsedUSD: 0.01,
	}}
	status, _ := NewService(repo).Check(context.Background(), uuid.New())
	if status.UsedPct != 1 {
		t.Errorf("0.5%% must ceil to 1%%, got %d", status.UsedPct)
	}
}

func TestCheck_PctClampedAt100(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	// used > cap (over-spend, possible due to in-flight pipeline)
	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{
		Tier:    TierFree,
		UsedUSD: 5.0,
	}}
	status, _ := NewService(repo).Check(context.Background(), uuid.New())
	if status.UsedPct != 100 {
		t.Errorf("over-cap should clamp to 100, got %d", status.UsedPct)
	}
	if !status.Exceeded {
		t.Errorf("over-cap should be exceeded")
	}
}

func TestCheck_UserQuotaNotFound(t *testing.T) {
	repo := &fakeRepo{err: repository.ErrUserQuotaNotFound}
	_, err := NewService(repo).Check(context.Background(), uuid.New())
	if !errors.Is(err, repository.ErrUserQuotaNotFound) {
		t.Errorf("expected ErrUserQuotaNotFound, got %v", err)
	}
}

func TestCheck_UsesProvidedClock(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	fixed := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{snapshot: repository.UserQuotaSnapshot{Tier: TierFree}}
	svc := newServiceWithClock(repo, func() time.Time { return fixed })

	_, _ = svc.Check(context.Background(), uuid.New())
	if !repo.gotAt.Equal(fixed) {
		t.Errorf("clock not threaded: got %v want %v", repo.gotAt, fixed)
	}
}

func TestRecordUsage_DelegatesToRepo(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	userID := uuid.New()

	err := svc.RecordUsage(context.Background(), repository.UsageLog{
		UserID:  userID,
		Feature: "stt",
		CostUSD: 0.42,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.recorded) != 1 {
		t.Fatalf("expected 1 recorded log, got %d", len(repo.recorded))
	}
	if repo.recorded[0].UserID != userID || repo.recorded[0].CostUSD != 0.42 {
		t.Errorf("log payload mismatch: %+v", repo.recorded[0])
	}
}
