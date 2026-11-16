//go:build load
// +build load

// Synthetic load test for the quota hot path (issue #39 Faz 6).
// Run with `go test -tags=load -count=1 ./internal/services/quota/`.
//
// Goal: prove the QuotaService.Check + RecordUsage code path has no
// latent goroutine-safety issue under contention (race detector
// optional but recommended) and surface a baseline p50/p99 number so
// future changes can be compared against a known good.
//
// Repo is the same in-memory fake the unit/E2E tests use — we want to
// measure the service + locking + metrics overhead, not Postgres
// latency. A separate DB-bound benchmark belongs in a perf harness.

package quota

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// concurrentRepo: minimal thread-safe stand-in for the production repo
// so QuotaService can hammer it without a real DB.
type concurrentRepo struct {
	mu   sync.Mutex
	used map[uuid.UUID]float64
}

func (r *concurrentRepo) GetSnapshot(_ context.Context, userID uuid.UUID, _ time.Time) (repository.UserQuotaSnapshot, error) {
	r.mu.Lock()
	u := r.used[userID]
	r.mu.Unlock()
	return repository.UserQuotaSnapshot{
		UserID:         userID,
		Tier:           TierFree,
		CycleAnchorDay: 1,
		CycleStartAt:   time.Now().AddDate(0, -1, 0),
		CycleEndAt:     time.Now().AddDate(0, 1, 0),
		UsedUSD:        u,
	}, nil
}

func (r *concurrentRepo) RecordUsageAtomic(_ context.Context, log repository.UsageLog) error {
	r.mu.Lock()
	r.used[log.UserID] += log.CostUSD
	r.mu.Unlock()
	return nil
}

func (r *concurrentRepo) ApplyAdminUpdate(_ context.Context, _ uuid.UUID, _ repository.AdminQuotaUpdate) (repository.UserQuotaSnapshot, error) {
	return repository.UserQuotaSnapshot{}, nil
}

// TestLoad_1000ConcurrentChecks fires N goroutines each running M
// QuotaService.Check calls back-to-back. Asserts:
//
//   - no goroutine returns an error
//   - p50 latency under 5ms (in-memory repo; budget keeps regressions
//     visible without flaking on slow CI)
//   - all calls land an "allow" result (UsedUSD stays at 0 since the
//     synthetic load only Checks)
//
// 1000 concurrent workers × 100 checks each = 100k Check calls. With
// the default 8-core CI this finishes in under a second.
func TestLoad_1000ConcurrentChecks(t *testing.T) {
	t.Setenv("QUOTA_FREE_USD", "")
	ResetForTest()

	const (
		workers        = 1000
		perWorker      = 100
		p50BudgetMs    = 5
		p99BudgetMs    = 25
	)

	repo := &concurrentRepo{used: map[uuid.UUID]float64{}}
	svc := NewService(repo)

	user := uuid.New()
	repo.used[user] = 0

	latencies := make([]time.Duration, workers*perWorker)
	var failures atomic.Int64

	var wg sync.WaitGroup
	wg.Add(workers)
	start := time.Now()
	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			base := workerID * perWorker
			for i := 0; i < perWorker; i++ {
				callStart := time.Now()
				st, err := svc.Check(context.Background(), user)
				latencies[base+i] = time.Since(callStart)
				if err != nil || st.Exceeded {
					failures.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()
	total := time.Since(start)

	if f := failures.Load(); f != 0 {
		t.Fatalf("expected 0 failures, got %d (out of %d calls)", f, workers*perWorker)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[len(latencies)/2]
	p99 := latencies[len(latencies)*99/100]
	p999 := latencies[len(latencies)*999/1000]

	t.Logf("load: %d workers × %d checks = %d calls in %v",
		workers, perWorker, workers*perWorker, total)
	t.Logf("latency: p50=%v p99=%v p99.9=%v max=%v",
		p50, p99, p999, latencies[len(latencies)-1])
	t.Logf("throughput: %.0f checks/sec",
		float64(workers*perWorker)/total.Seconds())

	// Budgets are intentionally loose — the test exists to catch
	// regressions of 10x+, not to enforce a microsecond target.
	if p50 > p50BudgetMs*time.Millisecond {
		t.Errorf("p50 latency %v exceeds budget %vms", p50, p50BudgetMs)
	}
	if p99 > p99BudgetMs*time.Millisecond {
		t.Errorf("p99 latency %v exceeds budget %vms", p99, p99BudgetMs)
	}
}

// TestLoad_RecordUsageAtomicConcurrent: counter increment must add up
// to exactly (workers × perWorker × cost) — proves no lost writes
// under contention on the in-memory repo. The production repo has the
// same atomic UPSERT guarantee at the Postgres level; this test
// exercises the service layer.
func TestLoad_RecordUsageAtomicConcurrent(t *testing.T) {
	repo := &concurrentRepo{used: map[uuid.UUID]float64{}}
	svc := NewService(repo)

	const (
		workers   = 200
		perWorker = 50
		cost      = 0.01
	)
	user := uuid.New()
	repo.used[user] = 0

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				_ = svc.RecordUsage(context.Background(), repository.UsageLog{
					UserID:  user,
					Feature: "load_test",
					CostUSD: cost,
				})
			}
		}()
	}
	wg.Wait()

	want := float64(workers*perWorker) * cost
	got := repo.used[user]
	// Float64 accumulation noise tolerance.
	if diff := got - want; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("counter convergence: got %v want %v", got, want)
	}
}
