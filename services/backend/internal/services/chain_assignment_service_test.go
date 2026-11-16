package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== fakes ====================

type fakeMeetingRepoCA struct {
	repository.MeetingRepository
	chainAssignment       map[uuid.UUID]repository.SetChainAssignmentInput
	chainAssignmentOwner  map[uuid.UUID]uuid.UUID
	clearLockCalls        []uuid.UUID
	systemMeetings        map[uuid.UUID]*models.Meeting
	setAssignmentErr      error
	clearLockErr          error
	setAssignmentNotOwned bool
	clearLockNotOwned     bool
}

func (f *fakeMeetingRepoCA) SetChainAssignment(_ context.Context, userID, meetingID uuid.UUID, in repository.SetChainAssignmentInput) (bool, error) {
	if f.setAssignmentErr != nil {
		return false, f.setAssignmentErr
	}
	if f.setAssignmentNotOwned {
		return false, nil
	}
	if f.chainAssignment == nil {
		f.chainAssignment = map[uuid.UUID]repository.SetChainAssignmentInput{}
		f.chainAssignmentOwner = map[uuid.UUID]uuid.UUID{}
	}
	f.chainAssignment[meetingID] = in
	f.chainAssignmentOwner[meetingID] = userID
	return true, nil
}

func (f *fakeMeetingRepoCA) ClearChainAssignmentLock(_ context.Context, _, meetingID uuid.UUID) (bool, error) {
	if f.clearLockErr != nil {
		return false, f.clearLockErr
	}
	if f.clearLockNotOwned {
		return false, nil
	}
	f.clearLockCalls = append(f.clearLockCalls, meetingID)
	return true, nil
}

func (f *fakeMeetingRepoCA) GetMeetingByIDSystem(_ context.Context, id uuid.UUID) (*models.Meeting, error) {
	if m, ok := f.systemMeetings[id]; ok {
		return m, nil
	}
	return nil, errors.New("not found")
}

type fakeChainRepoCA struct {
	repository.ChainRepository
	chains map[uuid.UUID]*models.MeetingChain
}

func (f *fakeChainRepoCA) FindByID(_ context.Context, id uuid.UUID) (*models.MeetingChain, error) {
	if c, ok := f.chains[id]; ok {
		return c, nil
	}
	return nil, errors.New("not found")
}

type fakeDecisionsRepoCA struct {
	rows               map[uuid.UUID]repository.ChainDecisionV2Row
	pendingByUser      map[uuid.UUID][]repository.ChainDecisionV2Row
	markStatusCalls    []markStatusCall
	acceptCalls        []acceptCall
	expiredMeetings    []uuid.UUID
	lastNSince         []time.Time
	declinedCountByKey map[string]int
	suppressedKeys     []string
	getByIDErr         error
	markStatusErr      error
	acceptErr          error
}

type markStatusCall struct {
	id     uuid.UUID
	userID uuid.UUID
	status string
}

type acceptCall struct {
	id     uuid.UUID
	userID uuid.UUID
	method string
}

func (f *fakeDecisionsRepoCA) LogShadowDecision(_ context.Context, _ repository.ChainDecisionV2Row) error {
	return nil
}
func (f *fakeDecisionsRepoCA) ListPendingForUser(_ context.Context, userID uuid.UUID, _ int) ([]repository.ChainDecisionV2Row, error) {
	return f.pendingByUser[userID], nil
}
func (f *fakeDecisionsRepoCA) GetByIDForUser(_ context.Context, id, userID uuid.UUID) (*repository.ChainDecisionV2Row, error) {
	if f.getByIDErr != nil {
		return nil, f.getByIDErr
	}
	row, ok := f.rows[id]
	if !ok || row.UserID != userID {
		return nil, repository.ErrChainDecisionNotFound
	}
	return &row, nil
}
func (f *fakeDecisionsRepoCA) MarkStatus(_ context.Context, id, userID uuid.UUID, status string) error {
	if f.markStatusErr != nil {
		return f.markStatusErr
	}
	f.markStatusCalls = append(f.markStatusCalls, markStatusCall{id, userID, status})
	if row, ok := f.rows[id]; ok {
		row.Status = status
		f.rows[id] = row
	}
	return nil
}
func (f *fakeDecisionsRepoCA) AcceptPendingSuggestion(_ context.Context, id, userID uuid.UUID, method string) (*repository.ChainDecisionV2Row, error) {
	if f.acceptErr != nil {
		return nil, f.acceptErr
	}
	f.acceptCalls = append(f.acceptCalls, acceptCall{id, userID, method})
	row, ok := f.rows[id]
	if !ok || row.UserID != userID || row.Status != "pending" {
		return nil, repository.ErrChainDecisionNotFound
	}
	if row.CandidateChainID == nil {
		return nil, repository.ErrChainDecisionMissingCandidateChain
	}
	row.Status = "accepted"
	f.rows[id] = row
	return &row, nil
}
func (f *fakeDecisionsRepoCA) ExpirePendingForMeeting(_ context.Context, _ uuid.UUID, meetingID uuid.UUID) (int, error) {
	f.expiredMeetings = append(f.expiredMeetings, meetingID)
	return 1, nil
}
func (f *fakeDecisionsRepoCA) CountDeclinedByKeySince(_ context.Context, _ uuid.UUID, key string, _ time.Time) (int, error) {
	return f.declinedCountByKey[key], nil
}
func (f *fakeDecisionsRepoCA) MarkSuppressedByKey(_ context.Context, _ uuid.UUID, key string) (int, error) {
	f.suppressedKeys = append(f.suppressedKeys, key)
	return 1, nil
}
func (f *fakeDecisionsRepoCA) CountPendingForUserSince(_ context.Context, _ uuid.UUID, _ time.Time) (int, error) {
	return 0, nil
}
func (f *fakeDecisionsRepoCA) LastNDecisionsAllDeclined(_ context.Context, _ uuid.UUID, key string, n int, since time.Time) (bool, error) {
	f.lastNSince = append(f.lastNSince, since)
	// "Streak hit" iff we have at least n recorded declines for this key.
	// Tests pre-populate declinedCountByKey to drive the suppression
	// trigger; the count semantics map cleanly to the streak semantics
	// because the existing tests don't interleave accepts.
	return f.declinedCountByKey[key] >= n, nil
}

type fakeDecisionRepoCA struct {
	repository.DecisionRepository
	cascadedChainID      map[uuid.UUID]uuid.UUID
	cascadedNullMeetings []uuid.UUID
}

func (f *fakeDecisionRepoCA) UpdateChainIDForMeeting(_ context.Context, meetingID, chainID uuid.UUID) error {
	if f.cascadedChainID == nil {
		f.cascadedChainID = map[uuid.UUID]uuid.UUID{}
	}
	f.cascadedChainID[meetingID] = chainID
	return nil
}
func (f *fakeDecisionRepoCA) UpdateChainIDForMeetingNull(_ context.Context, meetingID uuid.UUID) error {
	f.cascadedNullMeetings = append(f.cascadedNullMeetings, meetingID)
	return nil
}

func newCASvcWithFakes() (*ChainAssignmentService, *fakeMeetingRepoCA, *fakeChainRepoCA, *fakeDecisionsRepoCA, *fakeDecisionRepoCA) {
	mr := &fakeMeetingRepoCA{}
	cr := &fakeChainRepoCA{chains: map[uuid.UUID]*models.MeetingChain{}}
	dr := &fakeDecisionsRepoCA{rows: map[uuid.UUID]repository.ChainDecisionV2Row{}, pendingByUser: map[uuid.UUID][]repository.ChainDecisionV2Row{}, declinedCountByKey: map[string]int{}}
	ir := &fakeDecisionRepoCA{}
	return &ChainAssignmentService{
		meetingRepo:   mr,
		chainRepo:     cr,
		decisionsRepo: dr,
		itemRepo:      ir,
	}, mr, cr, dr, ir
}

// ==================== SetManualChain ====================

func TestCA_SetManualChain_WritesProvenance(t *testing.T) {
	t.Parallel()
	svc, mr, cr, dr, ir := newCASvcWithFakes()
	user := uuid.New()
	chainID := uuid.New()
	meetingID := uuid.New()
	cr.chains[chainID] = &models.MeetingChain{ID: chainID, UserID: user}

	if err := svc.SetManualChain(context.Background(), user, meetingID, chainID); err != nil {
		t.Fatalf("SetManualChain failed: %v", err)
	}

	in := mr.chainAssignment[meetingID]
	if in.ChainID == nil || *in.ChainID != chainID {
		t.Errorf("expected chain_id=%s, got %v", chainID, in.ChainID)
	}
	if in.Method == nil || *in.Method != models.ChainAssignmentMethodManualUser {
		t.Errorf("expected method=manual_user, got %v", in.Method)
	}
	if in.LockedAt == nil || in.LockedAt.IsZero() {
		t.Errorf("expected locked_at to be set, got %v", in.LockedAt)
	}
	if ir.cascadedChainID[meetingID] != chainID {
		t.Errorf("expected decision_items cascade to %s, got %v", chainID, ir.cascadedChainID[meetingID])
	}
	if len(dr.expiredMeetings) != 1 || dr.expiredMeetings[0] != meetingID {
		t.Errorf("expected stale pending suggestions to expire for %s, got %v", meetingID, dr.expiredMeetings)
	}
}

func TestCA_SetManualChain_ChainNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newCASvcWithFakes()
	err := svc.SetManualChain(context.Background(), uuid.New(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrChainNotFound) {
		t.Errorf("expected ErrChainNotFound, got %v", err)
	}
}

func TestCA_SetManualChain_RejectsCrossUserChain(t *testing.T) {
	t.Parallel()
	svc, _, cr, _, _ := newCASvcWithFakes()
	chainID := uuid.New()
	otherUser := uuid.New()
	cr.chains[chainID] = &models.MeetingChain{ID: chainID, UserID: otherUser}

	err := svc.SetManualChain(context.Background(), uuid.New(), uuid.New(), chainID)
	if !errors.Is(err, ErrChainAcrossUsers) {
		t.Errorf("expected ErrChainAcrossUsers, got %v", err)
	}
}

func TestCA_SetManualChain_MeetingNotOwned(t *testing.T) {
	t.Parallel()
	svc, mr, cr, _, _ := newCASvcWithFakes()
	user := uuid.New()
	chainID := uuid.New()
	cr.chains[chainID] = &models.MeetingChain{ID: chainID, UserID: user}
	mr.setAssignmentNotOwned = true

	err := svc.SetManualChain(context.Background(), user, uuid.New(), chainID)
	if !errors.Is(err, ErrMeetingNotOwned) {
		t.Errorf("expected ErrMeetingNotOwned, got %v", err)
	}
}

// ==================== RemoveFromChain ====================

func TestCA_RemoveFromChain_ClearsChainAndWritesProvenance(t *testing.T) {
	t.Parallel()
	svc, mr, _, dr, ir := newCASvcWithFakes()
	user := uuid.New()
	meetingID := uuid.New()

	if err := svc.RemoveFromChain(context.Background(), user, meetingID); err != nil {
		t.Fatalf("RemoveFromChain failed: %v", err)
	}

	in := mr.chainAssignment[meetingID]
	if in.ChainID != nil {
		t.Errorf("expected chain_id NULL, got %v", in.ChainID)
	}
	if in.Method == nil || *in.Method != models.ChainAssignmentMethodManualRemoved {
		t.Errorf("expected method=manual_removed, got %v", in.Method)
	}
	if in.LockedAt == nil {
		t.Error("expected locked_at to be set")
	}
	if len(ir.cascadedNullMeetings) != 1 || ir.cascadedNullMeetings[0] != meetingID {
		t.Errorf("expected NULL cascade for %s, got %v", meetingID, ir.cascadedNullMeetings)
	}
	if len(dr.expiredMeetings) != 1 || dr.expiredMeetings[0] != meetingID {
		t.Errorf("expected stale pending suggestions to expire for %s, got %v", meetingID, dr.expiredMeetings)
	}
}

// ==================== RetryAutoDetect ====================

func TestCA_RetryAutoDetect_ClearsLockOnly(t *testing.T) {
	t.Parallel()
	svc, mr, _, _, _ := newCASvcWithFakes()
	meetingID := uuid.New()

	if err := svc.RetryAutoDetect(context.Background(), uuid.New(), meetingID); err != nil {
		t.Fatalf("RetryAutoDetect failed: %v", err)
	}
	if len(mr.clearLockCalls) != 1 || mr.clearLockCalls[0] != meetingID {
		t.Errorf("expected ClearChainAssignmentLock on %s, got %v", meetingID, mr.clearLockCalls)
	}
	// Should NOT call SetChainAssignment
	if len(mr.chainAssignment) != 0 {
		t.Errorf("expected no SetChainAssignment, got %d calls", len(mr.chainAssignment))
	}
}

func TestCA_RetryAutoDetect_NotOwned(t *testing.T) {
	t.Parallel()
	svc, mr, _, _, _ := newCASvcWithFakes()
	mr.clearLockNotOwned = true
	err := svc.RetryAutoDetect(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrMeetingNotOwned) {
		t.Errorf("expected ErrMeetingNotOwned, got %v", err)
	}
}

// ==================== AcceptSuggestion ====================

func TestCA_AcceptSuggestion_HappyPath(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, ir := newCASvcWithFakes()
	user := uuid.New()
	meetingID := uuid.New()
	chainID := uuid.New()
	decisionID := uuid.New()
	score := 0.85
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID:               decisionID,
		UserID:           user,
		MeetingID:        meetingID,
		CandidateChainID: &chainID,
		WeightedScore:    &score,
		Status:           "pending",
		FeatureScores:    []byte(`{"k":"v"}`),
	}

	if err := svc.AcceptSuggestion(context.Background(), user, decisionID); err != nil {
		t.Fatalf("AcceptSuggestion failed: %v", err)
	}

	if len(dr.acceptCalls) != 1 {
		t.Fatalf("expected AcceptPendingSuggestion once, got %v", dr.acceptCalls)
	}
	if dr.acceptCalls[0].method != models.ChainAssignmentMethodLLMSuggestion {
		t.Errorf("expected method=llm_suggestion, got %s", dr.acceptCalls[0].method)
	}
	if len(dr.markStatusCalls) != 0 {
		t.Errorf("accept must not MarkStatus outside the transaction, got %v", dr.markStatusCalls)
	}
	if ir.cascadedChainID[meetingID] != chainID {
		t.Error("expected decision_items cascade")
	}
}

func TestCA_AcceptSuggestion_NotPending(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	decisionID := uuid.New()
	chainID := uuid.New()
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID:               decisionID,
		UserID:           user,
		CandidateChainID: &chainID,
		Status:           "declined", // already declined
	}
	err := svc.AcceptSuggestion(context.Background(), user, decisionID)
	if !errors.Is(err, ErrSuggestionNotPending) {
		t.Errorf("expected ErrSuggestionNotPending, got %v", err)
	}
}

func TestCA_AcceptSuggestion_ConcurrentResolutionDoesNotCascade(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, ir := newCASvcWithFakes()
	user := uuid.New()
	decisionID := uuid.New()
	meetingID := uuid.New()
	chainID := uuid.New()
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID:               decisionID,
		UserID:           user,
		MeetingID:        meetingID,
		CandidateChainID: &chainID,
		Status:           "pending",
	}
	dr.acceptErr = repository.ErrChainDecisionNotFound

	err := svc.AcceptSuggestion(context.Background(), user, decisionID)
	if !errors.Is(err, ErrSuggestionNotPending) {
		t.Fatalf("expected ErrSuggestionNotPending, got %v", err)
	}
	if len(ir.cascadedChainID) != 0 {
		t.Fatalf("must not cascade decision_items when accept transaction fails, got %v", ir.cascadedChainID)
	}
}

func TestCA_AcceptSuggestion_OrphanCandidateRejected(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	decisionID := uuid.New()
	matchedMeeting := uuid.New()
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID:               decisionID,
		UserID:           user,
		CandidateChainID: nil, // orphan candidate
		MatchedMeetingID: &matchedMeeting,
		Status:           "pending",
	}
	err := svc.AcceptSuggestion(context.Background(), user, decisionID)
	if !errors.Is(err, ErrSuggestionMissingChain) {
		t.Errorf("expected ErrSuggestionMissingChain, got %v", err)
	}
}

func TestCA_AcceptSuggestion_DecisionNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newCASvcWithFakes()
	err := svc.AcceptSuggestion(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, repository.ErrChainDecisionNotFound) {
		t.Errorf("expected ErrChainDecisionNotFound, got %v", err)
	}
}

// ==================== DeclineSuggestion ====================

func TestCA_DeclineSuggestion_BelowSuppressionThreshold(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	decisionID := uuid.New()
	key := "metadata:chain:abc"
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID: decisionID, UserID: user, Status: "pending", SuppressionKey: &key,
	}
	dr.declinedCountByKey[key] = 2 // below threshold (3)

	if err := svc.DeclineSuggestion(context.Background(), user, decisionID); err != nil {
		t.Fatalf("DeclineSuggestion failed: %v", err)
	}

	if len(dr.markStatusCalls) != 1 || dr.markStatusCalls[0].status != "declined" {
		t.Errorf("expected MarkStatus(declined), got %v", dr.markStatusCalls)
	}
	if len(dr.suppressedKeys) != 0 {
		t.Errorf("expected NO bulk suppress at count<3, got %v", dr.suppressedKeys)
	}
}

func TestCA_DeclineSuggestion_AtThresholdTriggersSuppress(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	decisionID := uuid.New()
	key := "metadata:chain:abc"
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID: decisionID, UserID: user, Status: "pending", SuppressionKey: &key,
	}
	dr.declinedCountByKey[key] = 3 // at threshold

	if err := svc.DeclineSuggestion(context.Background(), user, decisionID); err != nil {
		t.Fatalf("DeclineSuggestion failed: %v", err)
	}

	if len(dr.suppressedKeys) != 1 || dr.suppressedKeys[0] != key {
		t.Errorf("expected MarkSuppressedByKey(%q), got %v", key, dr.suppressedKeys)
	}
	if len(dr.lastNSince) != 1 || dr.lastNSince[0].IsZero() {
		t.Fatalf("expected suppression window to be passed to streak check, got %v", dr.lastNSince)
	}
	age := time.Since(dr.lastNSince[0])
	if age < suppressionWindow-time.Minute || age > suppressionWindow+time.Minute {
		t.Fatalf("expected 30d suppression window, got age %s", age)
	}
}

func TestCA_DeclineSuggestion_NoSuppressionKeySkipsCount(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	decisionID := uuid.New()
	dr.rows[decisionID] = repository.ChainDecisionV2Row{
		ID: decisionID, UserID: user, Status: "pending", SuppressionKey: nil,
	}

	if err := svc.DeclineSuggestion(context.Background(), user, decisionID); err != nil {
		t.Fatalf("DeclineSuggestion failed: %v", err)
	}
	if len(dr.suppressedKeys) != 0 {
		t.Error("expected no suppression action when key absent")
	}
}

// ==================== ListPendingSuggestions ====================

func TestCA_ListPendingSuggestions_EnrichmentAndFallback(t *testing.T) {
	t.Parallel()
	svc, mr, cr, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	meetingID := uuid.New()
	chainID := uuid.New()
	decisionID := uuid.New()

	mr.systemMeetings = map[uuid.UUID]*models.Meeting{
		meetingID: {ID: meetingID, Title: "Roadmap Sync", StartTime: time.Now()},
	}
	chainName := "Product Roadmap"
	cr.chains[chainID] = &models.MeetingChain{ID: chainID, UserID: user, DisplayName: &chainName}
	dr.pendingByUser[user] = []repository.ChainDecisionV2Row{
		{
			ID:               decisionID,
			UserID:           user,
			MeetingID:        meetingID,
			CandidateChainID: &chainID,
			CandidateKey:     "chain:" + chainID.String(),
			Status:           "pending",
			CreatedAt:        time.Now(),
		},
	}

	out, err := svc.ListPendingSuggestions(context.Background(), user, 10)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 suggestion, got %d", len(out))
	}
	if out[0].MeetingTitle != "Roadmap Sync" {
		t.Errorf("expected meeting title 'Roadmap Sync', got %q", out[0].MeetingTitle)
	}
	if out[0].CandidateLabel != "Product Roadmap" {
		t.Errorf("expected candidate label 'Product Roadmap', got %q", out[0].CandidateLabel)
	}
}

func TestCA_ListPendingSuggestions_FallsBackToCandidateKeyOnLookupMiss(t *testing.T) {
	t.Parallel()
	svc, _, _, dr, _ := newCASvcWithFakes()
	user := uuid.New()
	chainID := uuid.New()
	dr.pendingByUser[user] = []repository.ChainDecisionV2Row{
		{
			UserID:           user,
			CandidateChainID: &chainID, // chain not registered → lookup miss
			CandidateKey:     "chain:" + chainID.String(),
			Status:           "pending",
		},
	}

	out, _ := svc.ListPendingSuggestions(context.Background(), user, 10)
	if len(out) != 1 {
		t.Fatalf("expected row to survive lookup miss, got %d", len(out))
	}
	if out[0].CandidateLabel != "chain:"+chainID.String() {
		t.Errorf("expected fallback label, got %q", out[0].CandidateLabel)
	}
}

func TestCA_ListPendingSuggestions_RejectsNilUser(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newCASvcWithFakes()
	if _, err := svc.ListPendingSuggestions(context.Background(), uuid.Nil, 10); err == nil {
		t.Error("expected error for nil user")
	}
}
