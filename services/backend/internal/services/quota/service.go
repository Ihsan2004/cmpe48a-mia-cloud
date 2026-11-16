package quota

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// Status is the read result of Check. Backend-only — never serialize this
// struct to a user-facing HTTP response (CapUSD / UsedUSD are dollar
// amounts; per Karar UI yüzde gösterir, dolar görmez).
type Status struct {
	Tier         string
	UsedUSD      float64
	CapUSD       *float64 // nil ⇒ unlimited
	UsedPct      int      // 0..100, ceil(used/cap*100), clamped
	CycleStartAt time.Time
	CycleEndAt   time.Time
	Exceeded     bool
}

// Service is the application-facing quota API.
//
//   - Check is called by RequireQuota middleware and scheduler gates
//     (Faz 3). Reads one row via UserQuotaRepository.GetSnapshot.
//   - RecordUsage is the single write path used by UsageRecorder.Record.
//     It writes usage_logs AND usage_quota_periods in one statement so
//     post-commit counter drift is bounded by transaction failure
//     (drift cron, Faz 5, reconciles the residue).
type Service interface {
	Check(ctx context.Context, userID uuid.UUID) (Status, error)
	RecordUsage(ctx context.Context, log repository.UsageLog) error
}

type service struct {
	repo repository.UserQuotaRepository
	now  func() time.Time
}

func NewService(repo repository.UserQuotaRepository) Service {
	return &service{repo: repo, now: time.Now}
}

// newServiceWithClock is a test-only constructor that lets us pin
// time.Now() — exported via _test.go to avoid leaking into production.
func newServiceWithClock(repo repository.UserQuotaRepository, now func() time.Time) Service {
	return &service{repo: repo, now: now}
}

func (s *service) Check(ctx context.Context, userID uuid.UUID) (Status, error) {
	checkStart := time.Now()
	defer func() {
		metrics.QuotaCheckDurationSeconds.Observe(time.Since(checkStart).Seconds())
	}()

	snap, err := s.repo.GetSnapshot(ctx, userID, s.now())
	if err != nil {
		// Tier unknown on error path; the "error" result bucket is the
		// only signal we publish here. Caller (middleware/scheduler)
		// owns the fail-open decision.
		metrics.QuotaChecks.WithLabelValues("unknown", metrics.QuotaResultError).Inc()
		if errors.Is(err, repository.ErrUserQuotaNotFound) {
			return Status{}, err
		}
		return Status{}, fmt.Errorf("quota check: %w", err)
	}

	cap := ResolveCap(snap.Tier, snap.MonthlyUSDCapOverride)

	st := Status{
		Tier:         snap.Tier,
		UsedUSD:      snap.UsedUSD,
		CapUSD:       cap,
		CycleStartAt: snap.CycleStartAt,
		CycleEndAt:   snap.CycleEndAt,
	}

	switch {
	case cap == nil:
		// Unlimited (internal tier or explicit unlimited override absent).
		st.UsedPct = 0
		st.Exceeded = false
	case *cap == 0:
		// Override set to 0 explicitly blocks the user.
		st.UsedPct = 100
		st.Exceeded = true
	default:
		pct := int(math.Ceil(snap.UsedUSD / *cap * 100))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		st.UsedPct = pct
		st.Exceeded = snap.UsedUSD >= *cap
	}

	result := metrics.QuotaResultAllow
	if st.Exceeded {
		result = metrics.QuotaResultDeny
	}
	metrics.QuotaChecks.WithLabelValues(snap.Tier, result).Inc()

	return st, nil
}

func (s *service) RecordUsage(ctx context.Context, log repository.UsageLog) error {
	return s.repo.RecordUsageAtomic(ctx, log)
}
