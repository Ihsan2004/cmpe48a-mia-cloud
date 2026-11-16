package services

import (
	"context"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== test fakes ====================

type fakeDecisionsRepo struct {
	// Embed so future ChainDecisionsV2Repository additions (Stage 3 etc.)
	// don't force per-test stubs in the cutover suite.
	repository.ChainDecisionsV2Repository
	rows           []repository.ChainDecisionV2Row
	declinedByKey  map[string]int
	pendingForUser int
	lastNSince     []time.Time
}

func (f *fakeDecisionsRepo) LogShadowDecision(_ context.Context, row repository.ChainDecisionV2Row) error {
	f.rows = append(f.rows, row)
	return nil
}
func (f *fakeDecisionsRepo) CountDeclinedByKeySince(_ context.Context, _ uuid.UUID, key string, _ time.Time) (int, error) {
	if f.declinedByKey == nil {
		return 0, nil
	}
	return f.declinedByKey[key], nil
}
func (f *fakeDecisionsRepo) CountPendingForUserSince(_ context.Context, _ uuid.UUID, _ time.Time) (int, error) {
	return f.pendingForUser, nil
}
func (f *fakeDecisionsRepo) LastNDecisionsAllDeclined(_ context.Context, _ uuid.UUID, key string, n int, since time.Time) (bool, error) {
	f.lastNSince = append(f.lastNSince, since)
	if f.declinedByKey == nil {
		return false, nil
	}
	return f.declinedByKey[key] >= n, nil
}

// fakeMeetingRepoForV3 captures the meeting-side writes the v3 matcher
// needs to drive: orphan repoint (RepointMeetingChain) and the legacy
// UpdateMeetingFields path. updates[meetingID]["chain_id"] is set on
// repoint so existing assertions keep working with the new method.
type fakeMeetingRepoForV3 struct {
	repository.MeetingRepository
	updates map[uuid.UUID]map[string]interface{}
}

func (f *fakeMeetingRepoForV3) UpdateMeetingFields(_ context.Context, id uuid.UUID, fields map[string]interface{}) error {
	if f.updates == nil {
		f.updates = map[uuid.UUID]map[string]interface{}{}
	}
	f.updates[id] = fields
	return nil
}
func (f *fakeMeetingRepoForV3) RepointMeetingChain(_ context.Context, meetingID, chainID uuid.UUID) (bool, error) {
	if f.updates == nil {
		f.updates = map[uuid.UUID]map[string]interface{}{}
	}
	// Store the chain_id by VALUE (not as a *uuid.UUID) so callers
	// can compare against `*meeting.ChainID` directly without worrying
	// about pointer identity. Tests assert equality against the UUID
	// value, not the pointer address.
	f.updates[meetingID] = map[string]interface{}{"chain_id": chainID}
	return true, nil
}

// fakeDecisionItemRepoForV3 satisfies the interface and provides a
// no-op cascade so PersistAfterIngest can call it without exercising
// the embedded nil interface (Phase 4 review fix added the cascade
// for orphan promotion).
type fakeDecisionItemRepoForV3 struct {
	repository.DecisionRepository
}

func (f *fakeDecisionItemRepoForV3) UpdateChainIDForMeeting(_ context.Context, _ uuid.UUID, _ uuid.UUID) error {
	return nil
}
func (f *fakeDecisionItemRepoForV3) UpdateChainIDForMeetingNull(_ context.Context, _ uuid.UUID) error {
	return nil
}

func newV3WithFakes(candidates []repository.CandidateMeeting) (*ChainMatcherV3, *fakeChainRepo, *fakeDecisionsRepo) {
	chainRepo := &fakeChainRepo{candidates: candidates}
	dec := &fakeDecisionsRepo{}
	m := &ChainMatcherV3{
		chainRepo:     chainRepo,
		meetingRepo:   &fakeMeetingRepoForV3{},
		decisionsRepo: dec,
		itemRepo:      &fakeDecisionItemRepoForV3{},
	}
	return m, chainRepo, dec
}

// newV3WithMeetingRepo gives tests access to the meeting-side updates
// so orphan-promotion flows can verify the candidate got repointed.
func newV3WithMeetingRepo(candidates []repository.CandidateMeeting) (*ChainMatcherV3, *fakeChainRepo, *fakeDecisionsRepo, *fakeMeetingRepoForV3) {
	chainRepo := &fakeChainRepo{candidates: candidates}
	dec := &fakeDecisionsRepo{}
	mr := &fakeMeetingRepoForV3{}
	m := &ChainMatcherV3{
		chainRepo:     chainRepo,
		meetingRepo:   mr,
		decisionsRepo: dec,
		itemRepo:      &fakeDecisionItemRepoForV3{},
	}
	return m, chainRepo, dec, mr
}

func makeV3Meeting(title string, attendees []string, start time.Time, dur time.Duration) *models.Meeting {
	atts := make([]models.Attendee, len(attendees))
	for i, e := range attendees {
		atts[i] = models.Attendee{Email: e}
	}
	return &models.Meeting{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Title:     title,
		Attendees: atts,
		StartTime: start,
		EndTime:   start.Add(dur),
	}
}

func makeV3Candidate(title string, attendees []string, start time.Time, dur time.Duration, chainID *uuid.UUID) repository.CandidateMeeting {
	atts := make([]models.Attendee, len(attendees))
	for i, e := range attendees {
		atts[i] = models.Attendee{Email: e}
	}
	return repository.CandidateMeeting{
		ID:        uuid.New(),
		ChainID:   chainID,
		Title:     title,
		StartTime: start,
		EndTime:   start.Add(dur),
		Attendees: atts,
	}
}

// Anchor everything around a fixed clock so tests don't drift with
// wall-clock time. 2026-05-04 (Monday) gives us a known weekday for
// the time/weekday features.
var v3TestNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// ==================== Stage 1 hard gate ====================

func TestV3_Stage1_FloorPassesAndOneStrong(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)

	ps := scorePair(meeting, &cand)
	if !ps.Stage1Pass {
		t.Fatalf("expected Stage 1 pass for full overlap + identical title, got fail (P=%f, T=%f)",
			ps.ParticipantOverlap, ps.TitleSimilarity)
	}
}

func TestV3_Stage1_BelowParticipantFloor_Rejects(t *testing.T) {
	t.Parallel()
	cand := makeV3Candidate("Product Roadmap",
		[]string{"x@c.com", "y@c.com", "z@c.com", "w@c.com"},
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap",
		[]string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"},
		v3TestNow, 30*time.Minute)

	ps := scorePair(meeting, &cand)
	if ps.Stage1Pass {
		t.Errorf("expected Stage 1 fail (P=0), got pass with score %f", ps.WeightedScore)
	}
}

func TestV3_Stage1_GenericOnlyTitleOverlap_FailsStrongGate(t *testing.T) {
	t.Parallel()
	// 75% participant overlap → passes participant floor and strong-OR;
	// titles overlap only on generic "sync" → T = 0.2 (passes floor),
	// but DOES NOT satisfy T >= 0.60 strong side.
	// Strong side relies on participant. P >= 0.50 so strong-OR passes.
	// → Stage 1 passes, but title contributes only the floor — verify
	// the logged reason in shadow shows TitleSpecificCommon = 0.
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	candAtts := []string{"a@c.com", "b@c.com", "c@c.com", "x@c.com"}

	cand := makeV3Candidate("Engineering Sync", candAtts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Backend Sync", atts, v3TestNow, 30*time.Minute)

	ps := scorePair(meeting, &cand)
	if !ps.Stage1Pass {
		t.Fatalf("expected Stage 1 pass via strong participant, got fail (P=%f, T=%f)",
			ps.ParticipantOverlap, ps.TitleSimilarity)
	}
	if ps.TitleSpecificCommon != 0 {
		t.Errorf("expected 0 specific common (only generic 'sync' overlap), got %d",
			ps.TitleSpecificCommon)
	}
}

func TestV3_Stage1_TwoWeakSignals_Reject(t *testing.T) {
	t.Parallel()
	// P = 0.4 (above floor, below strong), T = 0.5 (above floor, below
	// strong) — compound gate must reject because neither side is
	// strong. This is the case where the legacy matcher would let it
	// pass (single threshold) but v3 won't.
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com", "e@c.com"}
	candAtts := []string{"a@c.com", "b@c.com", "x@c.com", "y@c.com", "z@c.com"}

	cand := makeV3Candidate("Roadmap Planning", candAtts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Roadmap Review", atts, v3TestNow, 30*time.Minute)

	ps := scorePair(meeting, &cand)
	if ps.Stage1Pass {
		t.Errorf("expected Stage 1 fail (two weak signals), got pass score=%f", ps.WeightedScore)
	}
}

func TestV3_Stage1_RejectsLessThan3Attendees(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)

	ps := scorePair(meeting, &cand)
	if ps.Stage1Pass {
		t.Errorf("expected Stage 1 fail (only 2 attendees), got pass")
	}
}

// ==================== Stage 2 weighted score ====================

func TestV3_Stage2_PerfectMatchScoreIs1(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	// Same weekday (Monday, 7 days ago), same time of day, recent (≤15d),
	// same duration bucket, identical title, identical attendees.
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)

	ps := scorePair(meeting, &cand)
	if !ps.Stage1Pass {
		t.Fatalf("expected pass, got Stage 1 fail")
	}
	if ps.WeightedScore < 0.99 || ps.WeightedScore > 1.0001 {
		t.Errorf("expected weighted score ≈ 1.0, got %f", ps.WeightedScore)
	}
}

func TestV3_Stage2_FormulaSumsWeights(t *testing.T) {
	t.Parallel()
	// Verify the weighted sum is exactly:
	//   0.42*P + 0.28*T + 0.12*W + 0.08*Tm + 0.05*R + 0.05*D
	//
	// Construct: P=1.0, T=1.0, weekday match, different time of day
	// (within 1h: 0.5), recency 1.0 (recent), duration mismatch (0.0).
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour).Add(45*time.Minute), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 60*time.Minute)

	ps := scorePair(meeting, &cand)
	want := 0.42*1 + 0.28*1 + 0.12*1 + 0.08*0.5 + 0.05*1 + 0.05*0
	if abs(ps.WeightedScore-want) > 1e-9 {
		t.Errorf("expected score %f, got %f", want, ps.WeightedScore)
	}
}

// ==================== aggregateByChain + tie-break ====================

func TestV3_AggregateByChain_KeepsBestPerChain(t *testing.T) {
	t.Parallel()
	chainA := uuid.New()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}

	weak := PairScore{
		Candidate: repository.CandidateMeeting{
			ID: uuid.New(), ChainID: &chainA, StartTime: v3TestNow.Add(-30 * 24 * time.Hour),
			Attendees: attendeeSlice(atts),
		},
		Stage1Pass: true, WeightedScore: 0.55, TitleSimilarity: 0.6,
	}
	strong := PairScore{
		Candidate: repository.CandidateMeeting{
			ID: uuid.New(), ChainID: &chainA, StartTime: v3TestNow.Add(-7 * 24 * time.Hour),
			Attendees: attendeeSlice(atts),
		},
		Stage1Pass: true, WeightedScore: 0.85, TitleSimilarity: 0.9,
	}

	out := aggregateByChain([]PairScore{weak, strong})
	if len(out) != 1 {
		t.Fatalf("expected 1 chain candidate, got %d", len(out))
	}
	if out[0].BestPair.WeightedScore != 0.85 {
		t.Errorf("expected best pair (0.85) to win, got %f", out[0].BestPair.WeightedScore)
	}
}

func TestV3_TieBreak_HigherTitleSimilarityWins(t *testing.T) {
	t.Parallel()
	a := PairScore{
		Candidate:       repository.CandidateMeeting{ID: uuid.New(), StartTime: v3TestNow.Add(-1 * 24 * time.Hour)},
		Stage1Pass:      true,
		WeightedScore:   0.7,
		TitleSimilarity: 0.5,
	}
	b := PairScore{
		Candidate:       repository.CandidateMeeting{ID: uuid.New(), StartTime: v3TestNow.Add(-10 * 24 * time.Hour)},
		Stage1Pass:      true,
		WeightedScore:   0.7, // same score
		TitleSimilarity: 0.9, // higher title sim → wins
	}
	if !pairBetter(b, a) {
		t.Errorf("expected b (higher title sim) to outrank a")
	}
}

func TestV3_TieBreak_RecencyBeforeUUID(t *testing.T) {
	t.Parallel()
	older := PairScore{
		Candidate:       repository.CandidateMeeting{ID: uuid.New(), StartTime: v3TestNow.Add(-30 * 24 * time.Hour)},
		Stage1Pass:      true,
		WeightedScore:   0.7,
		TitleSimilarity: 0.7,
	}
	newer := PairScore{
		Candidate:       repository.CandidateMeeting{ID: uuid.New(), StartTime: v3TestNow.Add(-1 * 24 * time.Hour)},
		Stage1Pass:      true,
		WeightedScore:   0.7,
		TitleSimilarity: 0.7,
	}
	if !pairBetter(newer, older) {
		t.Errorf("expected newer to outrank older when score+title equal")
	}
}

func TestV3_TieBreak_DeterministicWhenAllElseEqual(t *testing.T) {
	t.Parallel()
	// Identical scores, identical title sim, identical start times:
	// fall back to UUID lexical order. Determinism matters because the
	// same input data must always pick the same winner across re-runs.
	id1 := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	id2 := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	start := v3TestNow.Add(-7 * 24 * time.Hour)

	a := PairScore{
		Candidate:       repository.CandidateMeeting{ID: id1, StartTime: start},
		WeightedScore:   0.7,
		TitleSimilarity: 0.7,
	}
	b := PairScore{
		Candidate:       repository.CandidateMeeting{ID: id2, StartTime: start},
		WeightedScore:   0.7,
		TitleSimilarity: 0.7,
	}
	if !pairBetter(a, b) {
		t.Error("expected lower UUID to outrank higher UUID under all-equal tie-break")
	}
	if pairBetter(b, a) {
		t.Error("tie-break must be asymmetric under equal inputs")
	}
}

func TestV3_AggregateByChain_OrphansAndChainsCoexist(t *testing.T) {
	t.Parallel()
	chainA := uuid.New()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}

	chainPair := PairScore{
		Candidate: repository.CandidateMeeting{
			ID: uuid.New(), ChainID: &chainA, StartTime: v3TestNow.Add(-7 * 24 * time.Hour),
			Attendees: attendeeSlice(atts),
		},
		Stage1Pass: true, WeightedScore: 0.85, TitleSimilarity: 0.9,
	}
	orphanPair := PairScore{
		Candidate: repository.CandidateMeeting{
			ID: uuid.New(), ChainID: nil, StartTime: v3TestNow.Add(-3 * 24 * time.Hour),
			Attendees: attendeeSlice(atts),
		},
		Stage1Pass: true, WeightedScore: 0.92, TitleSimilarity: 0.95,
	}

	out := aggregateByChain([]PairScore{chainPair, orphanPair})
	if len(out) != 2 {
		t.Fatalf("expected 2 candidates (chain + orphan), got %d", len(out))
	}
	// Higher-scoring orphan should sort first.
	if !out[0].IsOrphan {
		t.Errorf("expected orphan (0.92) to outrank chain (0.85), got chain first")
	}
}

// ==================== Decide / MutateMeetingForDecision / PersistAfterIngest ====================

// Phase 4 cutover semantics — v3 is the production decider. These
// tests exercise the public Decide/Apply contract (the shadow-mode
// EvaluateShadow / LogShadow API was removed at cutover).

func TestV3_Decide_NoCandidatesReturnsOrphan(t *testing.T) {
	t.Parallel()
	m, _, _ := newV3WithFakes(nil)
	meeting := makeV3Meeting("Product Roadmap",
		[]string{"a@c.com", "b@c.com", "c@c.com"}, v3TestNow, 30*time.Minute)

	d, err := m.Decide(context.Background(), meeting)
	if err != nil {
		t.Fatalf("decide failed: %v", err)
	}
	if d.Action != ActionOrphan {
		t.Errorf("expected ActionOrphan, got %s", d.Action)
	}
}

func TestV3_Decide_RejectsNilMeeting(t *testing.T) {
	t.Parallel()
	m, _, _ := newV3WithFakes(nil)
	if _, err := m.Decide(context.Background(), nil); err == nil {
		t.Error("expected error for nil meeting")
	}
}

func TestV3_Decide_ManualUserBypass(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)
	method := models.ChainAssignmentMethodManualUser
	meeting.ChainAssignmentMethod = &method

	m, _, _ := newV3WithFakes([]repository.CandidateMeeting{cand})
	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSkip {
		t.Errorf("manual_user lock must bypass matcher; got %s", d.Action)
	}
}

func TestV3_Decide_ManualRemovedBypass(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)
	method := models.ChainAssignmentMethodManualRemoved
	meeting.ChainAssignmentMethod = &method

	m, _, _ := newV3WithFakes([]repository.CandidateMeeting{cand})
	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSkip {
		t.Errorf("manual_removed lock must bypass matcher; got %s", d.Action)
	}
}

func TestV3_Decide_AlreadyAssignedSkipped(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	existingChain := uuid.New()
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)
	meeting.ChainID = &existingChain

	m, _, _ := newV3WithFakes([]repository.CandidateMeeting{cand})
	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSkip {
		t.Errorf("a meeting that already has chain_id should be skipped, got %s", d.Action)
	}
}

func TestV3_Decide_SeriesIDOverridesExistingNonManualChain(t *testing.T) {
	t.Parallel()
	oldChainID := uuid.New()
	seriesChainID := uuid.New()
	seriesID := "series-wins-over-auto"
	seriesChain := &models.MeetingChain{ID: seriesChainID, ChainType: "calendar_series", CalendarSeriesID: &seriesID}

	m, chainRepo, _ := newV3WithFakes(nil)
	chainRepo.chainsBySeries = map[string]*models.MeetingChain{seriesID: seriesChain}

	meeting := makeV3Meeting("Standup", []string{"a@c.com", "b@c.com", "c@c.com"}, v3TestNow, 30*time.Minute)
	method := models.ChainAssignmentMethodAutoScore
	meeting.ChainID = &oldChainID
	meeting.ChainAssignmentMethod = &method
	meeting.CalendarSeriesID = &seriesID

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSeriesID {
		t.Fatalf("expected series_id to outrank non-manual chain_id, got %s", d.Action)
	}
	if d.Chain == nil || d.Chain.ID != seriesChainID {
		t.Errorf("expected series chain %s, got %+v", seriesChainID, d.Chain)
	}
}

func TestV3_Decide_SeriesIDExistingChain(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	seriesID := "google-event-series-123"
	chain := &models.MeetingChain{ID: chainID, ChainType: "calendar_series", CalendarSeriesID: &seriesID}

	m, chainRepo, _ := newV3WithFakes(nil)
	chainRepo.chainsByID = map[uuid.UUID]*models.MeetingChain{chainID: chain}
	chainRepo.chainsBySeries = map[string]*models.MeetingChain{seriesID: chain}

	meeting := makeV3Meeting("Standup", []string{"a@c.com", "b@c.com", "c@c.com"}, v3TestNow, 30*time.Minute)
	meeting.CalendarSeriesID = &seriesID

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSeriesID {
		t.Fatalf("expected ActionSeriesID, got %s", d.Action)
	}
	if d.Chain == nil || d.Chain.ID != chainID {
		t.Errorf("expected target chain %s, got %v", chainID, d.Chain)
	}
}

func TestV3_Decide_SeriesIDNewChainSentinel(t *testing.T) {
	t.Parallel()
	seriesID := "first-occurrence-series"
	m, _, _ := newV3WithFakes(nil)
	meeting := makeV3Meeting("Standup", []string{"a@c.com", "b@c.com", "c@c.com"}, v3TestNow, 30*time.Minute)
	meeting.CalendarSeriesID = &seriesID

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSeriesID {
		t.Fatalf("expected ActionSeriesID, got %s", d.Action)
	}
	// First-occurrence sentinel: Chain is non-nil with zero ID so
	// MutateMeetingForDecision knows to materialise the row.
	if d.Chain == nil || d.Chain.ID != uuid.Nil {
		t.Errorf("expected sentinel chain (nil ID), got %+v", d.Chain)
	}
}

func TestV3_Decide_AutoScoreChainCandidate(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	chainID := uuid.New()
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, &chainID)
	chain := &models.MeetingChain{ID: chainID}
	m, chainRepo, _ := newV3WithFakes([]repository.CandidateMeeting{cand})
	chainRepo.chainsByID = map[uuid.UUID]*models.MeetingChain{chainID: chain}
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionAutoScoreChain {
		t.Fatalf("expected ActionAutoScoreChain, got %s", d.Action)
	}
	if d.Chain == nil || d.Chain.ID != chainID {
		t.Errorf("expected chain %s, got %v", chainID, d.Chain)
	}
	if d.Winner == nil || d.Winner.BestPair.WeightedScore < bandAutoMin {
		t.Errorf("winner missing or below auto band: %+v", d.Winner)
	}
}

func TestV3_Decide_EqualStrengthChainCandidatesSuggestInsteadOfAuto(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	chainA := uuid.New()
	chainB := uuid.New()
	candA := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, &chainA)
	candB := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, &chainB)
	candA.ID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	candB.ID = uuid.MustParse("00000000-0000-0000-0000-000000000002")

	m, chainRepo, _ := newV3WithFakes([]repository.CandidateMeeting{candA, candB})
	chainRepo.chainsByID = map[uuid.UUID]*models.MeetingChain{
		chainA: &models.MeetingChain{ID: chainA},
		chainB: &models.MeetingChain{ID: chainB},
	}
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSuggest {
		t.Fatalf("equal-strength chain conflict should be a suggestion, got %s", d.Action)
	}
	if d.Winner == nil || d.Winner.ChainID != chainA {
		t.Errorf("expected deterministic top candidate chainA, got %+v", d.Winner)
	}
}

func TestV3_Decide_AutoScoreOrphanCandidate(t *testing.T) {
	t.Parallel()
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	cand := makeV3Candidate("Product Roadmap", atts,
		v3TestNow.Add(-7*24*time.Hour), 30*time.Minute, nil)
	m, _, _ := newV3WithFakes([]repository.CandidateMeeting{cand})
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 30*time.Minute)

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionAutoScoreOrphan {
		t.Fatalf("expected ActionAutoScoreOrphan, got %s", d.Action)
	}
	if d.Winner == nil || !d.Winner.IsOrphan {
		t.Errorf("expected orphan winner, got %+v", d.Winner)
	}
}

// suggestionBandRecipe constructs a (meeting, candidate) pair whose
// Stage 2 score lands solidly inside [0.40, 0.50). The recipe steers
// well clear of both band edges to dodge IEEE-754 rounding noise:
//
//	P = 0.5         → 0.42 * 0.5 = 0.21
//	T = 0.5         → 0.28 * 0.5 = 0.14
//	weekday miss    → 0
//	time within 1h  → 0.08 * 0.5 = 0.04
//	recency ≤15d    → 0.05 * 1.0 = 0.05
//	duration miss   → 0
//	Σ ≈ 0.44 — comfortably mid-band.
func suggestionBandRecipe(chainID *uuid.UUID) (*models.Meeting, repository.CandidateMeeting) {
	atts := []string{"a@c.com", "b@c.com", "c@c.com", "d@c.com"}
	candAtts := []string{"a@c.com", "b@c.com", "x@c.com", "y@c.com"} // 2/4 overlap → P=0.5
	cand := makeV3Candidate("Product Review", candAtts,
		v3TestNow.Add(-1*24*time.Hour).Add(30*time.Minute), 90*time.Minute, chainID)
	meeting := makeV3Meeting("Product Roadmap", atts, v3TestNow, 15*time.Minute)
	return meeting, cand
}

func TestV3_Decide_SuggestionBandWithChain(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	meeting, cand := suggestionBandRecipe(&chainID)
	chain := &models.MeetingChain{ID: chainID}
	m, chainRepo, _ := newV3WithFakes([]repository.CandidateMeeting{cand})
	chainRepo.chainsByID = map[uuid.UUID]*models.MeetingChain{chainID: chain}

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSuggest {
		t.Fatalf("expected ActionSuggest, got %s", d.Action)
	}
	if d.Chain == nil || d.Chain.ID != chainID {
		t.Errorf("expected target chain %s, got %v", chainID, d.Chain)
	}
}

func TestV3_Decide_SuppressedKeyDowngradesToShadow(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	meeting, cand := suggestionBandRecipe(&chainID)
	m, chainRepo, dec := newV3WithFakes([]repository.CandidateMeeting{cand})
	chainRepo.chainsByID = map[uuid.UUID]*models.MeetingChain{chainID: &models.MeetingChain{ID: chainID}}
	dec.declinedByKey = map[string]int{
		"metadata:chain:" + chainID.String(): suppressionDeclineLimit,
	}

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionSuppressed {
		t.Fatalf("expected ActionSuppressed, got %s", d.Action)
	}
	if len(dec.lastNSince) != 1 || dec.lastNSince[0].IsZero() {
		t.Fatalf("expected suppression window to be passed to streak check, got %v", dec.lastNSince)
	}
	age := time.Since(dec.lastNSince[0])
	if age < suppressionWindow-time.Minute || age > suppressionWindow+time.Minute {
		t.Fatalf("expected 30d suppression window, got age %s", age)
	}
}

func TestV3_Decide_RateLimitedDowngradesToShadow(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	meeting, cand := suggestionBandRecipe(&chainID)
	m, chainRepo, dec := newV3WithFakes([]repository.CandidateMeeting{cand})
	chainRepo.chainsByID = map[uuid.UUID]*models.MeetingChain{chainID: &models.MeetingChain{ID: chainID}}
	dec.pendingForUser = suggestionDailyLimit

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionRateLimited {
		t.Fatalf("expected ActionRateLimited, got %s", d.Action)
	}
}

func TestV3_Decide_OrphanWinnerInSuggestionBandStaysOrphan(t *testing.T) {
	t.Parallel()
	// Suggestion-band score against an orphan candidate must NOT
	// generate a suggestion (Phase 3 contract: AcceptSuggestion only
	// supports chain candidates).
	meeting, cand := suggestionBandRecipe(nil)
	m, _, _ := newV3WithFakes([]repository.CandidateMeeting{cand})

	d, _ := m.Decide(context.Background(), meeting)
	if d.Action != ActionOrphan {
		t.Errorf("expected orphan winner in suggestion band → ActionOrphan, got %s", d.Action)
	}
}

// ==================== MutateMeetingForDecision ====================

func TestV3_Mutate_AutoScoreChainStampsMeeting(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	score := 0.82
	d := Decision{
		Action: ActionAutoScoreChain,
		Chain:  &models.MeetingChain{ID: chainID},
		Winner: &ChainCandidate{
			ChainID:  chainID,
			BestPair: PairScore{WeightedScore: score},
		},
		FeatureScoresJSON: []byte(`{"k":"v"}`),
	}
	m, _, _ := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)

	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate failed: %v", err)
	}
	if meeting.ChainID == nil || *meeting.ChainID != chainID {
		t.Errorf("expected chain_id %s, got %v", chainID, meeting.ChainID)
	}
	if meeting.ChainAssignmentMethod == nil || *meeting.ChainAssignmentMethod != models.ChainAssignmentMethodAutoScore {
		t.Errorf("expected method=auto_score, got %v", meeting.ChainAssignmentMethod)
	}
	if meeting.ChainAssignmentScore == nil || *meeting.ChainAssignmentScore != score {
		t.Errorf("expected score=%f, got %v", score, meeting.ChainAssignmentScore)
	}
	if meeting.ChainAssignmentLockedAt != nil {
		t.Errorf("auto_score must NOT lock; got locked_at=%v", meeting.ChainAssignmentLockedAt)
	}
}

func TestV3_Mutate_AutoScoreOrphanCreatesChain(t *testing.T) {
	t.Parallel()
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionAutoScoreOrphan,
		Winner: &ChainCandidate{
			IsOrphan: true,
			BestPair: PairScore{Candidate: cand, WeightedScore: 0.7},
		},
		FeatureScoresJSON: []byte(`{}`),
	}
	m, chainRepo, _ := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()

	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate failed: %v", err)
	}
	if meeting.ChainID == nil {
		t.Fatal("expected meeting.ChainID set after promote")
	}
	if _, ok := chainRepo.chainsByID[*meeting.ChainID]; !ok {
		t.Error("expected freshly-created chain registered in chainRepo")
	}
	// Phase 4 review fix: the freshly-created chain MUST propagate
	// back to the caller's Decision so PersistAfterIngest can write a
	// correctly-attributed audit row. Without this, the applied row's
	// CandidateChainID falls to NULL and analytics can't reconstruct
	// which v3 decision produced the chain.
	if d.Chain == nil || d.Chain.ID != *meeting.ChainID {
		t.Errorf("expected decision.Chain back-propagated to %v, got %+v", meeting.ChainID, d.Chain)
	}
}

func TestV3_Mutate_SkipIsNoOp(t *testing.T) {
	t.Parallel()
	m, _, _ := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	prevChain := meeting.ChainID
	prevMethod := meeting.ChainAssignmentMethod
	d := Decision{Action: ActionSkip}
	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate skip should not error: %v", err)
	}
	if meeting.ChainID != prevChain || meeting.ChainAssignmentMethod != prevMethod {
		t.Error("Skip mutation should be a no-op")
	}
}

func TestV3_Mutate_SeriesNewBackPropagatesChain(t *testing.T) {
	t.Parallel()
	// Sentinel from Decide for first-occurrence series — Mutate must
	// materialise the chain and republish it on the Decision so the
	// audit row can attribute the new chain UUID.
	seriesID := "series-first-time"
	d := Decision{
		Action: ActionSeriesID,
		Chain:  &models.MeetingChain{}, // sentinel: zero ID
	}
	m, chainRepo, _ := newV3WithFakes(nil)
	meeting := makeV3Meeting("Standup", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()
	meeting.CalendarSeriesID = &seriesID

	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate failed: %v", err)
	}
	if d.Chain == nil || d.Chain.ID == uuid.Nil {
		t.Fatalf("expected new chain materialised on Decision, got %+v", d.Chain)
	}
	if meeting.ChainID == nil || *meeting.ChainID != d.Chain.ID {
		t.Errorf("expected meeting.ChainID == decision.Chain.ID (%v), got %v", d.Chain.ID, meeting.ChainID)
	}
	if _, ok := chainRepo.chainsByID[d.Chain.ID]; !ok {
		t.Error("expected freshly-created series chain registered in repo")
	}
}

// ==================== PersistAfterIngest ====================

func TestV3_Persist_AutoScoreChainWritesAppliedRow(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionAutoScoreChain,
		Chain:  &models.MeetingChain{ID: chainID},
		Winner: &ChainCandidate{
			ChainID:  chainID,
			BestPair: PairScore{Candidate: cand, ParticipantOverlap: 1.0, TitleSimilarity: 1.0, WeightedScore: 0.9},
		},
		FeatureScoresJSON: []byte(`{"k":"v"}`),
	}
	m, _, dec := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)

	if err := m.PersistAfterIngest(context.Background(), meeting, d); err != nil {
		t.Fatalf("persist failed: %v", err)
	}
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 applied row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	if row.Mode != "applied" || row.Status != "applied" {
		t.Errorf("expected mode=applied status=applied, got %s/%s", row.Mode, row.Status)
	}
	if row.CandidateChainID == nil || *row.CandidateChainID != chainID {
		t.Errorf("expected candidate_chain_id %s, got %v", chainID, row.CandidateChainID)
	}
}

func TestV3_Persist_AutoScoreOrphanRepointsCandidate(t *testing.T) {
	t.Parallel()
	orphanID := uuid.New()
	cand := repository.CandidateMeeting{ID: orphanID}
	chainID := uuid.New()
	d := Decision{
		Action: ActionAutoScoreOrphan,
		// Phase 4 review: Decision.Chain MUST be populated by the
		// time PersistAfterIngest runs (Mutate publishes it after
		// CreateChain). Mock that explicitly here so the unit test
		// exercises the post-Mutate state without going through
		// Mutate itself.
		Chain: &models.MeetingChain{ID: chainID},
		Winner: &ChainCandidate{
			IsOrphan: true,
			BestPair: PairScore{Candidate: cand, WeightedScore: 0.7},
		},
		FeatureScoresJSON: []byte(`{}`),
	}
	m, _, _, mr := newV3WithMeetingRepo(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.ChainID = &chainID // already stamped by Mutate

	if err := m.PersistAfterIngest(context.Background(), meeting, d); err != nil {
		t.Fatalf("persist failed: %v", err)
	}
	upd, ok := mr.updates[orphanID]
	if !ok {
		t.Fatalf("expected orphan %s to be repointed; updates=%v", orphanID, mr.updates)
	}
	if upd["chain_id"] != *meeting.ChainID {
		t.Errorf("expected chain_id %v, got %v", meeting.ChainID, upd["chain_id"])
	}
}

func TestV3_Persist_SuggestionWritesPendingRow(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionSuggest,
		Chain:  &models.MeetingChain{ID: chainID},
		Winner: &ChainCandidate{
			ChainID:  chainID,
			BestPair: PairScore{Candidate: cand, WeightedScore: 0.45},
		},
		FeatureScoresJSON: []byte(`{}`),
	}
	m, _, dec := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)

	if err := m.PersistAfterIngest(context.Background(), meeting, d); err != nil {
		t.Fatalf("persist failed: %v", err)
	}
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	if row.Mode != "suggestion" || row.Status != "pending" {
		t.Errorf("expected mode=suggestion status=pending, got %s/%s", row.Mode, row.Status)
	}
}

func TestV3_Persist_RateLimitedWritesShadowRow(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionRateLimited,
		Chain:  &models.MeetingChain{ID: chainID},
		Winner: &ChainCandidate{
			ChainID:  chainID,
			BestPair: PairScore{Candidate: cand, WeightedScore: 0.45},
		},
	}
	m, _, dec := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)

	_ = m.PersistAfterIngest(context.Background(), meeting, d)
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	// Phase 4 review fix: rate-limited rows use status='shadow_only'.
	// 'expired' is reserved for previously-pending suggestions that
	// timed out — a rate-limited row was never pending in the first
	// place.
	if row.Mode != "shadow" || row.Status != "shadow_only" {
		t.Errorf("expected mode=shadow status=shadow_only (rate-limited analytics row), got %s/%s",
			row.Mode, row.Status)
	}
}

func TestV3_Persist_SuppressedWritesShadowRowWithSuppressedStatus(t *testing.T) {
	t.Parallel()
	chainID := uuid.New()
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionSuppressed,
		Chain:  &models.MeetingChain{ID: chainID},
		Winner: &ChainCandidate{
			ChainID:  chainID,
			BestPair: PairScore{Candidate: cand, WeightedScore: 0.45},
		},
	}
	m, _, dec := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)

	_ = m.PersistAfterIngest(context.Background(), meeting, d)
	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(dec.rows))
	}
	if dec.rows[0].Status != "suppressed" {
		t.Errorf("expected status=suppressed, got %s", dec.rows[0].Status)
	}
}

func TestV3_Persist_OrphanIsNoOp(t *testing.T) {
	t.Parallel()
	m, _, dec := newV3WithFakes(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	if err := m.PersistAfterIngest(context.Background(), meeting, Decision{Action: ActionOrphan}); err != nil {
		t.Fatalf("persist orphan should not error: %v", err)
	}
	if len(dec.rows) != 0 {
		t.Errorf("expected no rows for orphan action, got %d", len(dec.rows))
	}
}

// ==================== Mutate → Persist round-trip ====================
//
// Locks the contract that any chain row MutateMeetingForDecision
// materialises (series-new, orphan-promotion) propagates back through
// Decision.Chain so PersistAfterIngest's audit row attributes the
// correct CandidateChainID. Earlier tests exercised Mutate and Persist
// in isolation, which let a pass-by-value bug slip past — these
// guard against the regression.

func TestV3_RoundTrip_AutoScoreOrphanAuditRowReferencesNewChain(t *testing.T) {
	t.Parallel()
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionAutoScoreOrphan,
		Winner: &ChainCandidate{
			IsOrphan: true,
			BestPair: PairScore{Candidate: cand, ParticipantOverlap: 0.8, TitleSimilarity: 0.9, WeightedScore: 0.7},
		},
		FeatureScoresJSON: []byte(`{"k":"v"}`),
	}
	m, _, dec, mr := newV3WithMeetingRepo(nil)
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()

	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate failed: %v", err)
	}
	if err := m.PersistAfterIngest(context.Background(), meeting, d); err != nil {
		t.Fatalf("persist failed: %v", err)
	}

	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 applied row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	if row.CandidateChainID == nil {
		t.Fatal("expected CandidateChainID populated on applied row, got nil")
	}
	if *row.CandidateChainID != *meeting.ChainID {
		t.Errorf("audit row chain (%s) doesn't match meeting chain (%s)",
			*row.CandidateChainID, *meeting.ChainID)
	}
	if row.CandidateKey != "chain:"+meeting.ChainID.String() {
		t.Errorf("expected candidate_key %s, got %s", "chain:"+meeting.ChainID.String(), row.CandidateKey)
	}
	if row.Mode != "applied" || row.Status != "applied" {
		t.Errorf("expected mode=applied status=applied, got %s/%s", row.Mode, row.Status)
	}
	// Orphan was repointed at the same chain.
	upd, ok := mr.updates[cand.ID]
	if !ok {
		t.Fatal("expected orphan repoint")
	}
	if upd["chain_id"] != *meeting.ChainID {
		t.Errorf("orphan repoint chain mismatch: got %v want %v", upd["chain_id"], meeting.ChainID)
	}
}

func TestV3_RoundTrip_SeriesNewAuditRowReferencesCreatedChain(t *testing.T) {
	t.Parallel()
	seriesID := "series-roundtrip"
	d := Decision{
		Action: ActionSeriesID,
		Chain:  &models.MeetingChain{}, // sentinel: zero ID, asks Mutate to create
	}
	m, _, dec := newV3WithFakes(nil)
	meeting := makeV3Meeting("Standup", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()
	meeting.CalendarSeriesID = &seriesID

	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate failed: %v", err)
	}
	if err := m.PersistAfterIngest(context.Background(), meeting, d); err != nil {
		t.Fatalf("persist failed: %v", err)
	}

	if len(dec.rows) != 1 {
		t.Fatalf("expected 1 applied row, got %d", len(dec.rows))
	}
	row := dec.rows[0]
	if row.CandidateChainID == nil || *row.CandidateChainID != *meeting.ChainID {
		t.Errorf("series-new audit row should reference the freshly-created chain (%v), got %v",
			meeting.ChainID, row.CandidateChainID)
	}
	if row.CandidateKey == "chain:00000000-0000-0000-0000-000000000000" || row.CandidateKey == "chain:unknown" {
		t.Errorf("audit row carrying a sentinel/unknown candidate_key: %s", row.CandidateKey)
	}
}

func TestV3_RoundTrip_AutoScoreOrphanCascadesDecisionItems(t *testing.T) {
	t.Parallel()
	// Phase 4 review: orphan promotion must repoint the orphan's
	// existing decision_items to the new chain. Without this, chain
	// rollups would miss the orphan's history after promotion.
	cand := repository.CandidateMeeting{ID: uuid.New()}
	d := Decision{
		Action: ActionAutoScoreOrphan,
		Winner: &ChainCandidate{
			IsOrphan: true,
			BestPair: PairScore{Candidate: cand, WeightedScore: 0.7},
		},
		FeatureScoresJSON: []byte(`{}`),
	}
	itemRepo := &cascadeRecordingItemRepo{}
	m, _, _, _ := newV3WithMeetingRepo(nil)
	m.itemRepo = itemRepo
	meeting := makeV3Meeting("X", nil, v3TestNow, 30*time.Minute)
	meeting.UserID = uuid.New()

	if err := m.MutateMeetingForDecision(context.Background(), meeting, &d); err != nil {
		t.Fatalf("mutate failed: %v", err)
	}
	if err := m.PersistAfterIngest(context.Background(), meeting, d); err != nil {
		t.Fatalf("persist failed: %v", err)
	}

	if len(itemRepo.cascades) != 1 {
		t.Fatalf("expected 1 decision_items cascade, got %d", len(itemRepo.cascades))
	}
	c := itemRepo.cascades[0]
	if c.meetingID != cand.ID {
		t.Errorf("expected cascade for orphan meeting %s, got %s", cand.ID, c.meetingID)
	}
	if c.chainID != *meeting.ChainID {
		t.Errorf("expected cascade chain %v, got %v", *meeting.ChainID, c.chainID)
	}
}

// cascadeRecordingItemRepo records UpdateChainIDForMeeting calls so
// the round-trip test can verify the orphan's items get repointed.
type cascadeRecordingItemRepo struct {
	repository.DecisionRepository
	cascades []struct {
		meetingID uuid.UUID
		chainID   uuid.UUID
	}
}

func (r *cascadeRecordingItemRepo) UpdateChainIDForMeeting(_ context.Context, meetingID, chainID uuid.UUID) error {
	r.cascades = append(r.cascades, struct {
		meetingID uuid.UUID
		chainID   uuid.UUID
	}{meetingID, chainID})
	return nil
}

// scoreOf returns the winner's weighted score, or 0 if winner is nil.
// Convenience for the suggestion-band recipe assertion.
func scoreOf(c *ChainCandidate) float64 {
	if c == nil {
		return 0
	}
	return c.BestPair.WeightedScore
}

// ==================== helpers ====================

func attendeeSlice(emails []string) []models.Attendee {
	out := make([]models.Attendee, len(emails))
	for i, e := range emails {
		out[i] = models.Attendee{Email: e}
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
