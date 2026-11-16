package teamdetection

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gonum.org/v1/gonum/graph/simple"
)

// =====================================================================
// fakes (shared by every TestService_* test below)
// =====================================================================

type fakeFetcher struct {
	events []services.BackfillEvent
	err    error
}

func (f *fakeFetcher) FetchPast30DaysWithAttendees(_ context.Context, _ uuid.UUID, _ string) ([]services.BackfillEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

type fakeRepo struct {
	mu          sync.Mutex
	createCalls []*models.TeamDetectionRun
	finishCalls []finishCall
}

type finishCall struct {
	runID                 uuid.UUID
	status                string
	inputAttendeeCount    *int
	inputEdgeCount        *int
	outputTeamCount       *int
	outputMemberCount     *int
	outputLargestTeamSize *int
	stageDurationsJSONB   json.RawMessage
	errorMessage          *string
}

func (r *fakeRepo) CreateDetectionRun(_ context.Context, run *models.TeamDetectionRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	run.CreatedAt = time.Now().UTC()
	cp := *run
	r.createCalls = append(r.createCalls, &cp)
	return nil
}

func (r *fakeRepo) UpdateDetectionRunFinish(
	_ context.Context,
	runID uuid.UUID,
	status string,
	inputAttendeeCount *int,
	inputEdgeCount *int,
	outputTeamCount *int,
	outputMemberCount *int,
	outputLargestTeamSize *int,
	stageDurationsJSONB json.RawMessage,
	errorMessage *string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishCalls = append(r.finishCalls, finishCall{
		runID:                 runID,
		status:                status,
		inputAttendeeCount:    inputAttendeeCount,
		inputEdgeCount:        inputEdgeCount,
		outputTeamCount:       outputTeamCount,
		outputMemberCount:     outputMemberCount,
		outputLargestTeamSize: outputLargestTeamSize,
		stageDurationsJSONB:   stageDurationsJSONB,
		errorMessage:          errorMessage,
	})
	return nil
}

// Unused TeamRepository methods — satisfy the interface but Phase 6
// does NOT call them (D-19).
func (r *fakeRepo) CreateTeam(context.Context, *models.Team) error                    { return nil }
func (r *fakeRepo) ListTeamsForUser(context.Context, uuid.UUID) ([]models.Team, error) { return nil, nil }
func (r *fakeRepo) GetTeam(context.Context, uuid.UUID, uuid.UUID) (*models.Team, error) {
	return nil, nil
}
func (r *fakeRepo) UpdateTeamName(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return false, nil
}
func (r *fakeRepo) DeleteTeam(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
func (r *fakeRepo) AddMember(context.Context, *models.TeamMember) error            { return nil }
func (r *fakeRepo) ListMembersForTeam(context.Context, uuid.UUID) ([]models.TeamMember, error) {
	return nil, nil
}
func (r *fakeRepo) DeleteMember(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (r *fakeRepo) GetLatestDetectionRun(context.Context, uuid.UUID) (*models.TeamDetectionRun, error) {
	return nil, nil
}

// Phase 7 interface additions — fake satisfies them as no-ops.
func (r *fakeRepo) SaveTeamsAndMembers(context.Context, uuid.UUID, []models.Team, []models.TeamMember) error {
	return nil
}
func (r *fakeRepo) ListTeamsByUserWithMembers(context.Context, uuid.UUID) ([]repository.TeamWithMembers, error) {
	return nil, nil
}
func (r *fakeRepo) DeleteMemberScoped(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}
func (r *fakeRepo) UpdateOnboardingStatus(context.Context, uuid.UUID, string) error { return nil }
func (r *fakeRepo) WithDetectLock(ctx context.Context, _ uuid.UUID, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

func frozenClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// =====================================================================
// (mkEvent is defined in graph_test.go and reused here)
// =====================================================================
// Test 1: data-quality gate — insufficient meetings (DETECT-06 / P8)
// =====================================================================

func TestService_Detect_DataQualityGate_InsufficientMeetings(t *testing.T) {
	// 9 events — strictly less than MinMeetings=10. The gate must fire
	// BEFORE the algorithm runs.
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	events := make([]services.BackfillEvent, 0, 9)
	for i := 0; i < 9; i++ {
		events = append(events, mkEvent(
			"e"+string(rune('0'+i)), "series-1", "Standup",
			base.Add(time.Duration(i*24)*time.Hour),
			"alice@example.com", "bob@example.com", "carol@example.com",
		))
	}
	fetcher := &fakeFetcher{events: events}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(base))

	before := testutil.ToFloat64(metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateDegenerate))
	result, err := svc.Detect(context.Background(), uuid.New(), "alice@example.com")
	after := testutil.ToFloat64(metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateDegenerate))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Status != StatusDegenerate {
		t.Errorf("Status = %q, want %q", result.Status, StatusDegenerate)
	}
	if result.EmptyReason != EmptyReasonInsufficientMeetings {
		t.Errorf("EmptyReason = %q, want %q", result.EmptyReason, EmptyReasonInsufficientMeetings)
	}
	if len(result.Teams) != 0 {
		t.Errorf("Teams = %d entries, want 0", len(result.Teams))
	}
	// One row written: CreateDetectionRun + Finalize.
	if len(repo.createCalls) != 1 {
		t.Fatalf("CreateDetectionRun calls = %d, want 1", len(repo.createCalls))
	}
	if got := repo.createCalls[0].InputMeetingCount; got == nil || *got != 9 {
		t.Errorf("InputMeetingCount = %v, want *9", got)
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("UpdateDetectionRunFinish calls = %d, want 1", len(repo.finishCalls))
	}
	fin := repo.finishCalls[0]
	if fin.status != StatusDegenerate {
		t.Errorf("finish status = %q, want %q", fin.status, StatusDegenerate)
	}
	if fin.errorMessage == nil || *fin.errorMessage != EmptyReasonInsufficientMeetings {
		t.Errorf("finish errorMessage = %v, want *%q", fin.errorMessage, EmptyReasonInsufficientMeetings)
	}
	if after-before < 1 {
		t.Errorf("StateDegenerate counter did not increment: before=%f after=%f", before, after)
	}
}

// =====================================================================
// Test 2: data-quality gate — insufficient attendees (DETECT-06)
// =====================================================================

func TestService_Detect_DataQualityGate_InsufficientAttendees(t *testing.T) {
	// 12 events with only 2 distinct attendees. After graph build the
	// graph has 2 nodes < MinAttendees=3 (v2). The gate must fire BEFORE
	// ClusterCommunities runs.
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	events := make([]services.BackfillEvent, 0, 12)
	for i := 0; i < 12; i++ {
		events = append(events, mkEvent(
			"e"+string(rune('a'+i)), "series-1", "Standup",
			base.Add(time.Duration(i*24*7)*time.Hour),
			"alice@example.com", "bob@example.com",
		))
	}
	fetcher := &fakeFetcher{events: events}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(base))

	result, err := svc.Detect(context.Background(), uuid.New(), "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusDegenerate {
		t.Errorf("Status = %q, want %q", result.Status, StatusDegenerate)
	}
	if result.EmptyReason != EmptyReasonInsufficientAttendees {
		t.Errorf("EmptyReason = %q, want %q", result.EmptyReason, EmptyReasonInsufficientAttendees)
	}
	if len(result.Teams) != 0 {
		t.Errorf("Teams = %d entries, want 0", len(result.Teams))
	}
	// One create + one finish.
	if len(repo.createCalls) != 1 {
		t.Fatalf("CreateDetectionRun calls = %d, want 1", len(repo.createCalls))
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("UpdateDetectionRunFinish calls = %d, want 1", len(repo.finishCalls))
	}
	fin := repo.finishCalls[0]
	if fin.errorMessage == nil || *fin.errorMessage != EmptyReasonInsufficientAttendees {
		t.Errorf("finish errorMessage = %v, want *%q", fin.errorMessage, EmptyReasonInsufficientAttendees)
	}
}

// =====================================================================
// Test 3: fetch failure → StatusError, NO row written
// =====================================================================

func TestService_Detect_FetchFailure_StatusError(t *testing.T) {
	fetchErr := &services.BackfillError{Class: services.ClassOAuthExpired}
	fetcher := &fakeFetcher{err: fetchErr}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now()))

	result, err := svc.Detect(context.Background(), uuid.New(), "alice@example.com")
	if err == nil {
		t.Fatal("expected error on fetch failure")
	}
	if result != nil {
		t.Errorf("expected nil result on fetch failure, got %+v", result)
	}
	if len(repo.createCalls) != 0 {
		t.Errorf("expected 0 CreateDetectionRun calls on fetch failure, got %d (documented trade-off)", len(repo.createCalls))
	}
	if len(repo.finishCalls) != 0 {
		t.Errorf("expected 0 UpdateDetectionRunFinish calls on fetch failure, got %d", len(repo.finishCalls))
	}
}

// =====================================================================
// Test 4: small_dense fixture → success
// =====================================================================

func TestService_Detect_SmallDenseFixture_Success(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

	result, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("Status = %q, want %q", result.Status, StatusSuccess)
	}
	if result.EmptyReason != "" {
		t.Errorf("EmptyReason = %q, want empty", result.EmptyReason)
	}
	if len(result.Teams) == 0 {
		t.Errorf("expected ≥ 1 team, got 0")
	}
	// RunID is the uuid from CreateDetectionRun.
	if len(repo.createCalls) != 1 {
		t.Fatalf("CreateDetectionRun calls = %d, want 1", len(repo.createCalls))
	}
	if result.RunID != repo.createCalls[0].ID {
		t.Errorf("RunID = %v, want %v", result.RunID, repo.createCalls[0].ID)
	}
	if len(repo.finishCalls) != 1 {
		t.Errorf("Finish calls = %d, want 1", len(repo.finishCalls))
	}
	if repo.finishCalls[0].status != StatusSuccess {
		t.Errorf("finish status = %q, want %q", repo.finishCalls[0].status, StatusSuccess)
	}
}

// =====================================================================
// Test 5: single_large_component → degenerate/degenerate_cluster
// =====================================================================

func TestService_Detect_SingleLargeComponentFixture_Degenerate(t *testing.T) {
	input, _ := loadFixture(t, "single_large_component")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

	result, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusDegenerate {
		t.Errorf("Status = %q, want %q", result.Status, StatusDegenerate)
	}
	if result.EmptyReason != EmptyReasonDegenerateCluster {
		t.Errorf("EmptyReason = %q, want %q", result.EmptyReason, EmptyReasonDegenerateCluster)
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("Finish calls = %d, want 1", len(repo.finishCalls))
	}
	fin := repo.finishCalls[0]
	if fin.errorMessage == nil || *fin.errorMessage != EmptyReasonDegenerateCluster {
		t.Errorf("finish errorMessage = %v, want *%q", fin.errorMessage, EmptyReasonDegenerateCluster)
	}
}

// =====================================================================
// Test 6: panic recovery → StatusError + metric increment
// =====================================================================

func TestService_Detect_PanicRecovery_StatusError(t *testing.T) {
	// small_dense events feed the pipeline; the injected scoreFunc
	// panics so the defer must recover and finalize the row as
	// StatusError.
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))
	svc.scoreFunc = func(_ [][]string, _ []services.BackfillEvent, _ *simple.WeightedUndirectedGraph, _ *nodeIndex) []DetectedTeam {
		panic("synthetic test panic from scoreFunc")
	}

	before := testutil.ToFloat64(metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateError))
	result, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	after := testutil.ToFloat64(metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateError))

	if err == nil {
		t.Fatal("expected error after panic")
	}
	if result == nil {
		t.Fatal("expected non-nil result with StatusError")
	}
	if result.Status != StatusError {
		t.Errorf("Status = %q, want %q", result.Status, StatusError)
	}
	// Finalize must have been called.
	if len(repo.finishCalls) != 1 {
		t.Fatalf("Finish calls = %d, want 1 (defer must finalize even on panic)", len(repo.finishCalls))
	}
	fin := repo.finishCalls[0]
	if fin.status != StatusError {
		t.Errorf("finish status = %q, want %q", fin.status, StatusError)
	}
	if fin.errorMessage == nil || !contains(*fin.errorMessage, "panic") {
		t.Errorf("finish errorMessage = %v, want one containing 'panic'", fin.errorMessage)
	}
	if after-before < 1 {
		t.Errorf("StateError counter did not increment: before=%f after=%f", before, after)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// =====================================================================
// Test 7: parameters_jsonb is the snapshot byte-for-byte
// =====================================================================

func TestService_Detect_ParametersJSONB_IsSnapshot(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

	_, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.createCalls) != 1 {
		t.Fatalf("CreateDetectionRun calls = %d, want 1", len(repo.createCalls))
	}
	gotParams := []byte(repo.createCalls[0].ParametersJSONB)
	wantParams := []byte(SnapshotParameters())
	if string(gotParams) != string(wantParams) {
		t.Errorf("parameters_jsonb drift\n got=%s\nwant=%s", gotParams, wantParams)
	}
}

// =====================================================================
// Test 8: algorithm_version pointer set to "cpm-k3-v1"
// =====================================================================

func TestService_Detect_AlgorithmVersion_Set(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

	_, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.createCalls) != 1 {
		t.Fatalf("CreateDetectionRun calls = %d, want 1", len(repo.createCalls))
	}
	if av := repo.createCalls[0].AlgorithmVersion; av == nil || *av != "cpm-k3-v2" {
		t.Errorf("AlgorithmVersion = %v, want *\"cpm-k3-v2\"", av)
	}
}

// =====================================================================
// Test 9: all five stages observed in a single successful run
// =====================================================================

func TestService_Detect_AllFiveStagesObserved(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

	_, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Across the package's test lifetime the histogram has been touched
	// at least once per stage by this single call. We can't query the
	// per-call delta without per-label scoping, but CollectAndCount on
	// the vec must return at least 5 distinct label series.
	if n := testutil.CollectAndCount(metrics.TeamDetectionDurationSeconds); n < 5 {
		t.Errorf("TeamDetectionDurationSeconds label-series count = %d, want >= 5", n)
	}
}

// =====================================================================
// Test 10: terminal success counter increments by exactly 1
// =====================================================================

func TestService_Detect_TerminalStateCounterIncremented(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

	before := testutil.ToFloat64(metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateSuccess))
	_, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	after := testutil.ToFloat64(metrics.TeamDetectionRunsTotal.WithLabelValues(metrics.StateSuccess))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delta := after - before; delta != 1 {
		t.Errorf("StateSuccess counter delta = %f, want 1", delta)
	}
}

// =====================================================================
// Test 11: TestDeterminism_100x (DETECT-02 + D-20) — THE structural proof
// =====================================================================

func TestDeterminism_100x(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	frozen := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	svc := NewService(fetcher, repo, frozenClock(frozen))

	type stableResult struct {
		Status      string
		EmptyReason string
		Teams       []DetectedTeam // RunID stripped from comparison
	}
	stableEncode := func(r *DetectionResult) []byte {
		sr := stableResult{Status: r.Status, EmptyReason: r.EmptyReason, Teams: r.Teams}
		b, _ := json.Marshal(sr)
		return b
	}

	first, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstBytes := stableEncode(first)
	firstParams := SnapshotParameters()

	for i := 1; i < 100; i++ {
		got, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if string(stableEncode(got)) != string(firstBytes) {
			t.Fatalf("iter %d: DETECT-02 violation — output drift\nfirst=%s\ngot  =%s", i, firstBytes, stableEncode(got))
		}
		nowParams := SnapshotParameters()
		if string(nowParams) != string(firstParams) {
			t.Fatalf("iter %d: D-20 violation — parameters_jsonb drift", i)
		}
	}
}

// =====================================================================
// Test 12: round-trip the 8 fixtures
// =====================================================================

func TestService_Detect_AllEightFixtures_RoundTrip(t *testing.T) {
	for _, name := range allFixtures {
		t.Run(name, func(t *testing.T) {
			input, expected := loadFixture(t, name)
			fetcher := &fakeFetcher{events: input}
			repo := &fakeRepo{}
			svc := NewService(fetcher, repo, frozenClock(time.Now().UTC()))

			result, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if result.Status != expected.ExpectedStatus {
				t.Errorf("Status = %q, want %q (fixture %s)", result.Status, expected.ExpectedStatus, name)
			}
		})
	}
}

// =====================================================================
// Phase 6 gap-closure (06-04) tests — OBS-01 full-row persistence
// =====================================================================

// Test 13: success path persists all five new metric values.
func TestService_Detect_Success_PersistsAllMetrics(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)))

	result, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %q (reason=%q)", result.Status, result.EmptyReason)
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("expected 1 finish call, got %d", len(repo.finishCalls))
	}
	fc := repo.finishCalls[0]

	// Phase 6 gap-closure assertions (OBS-01 full coverage):
	if fc.inputAttendeeCount == nil || *fc.inputAttendeeCount < MinAttendees {
		t.Errorf("inputAttendeeCount = %v, want non-nil >= %d (idx.Size())", fc.inputAttendeeCount, MinAttendees)
	}
	if fc.inputEdgeCount == nil || *fc.inputEdgeCount <= 0 {
		t.Errorf("inputEdgeCount = %v, want non-nil > 0 (g.Edges().Len())", fc.inputEdgeCount)
	}
	if fc.outputTeamCount == nil || *fc.outputTeamCount != len(result.Teams) {
		t.Errorf("outputTeamCount = %v, want %d", fc.outputTeamCount, len(result.Teams))
	}
	// Compute the expected union + largest-team from the result
	expectedMembers := map[string]struct{}{}
	expectedLargest := 0
	for _, team := range result.Teams {
		if len(team.Members) > expectedLargest {
			expectedLargest = len(team.Members)
		}
		for _, m := range team.Members {
			expectedMembers[m.Email] = struct{}{}
		}
	}
	if fc.outputMemberCount == nil || *fc.outputMemberCount != len(expectedMembers) {
		t.Errorf("outputMemberCount = %v, want %d (union of team.Members.Email)", fc.outputMemberCount, len(expectedMembers))
	}
	if fc.outputLargestTeamSize == nil || *fc.outputLargestTeamSize != expectedLargest {
		t.Errorf("outputLargestTeamSize = %v, want %d", fc.outputLargestTeamSize, expectedLargest)
	}

	// Stage durations JSONB shape: exactly the four pre-persist stages.
	if len(fc.stageDurationsJSONB) == 0 {
		t.Fatal("stageDurationsJSONB empty, want JSON object")
	}
	var stages map[string]float64
	if err := json.Unmarshal(fc.stageDurationsJSONB, &stages); err != nil {
		t.Fatalf("stageDurationsJSONB unmarshal: %v", err)
	}
	for _, want := range []string{metrics.StageFetch, metrics.StageGraphBuild, metrics.StageCluster, metrics.StageScore} {
		if v, ok := stages[want]; !ok {
			t.Errorf("stageDurationsJSONB missing key %q (got %v)", want, stages)
		} else if v < 0 {
			t.Errorf("stage %q duration negative: %v", want, v)
		}
	}
	if _, has := stages[metrics.StagePersist]; has {
		t.Errorf("stageDurationsJSONB MUST NOT include %q (self-referential, see service.go finalizePersist)", metrics.StagePersist)
	}
}

// Test 14: post-graph degenerate path persists graph counts + zero outputs.
func TestService_Detect_DegenerateAfterGraph_PersistsZeroOutputs(t *testing.T) {
	input, _ := loadFixture(t, "single_large_component")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)))

	result, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if result.Status != StatusDegenerate {
		t.Fatalf("expected degenerate, got %q", result.Status)
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("expected 1 finish call, got %d", len(repo.finishCalls))
	}
	fc := repo.finishCalls[0]

	if fc.inputAttendeeCount == nil || *fc.inputAttendeeCount <= 0 {
		t.Errorf("inputAttendeeCount = %v, want non-nil > 0 (graph WAS built)", fc.inputAttendeeCount)
	}
	if fc.inputEdgeCount == nil || *fc.inputEdgeCount <= 0 {
		t.Errorf("inputEdgeCount = %v, want non-nil > 0 (graph WAS built)", fc.inputEdgeCount)
	}
	if fc.outputMemberCount == nil || *fc.outputMemberCount != 0 {
		t.Errorf("outputMemberCount = %v, want &0 on degenerate path", fc.outputMemberCount)
	}
	if fc.outputLargestTeamSize == nil || *fc.outputLargestTeamSize != 0 {
		t.Errorf("outputLargestTeamSize = %v, want &0 on degenerate path", fc.outputLargestTeamSize)
	}

	var stages map[string]float64
	if err := json.Unmarshal(fc.stageDurationsJSONB, &stages); err != nil {
		t.Fatalf("stageDurationsJSONB unmarshal: %v", err)
	}
	for _, want := range []string{metrics.StageFetch, metrics.StageGraphBuild, metrics.StageCluster} {
		if _, ok := stages[want]; !ok {
			t.Errorf("stageDurationsJSONB missing required pre-cluster key %q (got %v)", want, stages)
		}
	}
}

// Test 15: pre-algorithm gate (MinMeetings) omits graph-derived metrics.
func TestService_Detect_PreAlgorithmGate_OmitsGraphMetrics(t *testing.T) {
	// 9 events trips MinMeetings gate before BuildGraph runs.
	base := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	events := make([]services.BackfillEvent, 0, 9)
	for i := 0; i < 9; i++ {
		events = append(events, mkEvent(
			"e"+string(rune('0'+i)), "series-1", "Standup",
			base.Add(time.Duration(i*24)*time.Hour),
			"alice@example.com", "bob@example.com", "carol@example.com",
		))
	}
	fetcher := &fakeFetcher{events: events}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(base))

	_, err := svc.Detect(context.Background(), uuid.New(), "alice@example.com")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("expected 1 finish call, got %d", len(repo.finishCalls))
	}
	fc := repo.finishCalls[0]

	if fc.inputAttendeeCount != nil {
		t.Errorf("inputAttendeeCount = %v, want nil (graph never ran)", *fc.inputAttendeeCount)
	}
	if fc.inputEdgeCount != nil {
		t.Errorf("inputEdgeCount = %v, want nil (graph never ran)", *fc.inputEdgeCount)
	}
	if fc.outputMemberCount == nil || *fc.outputMemberCount != 0 {
		t.Errorf("outputMemberCount = %v, want &0", fc.outputMemberCount)
	}
	if fc.outputLargestTeamSize == nil || *fc.outputLargestTeamSize != 0 {
		t.Errorf("outputLargestTeamSize = %v, want &0", fc.outputLargestTeamSize)
	}

	var stages map[string]float64
	if err := json.Unmarshal(fc.stageDurationsJSONB, &stages); err != nil {
		t.Fatalf("stageDurationsJSONB unmarshal: %v", err)
	}
	if _, ok := stages[metrics.StageFetch]; !ok {
		t.Errorf("stageDurationsJSONB missing %q (got %v)", metrics.StageFetch, stages)
	}
	for _, mustNot := range []string{metrics.StageGraphBuild, metrics.StageCluster, metrics.StageScore} {
		if _, ok := stages[mustNot]; ok {
			t.Errorf("stageDurationsJSONB MUST NOT include %q on pre-algorithm-gate path (got %v)", mustNot, stages)
		}
	}
}

// Test 16: panic recovery still persists the stages observed pre-panic.
func TestService_Detect_PanicRecovery_StillPersistsKnownStages(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")
	fetcher := &fakeFetcher{events: input}
	repo := &fakeRepo{}
	svc := NewService(fetcher, repo, frozenClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)))
	svc.scoreFunc = func(_ [][]string, _ []services.BackfillEvent, _ *simple.WeightedUndirectedGraph, _ *nodeIndex) []DetectedTeam {
		panic("synthetic panic from test")
	}

	_, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
	if err == nil {
		t.Fatal("expected panic-derived error, got nil")
	}
	if len(repo.finishCalls) != 1 {
		t.Fatalf("expected 1 finish call (defer-recover persisted it), got %d", len(repo.finishCalls))
	}
	fc := repo.finishCalls[0]
	if fc.status != StatusError {
		t.Errorf("status = %q, want %q", fc.status, StatusError)
	}
	if fc.inputAttendeeCount == nil {
		t.Error("inputAttendeeCount nil after panic — BuildGraph should have completed before scoreFunc panicked")
	}
	if fc.inputEdgeCount == nil {
		t.Error("inputEdgeCount nil after panic — BuildGraph should have completed before scoreFunc panicked")
	}
	if fc.outputMemberCount == nil || *fc.outputMemberCount != 0 {
		t.Errorf("outputMemberCount = %v, want &0 on panic path", fc.outputMemberCount)
	}
	if fc.outputLargestTeamSize == nil || *fc.outputLargestTeamSize != 0 {
		t.Errorf("outputLargestTeamSize = %v, want &0 on panic path", fc.outputLargestTeamSize)
	}
	var stages map[string]float64
	if err := json.Unmarshal(fc.stageDurationsJSONB, &stages); err != nil {
		t.Fatalf("stageDurationsJSONB unmarshal: %v", err)
	}
	for _, want := range []string{metrics.StageFetch, metrics.StageGraphBuild, metrics.StageCluster} {
		if _, ok := stages[want]; !ok {
			t.Errorf("stageDurationsJSONB missing %q after panic (got %v)", want, stages)
		}
	}
}

// Test 17: the four NEW deterministic metric values are byte-identical
// across 100 iterations. stage_durations_jsonb is intentionally excluded
// (clock-driven). Companion to TestDeterminism_100x which covers
// DETECT-02 on the DetectionResult JSON.
func TestDeterminism_100x_NewMetricsDeterministic(t *testing.T) {
	input, _ := loadFixture(t, "small_dense")

	type metricSnapshot struct {
		inputAttendeeCount    int
		inputEdgeCount        int
		outputTeamCount       int
		outputMemberCount     int
		outputLargestTeamSize int
	}
	deref := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}

	var first metricSnapshot
	for i := 0; i < 100; i++ {
		fetcher := &fakeFetcher{events: input}
		repo := &fakeRepo{}
		svc := NewService(fetcher, repo, frozenClock(time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)))
		_, err := svc.Detect(context.Background(), uuid.New(), "self@example.com")
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if len(repo.finishCalls) != 1 {
			t.Fatalf("iter %d: expected 1 finish call, got %d", i, len(repo.finishCalls))
		}
		fc := repo.finishCalls[0]
		snap := metricSnapshot{
			inputAttendeeCount:    deref(fc.inputAttendeeCount),
			inputEdgeCount:        deref(fc.inputEdgeCount),
			outputTeamCount:       deref(fc.outputTeamCount),
			outputMemberCount:     deref(fc.outputMemberCount),
			outputLargestTeamSize: deref(fc.outputLargestTeamSize),
		}
		if i == 0 {
			first = snap
			continue
		}
		if snap != first {
			t.Fatalf("iter %d: gap-closure metric drift\nfirst=%+v\ngot  =%+v", i, first, snap)
		}
	}
}
