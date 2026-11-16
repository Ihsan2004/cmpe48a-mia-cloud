package services

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== fakes ====================

type fakeCentroidsRepo struct {
	cache          map[uuid.UUID]*repository.ChainCentroid
	upsertCalls    []upsertCentroidCall
	eligible       []repository.EligibleChainRow
	eligibleErr    error
	chainTexts     map[uuid.UUID][]string
	mostRecent     map[uuid.UUID]time.Time
	getCentroidErr error
}

type upsertCentroidCall struct {
	chainID uuid.UUID
	dim     int
	count   int
}

func (f *fakeCentroidsRepo) GetCentroid(_ context.Context, chainID uuid.UUID) (*repository.ChainCentroid, error) {
	if f.getCentroidErr != nil {
		return nil, f.getCentroidErr
	}
	if c, ok := f.cache[chainID]; ok {
		return c, nil
	}
	return nil, repository.ErrChainCentroidNotFound
}

func (f *fakeCentroidsRepo) UpsertCentroid(_ context.Context, chainID uuid.UUID, centroid []float64, count int) error {
	f.upsertCalls = append(f.upsertCalls, upsertCentroidCall{chainID, len(centroid), count})
	if f.cache == nil {
		f.cache = map[uuid.UUID]*repository.ChainCentroid{}
	}
	f.cache[chainID] = &repository.ChainCentroid{
		ChainID:             chainID,
		Centroid:            centroid,
		SourceDecisionCount: count,
		ComputedAt:          time.Now(),
	}
	return nil
}

func (f *fakeCentroidsRepo) GetEligibleChainsForUser(_ context.Context, _, _ uuid.UUID, _ int, _ time.Duration) ([]repository.EligibleChainRow, error) {
	return f.eligible, f.eligibleErr
}

func (f *fakeCentroidsRepo) GetRecentDecisionTextsForChain(_ context.Context, chainID uuid.UUID, _ int) ([]string, error) {
	return f.chainTexts[chainID], nil
}

func (f *fakeCentroidsRepo) MostRecentDecisionTimeForChain(_ context.Context, chainID uuid.UUID) (time.Time, error) {
	return f.mostRecent[chainID], nil
}

type fakeEmbedder struct {
	// embeddings keyed by exact text. Tests pre-load expected texts so
	// the centroid math has known inputs.
	embeddings map[string][]float64
	calls      [][]string
	// Per-call attribution captured for issue #39 assertions: stage3
	// must always pass the meeting owner's UUID (never uuid.Nil) so
	// the embedding cost lands in the right tenant's usage_logs.
	callUsers    []uuid.UUID
	callMeetings []*uuid.UUID
	err          error
}

func (f *fakeEmbedder) EmbedTexts(_ context.Context, userID uuid.UUID, meetingID *uuid.UUID, texts []string) ([][]float64, error) {
	f.calls = append(f.calls, append([]string(nil), texts...))
	f.callUsers = append(f.callUsers, userID)
	f.callMeetings = append(f.callMeetings, meetingID)
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if v, ok := f.embeddings[t]; ok {
			out[i] = v
		}
	}
	return out, nil
}

type fakeItemRepoForStage3 struct {
	repository.DecisionRepository
	byMeeting       map[uuid.UUID][]models.DecisionItem
	cascadedChainID map[uuid.UUID]uuid.UUID // meeting → chain assigned via cascade
}

func (f *fakeItemRepoForStage3) GetDecisionItemsByMeeting(_ context.Context, meetingID uuid.UUID) ([]models.DecisionItem, error) {
	return f.byMeeting[meetingID], nil
}
func (f *fakeItemRepoForStage3) UpdateChainIDForMeeting(_ context.Context, meetingID, chainID uuid.UUID) error {
	if f.cascadedChainID == nil {
		f.cascadedChainID = map[uuid.UUID]uuid.UUID{}
	}
	f.cascadedChainID[meetingID] = chainID
	return nil
}

// fakeMeetingRepoForStage3 captures SetChainAssignment writes from
// the auto-attach path (Phase 6 cutover).
type fakeMeetingRepoForStage3 struct {
	repository.MeetingRepository
	assignments map[uuid.UUID]repository.SetChainAssignmentInput
	notOwned    bool
}

func (f *fakeMeetingRepoForStage3) SetChainAssignment(_ context.Context, _, meetingID uuid.UUID, in repository.SetChainAssignmentInput) (bool, error) {
	if f.notOwned {
		return false, nil
	}
	if f.assignments == nil {
		f.assignments = map[uuid.UUID]repository.SetChainAssignmentInput{}
	}
	f.assignments[meetingID] = in
	return true, nil
}

func newStage3WithFakes() (*ChainMatcherV3Stage3, *fakeCentroidsRepo, *fakeEmbedder, *fakeDecisionsRepo, *fakeItemRepoForStage3, *fakeMeetingRepoForStage3) {
	cr := &fakeCentroidsRepo{cache: map[uuid.UUID]*repository.ChainCentroid{}, chainTexts: map[uuid.UUID][]string{}, mostRecent: map[uuid.UUID]time.Time{}}
	emb := &fakeEmbedder{embeddings: map[string][]float64{}}
	dec := &fakeDecisionsRepo{}
	ir := &fakeItemRepoForStage3{byMeeting: map[uuid.UUID][]models.DecisionItem{}}
	mr := &fakeMeetingRepoForStage3{}
	m := &ChainMatcherV3Stage3{
		chainRepo:     &fakeChainRepo{},
		meetingRepo:   mr,
		centroidsRepo: cr,
		decisionsRepo: dec,
		itemRepo:      ir,
		embedder:      emb,
	}
	return m, cr, emb, dec, ir, mr
}

// twoDimAxis returns a vector pointing along axis `i` of dimension `dim`.
// Lets tests build orthogonal / parallel pairs for cosine assertions.
func twoDimAxis(dim, i int) []float64 {
	v := make([]float64, dim)
	v[i] = 1
	return v
}

// ==================== meanEmbedding / cosineSimilarity ====================

func TestStage3_MeanEmbedding_AveragesElementWise(t *testing.T) {
	t.Parallel()
	got, err := meanEmbedding([][]float64{
		{1, 2, 3},
		{3, 2, 1},
	})
	if err != nil {
		t.Fatalf("mean failed: %v", err)
	}
	want := []float64{2, 2, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dim %d: want %f got %f", i, want[i], got[i])
		}
	}
}

func TestStage3_MeanEmbedding_FiltersNil(t *testing.T) {
	t.Parallel()
	got, err := meanEmbedding([][]float64{
		{2, 2},
		nil,
		{4, 4},
	})
	if err != nil {
		t.Fatalf("mean failed: %v", err)
	}
	if got[0] != 3 || got[1] != 3 {
		t.Errorf("expected mean of non-nil vectors only, got %v", got)
	}
}

func TestStage3_MeanEmbedding_RejectsAllNil(t *testing.T) {
	t.Parallel()
	if _, err := meanEmbedding([][]float64{nil, nil}); err == nil {
		t.Error("expected error when no usable vectors")
	}
}

func TestStage3_CosineSimilarity_Identical(t *testing.T) {
	t.Parallel()
	c, err := cosineSimilarity([]float64{1, 2, 3}, []float64{1, 2, 3})
	if err != nil {
		t.Fatalf("cosine failed: %v", err)
	}
	if math.Abs(c-1.0) > 1e-9 {
		t.Errorf("expected 1.0 for identical vectors, got %f", c)
	}
}

func TestStage3_CosineSimilarity_Orthogonal(t *testing.T) {
	t.Parallel()
	c, _ := cosineSimilarity(twoDimAxis(3, 0), twoDimAxis(3, 1))
	if math.Abs(c) > 1e-9 {
		t.Errorf("expected ~0 for orthogonal vectors, got %f", c)
	}
}

func TestStage3_CosineSimilarity_DimMismatchRejected(t *testing.T) {
	t.Parallel()
	if _, err := cosineSimilarity([]float64{1, 0}, []float64{1, 0, 0}); err == nil {
		t.Error("expected dim-mismatch error")
	}
}

// ==================== EvaluateAndApply guards ====================

func TestStage3_Skip_AlreadyChained(t *testing.T) {
	t.Parallel()
	m, _, emb, dec, _, mr := newStage3WithFakes()
	chainID := uuid.New()
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.ChainID = &chainID

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(emb.calls) != 0 {
		t.Error("expected no embedder calls when meeting already chained")
	}
	if len(dec.rows) != 0 {
		t.Error("expected no rows for already-chained meeting")
	}
	if len(mr.assignments) != 0 {
		t.Error("expected no SetChainAssignment for already-chained meeting")
	}
}

func TestStage3_Skip_ManualRemovedLock(t *testing.T) {
	t.Parallel()
	m, _, emb, dec, _, mr := newStage3WithFakes()
	method := models.ChainAssignmentMethodManualRemoved
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.ChainAssignmentMethod = &method

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(emb.calls) != 0 || len(dec.rows) != 0 || len(mr.assignments) != 0 {
		t.Error("expected no work for manual_removed lock")
	}
}

func TestStage3_Skip_BelowMeetingDecisionFloor(t *testing.T) {
	t.Parallel()
	m, _, emb, dec, ir, _ := newStage3WithFakes()
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	ir.byMeeting[meeting.ID] = []models.DecisionItem{
		{Text: "single decision"},
	}

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(emb.calls) != 0 || len(dec.rows) != 0 {
		t.Error("expected no work below 2-decision floor")
	}
}

func TestStage3_Skip_NoEligibleChains(t *testing.T) {
	t.Parallel()
	m, cr, emb, dec, ir, _ := newStage3WithFakes()
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	ir.byMeeting[meeting.ID] = []models.DecisionItem{
		{Text: "a"}, {Text: "b"},
	}
	cr.eligible = nil // no eligible chains

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(emb.calls) != 0 {
		t.Error("expected no embedder calls when no eligible chains")
	}
	if len(dec.rows) != 0 {
		t.Error("expected no rows when no eligible chains")
	}
}

func TestStage3_Skip_BelowUsableEmbeddingFloor(t *testing.T) {
	t.Parallel()
	m, cr, emb, dec, ir, mr := newStage3WithFakes()
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	chainID := uuid.New()
	ir.byMeeting[meeting.ID] = []models.DecisionItem{
		{Text: "usable"}, {Text: "missing-vector"},
	}
	emb.embeddings["usable"] = []float64{1, 0}
	cr.eligible = []repository.EligibleChainRow{
		{ChainID: chainID, LastMeetingStart: time.Now(), DecisionBoundMeetingCount: 4},
	}

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(emb.calls) != 1 {
		t.Fatalf("expected one meeting embedder call before usable-vector guard, got %d", len(emb.calls))
	}
	if len(dec.rows) != 0 || len(mr.assignments) != 0 {
		t.Fatalf("expected no action with fewer than 2 usable embeddings, rows=%d assignments=%d",
			len(dec.rows), len(mr.assignments))
	}
}

// ==================== Phase 6 cutover paths ====================

// stage3HappyEligible returns a fully-wired matcher + meeting whose
// score will land in the auto band by default. Tests tweak the
// embeddings to push into the suggestion / orphan bands.
func stage3HappyEligible(t *testing.T, meetingVec, chainVec []float64) (
	*ChainMatcherV3Stage3,
	*fakeCentroidsRepo, *fakeEmbedder, *fakeDecisionsRepo, *fakeItemRepoForStage3, *fakeMeetingRepoForStage3,
	*models.Meeting, uuid.UUID,
) {
	t.Helper()
	m, cr, emb, dec, ir, mr := newStage3WithFakes()
	chainID := uuid.New()
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()

	ir.byMeeting[meeting.ID] = []models.DecisionItem{
		{Text: "m1"}, {Text: "m2"},
	}
	emb.embeddings["m1"] = meetingVec
	emb.embeddings["m2"] = meetingVec
	emb.embeddings["c1"] = chainVec
	emb.embeddings["c2"] = chainVec

	cr.eligible = []repository.EligibleChainRow{
		{ChainID: chainID, LastMeetingStart: time.Now(), DecisionBoundMeetingCount: 4},
	}
	cr.chainTexts[chainID] = []string{"c1", "c2"}

	return m, cr, emb, dec, ir, mr, meeting, chainID
}

func TestStage3_AutoBand_AppliesDecisionMatchAndCascades(t *testing.T) {
	t.Parallel()
	// Identical vectors → cosine = 1.0 → auto-attach
	m, _, _, dec, ir, mr, meeting, chainID := stage3HappyEligible(t,
		[]float64{1, 0, 0}, []float64{1, 0, 0})

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}

	// Meeting got the chain assignment
	in, ok := mr.assignments[meeting.ID]
	if !ok {
		t.Fatal("expected SetChainAssignment to be called for auto-attach")
	}
	if in.ChainID == nil || *in.ChainID != chainID {
		t.Errorf("expected chain_id %s, got %v", chainID, in.ChainID)
	}
	if in.Method == nil || *in.Method != models.ChainAssignmentMethodDecisionMatch {
		t.Errorf("expected method=decision_match, got %v", in.Method)
	}
	if in.Score == nil || math.Abs(*in.Score-1.0) > 1e-9 {
		t.Errorf("expected score≈1.0, got %v", in.Score)
	}
	if in.LockedAt != nil {
		t.Error("decision_match must NOT set locked_at — auto assignments stay user-overridable")
	}
	// In-memory mutation so writeAppliedRow has the chain id
	if meeting.ChainID == nil || *meeting.ChainID != chainID {
		t.Error("expected meeting.ChainID set after apply")
	}
	// decision_items cascade
	if got, ok := ir.cascadedChainID[meeting.ID]; !ok || got != chainID {
		t.Errorf("expected decision_items cascade for %s → %s, got %v", meeting.ID, chainID, ir.cascadedChainID)
	}
	// Audit row
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 applied audit row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	if row.Mode != "applied" || row.Status != "applied" {
		t.Errorf("expected mode=applied status=applied, got %s/%s", row.Mode, row.Status)
	}
	if row.CandidateChainID == nil || *row.CandidateChainID != chainID {
		t.Errorf("audit row chain mismatch: got %v want %s", row.CandidateChainID, chainID)
	}
	if row.SuppressionKey == nil || *row.SuppressionKey != "decision:chain:"+chainID.String() {
		t.Errorf("expected decision: prefix suppression_key, got %v", row.SuppressionKey)
	}
}

func TestStage3_SuggestionBand_WritesPendingRow(t *testing.T) {
	t.Parallel()
	// Cosine = 0.75 (between 0.70 and 0.80) → suggestion band.
	// Vectors {1,0} and {3,1} (normalized) — cosine = 3/sqrt(10) ≈ 0.9486.
	// We need lower. Try {1,0} and {1,1}: cosine = 1/sqrt(2) ≈ 0.707 — borderline.
	// Use {1,0} and {3,sqrt(7)} roughly — easier: just craft the band directly.
	// Simpler: identical → 1.0 (auto). To get suggestion, use angle ≈ 39°:
	// {1,0} vs {cos(39°), sin(39°)} ≈ {0.777, 0.629} → cosine = 0.777.
	m, _, _, dec, _, mr, meeting, chainID := stage3HappyEligible(t,
		[]float64{1, 0}, []float64{0.777, 0.629})

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(mr.assignments) != 0 {
		t.Error("suggestion path must NOT call SetChainAssignment")
	}
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 pending row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	if row.Mode != "suggestion" || row.Status != "pending" {
		t.Errorf("expected mode=suggestion status=pending, got %s/%s", row.Mode, row.Status)
	}
	if row.CandidateChainID == nil || *row.CandidateChainID != chainID {
		t.Errorf("expected candidate_chain_id %s, got %v", chainID, row.CandidateChainID)
	}
	if row.DecisionCosine == nil || *row.DecisionCosine < 0.70 || *row.DecisionCosine >= 0.80 {
		t.Errorf("expected cosine in [0.70, 0.80), got %v", row.DecisionCosine)
	}
}

func TestStage3_SuggestionBand_SuppressedDowngradesToShadow(t *testing.T) {
	t.Parallel()
	m, _, _, dec, _, mr, meeting, chainID := stage3HappyEligible(t,
		[]float64{1, 0}, []float64{0.777, 0.629})
	dec.declinedByKey = map[string]int{
		"decision:chain:" + chainID.String(): suppressionDeclineLimit,
	}

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(mr.assignments) != 0 {
		t.Error("suppressed path must NOT call SetChainAssignment")
	}
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 shadow row, got %d", len(dec.rows))
	}
	if dec.rows[0].Status != "suppressed" {
		t.Errorf("expected status=suppressed, got %s", dec.rows[0].Status)
	}
	if len(dec.lastNSince) != 1 || dec.lastNSince[0].IsZero() {
		t.Fatalf("expected suppression window to be passed to Stage 3 streak check, got %v", dec.lastNSince)
	}
	age := time.Since(dec.lastNSince[0])
	if age < suppressionWindow-time.Minute || age > suppressionWindow+time.Minute {
		t.Fatalf("expected 30d suppression window, got age %s", age)
	}
}

func TestStage3_SuggestionBand_RateLimitedDowngradesToShadow(t *testing.T) {
	t.Parallel()
	m, _, _, dec, _, mr, meeting, _ := stage3HappyEligible(t,
		[]float64{1, 0}, []float64{0.777, 0.629})
	dec.pendingForUser = suggestionDailyLimit

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(mr.assignments) != 0 {
		t.Error("rate-limited path must NOT call SetChainAssignment")
	}
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 shadow row, got %d", len(dec.rows))
	}
	if dec.rows[0].Status != "shadow_only" {
		t.Errorf("expected status=shadow_only (rate-limited), got %s", dec.rows[0].Status)
	}
}

func TestStage3_BelowSuggestionFloor_NoActionNoLog(t *testing.T) {
	t.Parallel()
	// Orthogonal → cosine 0 → no winner.
	m, _, _, dec, _, mr, meeting, _ := stage3HappyEligible(t,
		[]float64{1, 0, 0}, []float64{0, 1, 0})

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if len(mr.assignments) != 0 || len(dec.rows) != 0 {
		t.Error("below-band must produce no assignment and no audit row")
	}
}

func TestStage3_PicksHighestCosineWinner(t *testing.T) {
	t.Parallel()
	// Two eligible chains: one in suggestion band (~0.75), one in
	// auto band (1.0). Auto winner must be selected.
	m, cr, emb, dec, ir, mr := newStage3WithFakes()
	weakChain := uuid.New()
	strongChain := uuid.New()
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()

	ir.byMeeting[meeting.ID] = []models.DecisionItem{{Text: "m1"}, {Text: "m2"}}
	emb.embeddings["m1"] = []float64{1, 0}
	emb.embeddings["m2"] = []float64{1, 0}
	emb.embeddings["weak"] = []float64{0.777, 0.629} // cosine 0.777
	emb.embeddings["strong"] = []float64{1, 0}       // cosine 1.0

	cr.eligible = []repository.EligibleChainRow{
		{ChainID: weakChain, LastMeetingStart: time.Now(), DecisionBoundMeetingCount: 4},
		{ChainID: strongChain, LastMeetingStart: time.Now(), DecisionBoundMeetingCount: 4},
	}
	cr.chainTexts[weakChain] = []string{"weak"}
	cr.chainTexts[strongChain] = []string{"strong"}

	if err := m.EvaluateAndApply(context.Background(), meeting); err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	in, ok := mr.assignments[meeting.ID]
	if !ok {
		t.Fatal("expected SetChainAssignment from auto winner")
	}
	if in.ChainID == nil || *in.ChainID != strongChain {
		t.Errorf("expected winner=%s (auto-band), got %v", strongChain, in.ChainID)
	}
	// Single audit row for the winner only.
	if len(dec.rows) != 1 {
		t.Errorf("expected single audit row for the winner, got %d", len(dec.rows))
	}
}

// ==================== centroid lazy invalidation ====================

func TestStage3_Centroid_FreshCacheReused(t *testing.T) {
	t.Parallel()
	m, cr, emb, _, _, _ := newStage3WithFakes()
	chainID := uuid.New()
	cached := []float64{1, 0, 0}
	cr.cache[chainID] = &repository.ChainCentroid{
		ChainID:             chainID,
		Centroid:            cached,
		SourceDecisionCount: 5,
		ComputedAt:          v3TestNow.Add(1 * time.Hour),
	}
	cr.mostRecent[chainID] = v3TestNow // older than cache → cache fresh

	got, err := m.getOrRebuildChainCentroid(context.Background(), uuid.Nil, chainID)
	if err != nil {
		t.Fatalf("getOrRebuild failed: %v", err)
	}
	_ = got
	if len(emb.calls) != 0 {
		t.Errorf("expected no embed call when cache is fresh, got %d", len(emb.calls))
	}
	if len(cr.upsertCalls) != 0 {
		t.Errorf("expected no upsert when cache is fresh, got %d", len(cr.upsertCalls))
	}
}

func TestStage3_Centroid_StaleCacheRebuilds(t *testing.T) {
	t.Parallel()
	m, cr, emb, _, _, _ := newStage3WithFakes()
	chainID := uuid.New()
	cr.cache[chainID] = &repository.ChainCentroid{
		ChainID:             chainID,
		Centroid:            []float64{0, 1, 0},
		SourceDecisionCount: 5,
		ComputedAt:          v3TestNow,
	}
	cr.mostRecent[chainID] = v3TestNow.Add(1 * time.Hour) // newer than cache → stale
	cr.chainTexts[chainID] = []string{"new decision"}
	emb.embeddings["new decision"] = []float64{1, 0, 0}

	got, err := m.getOrRebuildChainCentroid(context.Background(), uuid.Nil, chainID)
	if err != nil {
		t.Fatalf("getOrRebuild failed: %v", err)
	}
	if got[0] != 1 || got[1] != 0 {
		t.Errorf("expected rebuilt centroid from new decision, got %v", got)
	}
	if len(emb.calls) != 1 {
		t.Errorf("expected 1 embed call to rebuild, got %d", len(emb.calls))
	}
	if len(cr.upsertCalls) != 1 {
		t.Errorf("expected 1 upsert after rebuild, got %d", len(cr.upsertCalls))
	}
}

func TestStage3_Centroid_MissingCacheRebuilds(t *testing.T) {
	t.Parallel()
	m, cr, emb, _, _, _ := newStage3WithFakes()
	chainID := uuid.New()
	cr.chainTexts[chainID] = []string{"a", "b"}
	emb.embeddings["a"] = []float64{2, 0}
	emb.embeddings["b"] = []float64{0, 2}

	got, err := m.getOrRebuildChainCentroid(context.Background(), uuid.Nil, chainID)
	if err != nil {
		t.Fatalf("getOrRebuild failed: %v", err)
	}
	if got[0] != 1 || got[1] != 1 {
		t.Errorf("expected mean (1,1), got %v", got)
	}
	if len(cr.upsertCalls) != 1 {
		t.Errorf("expected 1 upsert after first build, got %d", len(cr.upsertCalls))
	}
}

func TestStage3_Centroid_PropagatesEmbedderError(t *testing.T) {
	t.Parallel()
	m, cr, emb, _, _, _ := newStage3WithFakes()
	chainID := uuid.New()
	cr.chainTexts[chainID] = []string{"a"}
	emb.err = errors.New("nlp down")

	if _, err := m.getOrRebuildChainCentroid(context.Background(), uuid.Nil, chainID); err == nil {
		t.Error("expected error to surface when embedder fails")
	}
}

// Issue #39 — when a centroid rebuild fires, the userID forwarded to
// the embedder MUST match the evaluating meeting's owner, never
// uuid.Nil. The httpEmbeddingClient skips usage recording for
// uuid.Nil, so a regression that drops the id would silently lose
// chain v3 cost attribution.
func TestStage3_Centroid_RebuildAttributesUser(t *testing.T) {
	t.Parallel()
	m, cr, emb, _, _, _ := newStage3WithFakes()
	chainID := uuid.New()
	ownerID := uuid.New()
	cr.chainTexts[chainID] = []string{"a", "b"}
	emb.embeddings["a"] = []float64{1, 0}
	emb.embeddings["b"] = []float64{0, 1}

	_, err := m.getOrRebuildChainCentroid(context.Background(), ownerID, chainID)
	if err != nil {
		t.Fatalf("getOrRebuild failed: %v", err)
	}
	if len(emb.callUsers) != 1 {
		t.Fatalf("expected 1 embed call, got %d", len(emb.callUsers))
	}
	if emb.callUsers[0] != ownerID {
		t.Errorf("attribution lost: got userID=%v want %v", emb.callUsers[0], ownerID)
	}
	if emb.callMeetings[0] != nil {
		t.Errorf("chain-level rebuild must pass nil meeting_id, got %v", *emb.callMeetings[0])
	}
}

// ==================== EvaluateAndApply surfaces meeting-side errors ====================

func TestStage3_RejectsNilMeeting(t *testing.T) {
	t.Parallel()
	m, _, _, _, _, _ := newStage3WithFakes()
	if err := m.EvaluateAndApply(context.Background(), nil); err == nil {
		t.Error("expected error for nil meeting")
	}
}

// ==================== band classifiers ====================

func TestStage3_AutoBandBoundary(t *testing.T) {
	t.Parallel()
	if !IsStage3AutoBand(0.80) {
		t.Error("0.80 should be auto band (inclusive)")
	}
	if IsStage3AutoBand(0.79) {
		t.Error("0.79 should not be auto band")
	}
}

func TestStage3_SuggestionBandBoundary(t *testing.T) {
	t.Parallel()
	if !IsStage3SuggestionBand(0.70) {
		t.Error("0.70 should be suggestion band (inclusive lower)")
	}
	if IsStage3SuggestionBand(0.80) {
		t.Error("0.80 belongs to auto, not suggestion")
	}
	if IsStage3SuggestionBand(0.69) {
		t.Error("0.69 below suggestion floor")
	}
}
