package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ChainAssignmentService is Phase 3 of the chain-detection roadmap:
// the user-facing surface that lets people fix wrong chain assignments
// and act on LLM suggestions.
//
// All five state transitions go through here so the cascade rules
// (decision_items.chain_id sync) and provenance writes
// (chain_assignment_method + locked_at) can't drift between handlers.
//
// The service owns no business state; it composes
// MeetingRepository + ChainRepository + ChainDecisionsV2Repository +
// DecisionRepository. This makes it trivially testable with fakes.
type ChainAssignmentService struct {
	meetingRepo   repository.MeetingRepository
	chainRepo     repository.ChainRepository
	decisionsRepo repository.ChainDecisionsV2Repository
	itemRepo      repository.DecisionRepository
}

func NewChainAssignmentService() *ChainAssignmentService {
	return &ChainAssignmentService{
		meetingRepo:   repository.NewMeetingRepository(),
		chainRepo:     repository.NewChainRepository(),
		decisionsRepo: repository.NewChainDecisionsV2Repository(),
		itemRepo:      repository.NewDecisionRepository(),
	}
}

// Errors surfaced by the service. Handlers translate these to status
// codes; tests assert on the sentinel rather than string match.
var (
	// ErrChainNotFound — the target chain doesn't exist (or doesn't
	// belong to the calling user, once chain ownership is checked).
	ErrChainNotFound = errors.New("chain not found")

	// ErrChainAcrossUsers — the requested chain belongs to a different
	// user. Refuses cross-tenant chain assignment so a stolen chain id
	// can't be used to expose another user's chain to your meetings.
	ErrChainAcrossUsers = errors.New("chain belongs to another user")

	// ErrSuggestionNotPending — accept/decline was called on a
	// suggestion that's not in 'pending' state (already accepted /
	// declined / suppressed / expired). Idempotency: avoids double
	// state transitions.
	ErrSuggestionNotPending = errors.New("suggestion is not pending")

	// ErrSuggestionMissingChain — accept was called on an orphan-only
	// suggestion (candidate is a meeting, not a chain). Phase 4 may
	// promote orphan candidates to chains on accept; for Phase 3 we
	// reject — manual move covers the orphan case explicitly.
	ErrSuggestionMissingChain = errors.New("suggestion has no candidate chain")
)

// suppression policy from docs/CHAIN_DETECTION_ROADMAP.md
// §Suggestion limits: 3 declines in 30 days → suppress.
const (
	suppressionDeclineLimit  = 3
	suppressionWindow        = 30 * 24 * time.Hour
	pendingSuggestionPageMax = 50
)

// SetManualChain assigns a meeting to an explicit chain at the user's
// request. Writes provenance (method='manual_user', locked_at=now)
// and cascades chain_id to the meeting's decision_items so downstream
// queries (brief generator, chain rollups) see the new linkage.
//
// The chain must exist and belong to the same user. Cross-user
// chain references return ErrChainAcrossUsers, never falling through
// to a constraint violation.
func (s *ChainAssignmentService) SetManualChain(
	ctx context.Context,
	userID, meetingID, chainID uuid.UUID,
) error {
	if userID == uuid.Nil || meetingID == uuid.Nil || chainID == uuid.Nil {
		return fmt.Errorf("SetManualChain: all ids required")
	}

	chain, err := s.chainRepo.FindByID(ctx, chainID)
	if err != nil || chain == nil {
		return ErrChainNotFound
	}
	if chain.UserID != userID {
		return ErrChainAcrossUsers
	}

	now := time.Now()
	method := models.ChainAssignmentMethodManualUser
	in := repository.SetChainAssignmentInput{
		ChainID:  &chainID,
		Method:   &method,
		LockedAt: &now,
	}
	updated, err := s.meetingRepo.SetChainAssignment(ctx, userID, meetingID, in)
	if err != nil {
		return fmt.Errorf("SetManualChain: write assignment: %w", err)
	}
	if !updated {
		return ErrMeetingNotOwned
	}

	if err := s.itemRepo.UpdateChainIDForMeeting(ctx, meetingID, chainID); err != nil {
		// Don't unwind the chain assignment — the source of truth is
		// meetings.chain_id, decision_items just denormalises it.
		// A separate reconciliation job (or the next decision write)
		// will fix the drift. Log loudly so we notice if this happens
		// at scale.
		slog.Error("chain_id cascade to decision_items failed",
			"meeting_id", meetingID, "chain_id", chainID, "error", err)
	}
	if n, err := s.decisionsRepo.ExpirePendingForMeeting(ctx, userID, meetingID); err != nil {
		slog.Warn("expire stale chain suggestions after manual assign failed",
			"meeting_id", meetingID, "chain_id", chainID, "error", err)
	} else if n > 0 {
		slog.Info("expired stale chain suggestions after manual assign",
			"meeting_id", meetingID, "chain_id", chainID, "expired", n)
	}

	slog.Info("manual chain assigned",
		"meeting_id", meetingID, "chain_id", chainID, "user_id", userID)
	return nil
}

// RemoveFromChain detaches a meeting from any chain at the user's
// request. Writes provenance (method='manual_removed', locked_at=now)
// so the auto matcher (Phase 4) won't re-attach it. Cascades NULL to
// decision_items.chain_id so chain rollups stop including this
// meeting's items.
func (s *ChainAssignmentService) RemoveFromChain(
	ctx context.Context,
	userID, meetingID uuid.UUID,
) error {
	if userID == uuid.Nil || meetingID == uuid.Nil {
		return fmt.Errorf("RemoveFromChain: ids required")
	}

	now := time.Now()
	method := models.ChainAssignmentMethodManualRemoved
	in := repository.SetChainAssignmentInput{
		ChainID:  nil, // explicit NULL — chain detached
		Method:   &method,
		LockedAt: &now,
	}
	updated, err := s.meetingRepo.SetChainAssignment(ctx, userID, meetingID, in)
	if err != nil {
		return fmt.Errorf("RemoveFromChain: write assignment: %w", err)
	}
	if !updated {
		return ErrMeetingNotOwned
	}

	if err := s.itemRepo.UpdateChainIDForMeetingNull(ctx, meetingID); err != nil {
		slog.Error("chain_id NULL cascade failed",
			"meeting_id", meetingID, "error", err)
	}
	if n, err := s.decisionsRepo.ExpirePendingForMeeting(ctx, userID, meetingID); err != nil {
		slog.Warn("expire stale chain suggestions after manual remove failed",
			"meeting_id", meetingID, "error", err)
	} else if n > 0 {
		slog.Info("expired stale chain suggestions after manual remove",
			"meeting_id", meetingID, "expired", n)
	}

	slog.Info("meeting removed from chain (manual)",
		"meeting_id", meetingID, "user_id", userID)
	return nil
}

// RetryAutoDetect clears the manual lock on a meeting so the auto
// matcher (Phase 4) can re-evaluate it on next ingest. Keeps the
// current chain_id in place — the meeting's existing chain assignment
// stays visible to the user until the matcher decides differently.
func (s *ChainAssignmentService) RetryAutoDetect(
	ctx context.Context,
	userID, meetingID uuid.UUID,
) error {
	if userID == uuid.Nil || meetingID == uuid.Nil {
		return fmt.Errorf("RetryAutoDetect: ids required")
	}
	updated, err := s.meetingRepo.ClearChainAssignmentLock(ctx, userID, meetingID)
	if err != nil {
		return fmt.Errorf("RetryAutoDetect: clear lock: %w", err)
	}
	if !updated {
		return ErrMeetingNotOwned
	}
	slog.Info("meeting chain assignment lock cleared",
		"meeting_id", meetingID, "user_id", userID)
	return nil
}

// AcceptSuggestion accepts a pending LLM suggestion: marks the
// chain_decisions_v2 row as 'accepted' and writes the chain assignment
// to the meeting with method='llm_suggestion'. The suggestion must
// have a candidate_chain_id — orphan candidates can't auto-promote
// in Phase 3 (use manual move instead).
//
// Idempotency: if the row is already accepted/declined/suppressed, we
// return ErrSuggestionNotPending so a double-click doesn't re-apply
// the chain assignment (which could otherwise overwrite a manual
// move the user did in between).
func (s *ChainAssignmentService) AcceptSuggestion(
	ctx context.Context,
	userID, decisionID uuid.UUID,
) error {
	row, err := s.fetchPending(ctx, userID, decisionID)
	if err != nil {
		return err
	}
	if row.CandidateChainID == nil {
		return ErrSuggestionMissingChain
	}

	method := models.ChainAssignmentMethodLLMSuggestion
	row, err = s.decisionsRepo.AcceptPendingSuggestion(ctx, decisionID, userID, method)
	if errors.Is(err, repository.ErrChainDecisionMissingCandidateChain) {
		return ErrSuggestionMissingChain
	}
	if errors.Is(err, repository.ErrChainDecisionNotFound) {
		return ErrSuggestionNotPending
	}
	if err != nil {
		return fmt.Errorf("AcceptSuggestion: accept transaction: %w", err)
	}

	if err := s.itemRepo.UpdateChainIDForMeeting(ctx, row.MeetingID, *row.CandidateChainID); err != nil {
		slog.Error("chain_id cascade after suggestion accept failed",
			"meeting_id", row.MeetingID, "chain_id", *row.CandidateChainID, "error", err)
	}

	slog.Info("chain suggestion accepted",
		"decision_id", decisionID, "meeting_id", row.MeetingID,
		"chain_id", *row.CandidateChainID, "user_id", userID)
	return nil
}

// DeclineSuggestion marks a pending suggestion as 'declined' and runs
// the suppression check: if the user has now declined this same
// suppression_key 3+ times in the last 30 days, every other pending
// row sharing that key is bulk-marked 'suppressed' so the inbox stops
// nagging.
func (s *ChainAssignmentService) DeclineSuggestion(
	ctx context.Context,
	userID, decisionID uuid.UUID,
) error {
	row, err := s.fetchPending(ctx, userID, decisionID)
	if err != nil {
		return err
	}

	if err := s.decisionsRepo.MarkStatus(ctx, decisionID, userID, "declined"); err != nil {
		return fmt.Errorf("DeclineSuggestion: mark declined: %w", err)
	}

	// Suppression counter check. The just-declined row is now
	// "3 ardışık decline" — if the user's most-recent N decisions on
	// this key are all declined, bulk-suppress the rest. An accept
	// in between resets the streak (that's the difference from the
	// earlier "any 3 declines in 30d" form, which was more aggressive
	// than the roadmap intent).
	if row.SuppressionKey != nil && *row.SuppressionKey != "" {
		key := *row.SuppressionKey
		since := time.Now().Add(-suppressionWindow)
		streakHit, err := s.decisionsRepo.LastNDecisionsAllDeclined(ctx, userID, key, suppressionDeclineLimit, since)
		if err != nil {
			slog.Warn("suppression streak check failed", "key", key, "error", err)
		} else if streakHit {
			n, err := s.decisionsRepo.MarkSuppressedByKey(ctx, userID, key)
			if err != nil {
				slog.Warn("bulk suppress failed", "key", key, "error", err)
			} else if n > 0 {
				slog.Info("chain suggestion key suppressed",
					"key", key, "user_id", userID, "suppressed_pending", n,
					"streak_length", suppressionDeclineLimit)
			}
		}
	}

	slog.Info("chain suggestion declined",
		"decision_id", decisionID, "user_id", userID)
	return nil
}

// PendingSuggestion is the projection ListPendingSuggestions returns:
// the raw decision row enriched with the kind of identifiers a UI
// needs to render the inbox without a per-row N+1.
//
// Title fields are best-effort — if a JOIN comes up empty (the
// candidate meeting was deleted between log and fetch, say), the
// CandidateLabel falls back to the candidate_key string. Phase 3 UI
// surfaces the row regardless so the user can still decline.
type PendingSuggestion struct {
	DecisionID         uuid.UUID
	MeetingID          uuid.UUID
	MeetingTitle       string
	MeetingStartTime   time.Time
	CandidateChainID   *uuid.UUID
	MatchedMeetingID   *uuid.UUID
	CandidateLabel     string // chain display_name or matched meeting title
	ParticipantOverlap *float64
	TitleSimilarity    *float64
	WeightedScore      *float64
	DecisionCosine     *float64
	SuppressionKey     *string
	CreatedAt          time.Time
}

// ListPendingSuggestions returns the user's pending suggestions for
// the inbox UI, most recent first. Bounded by pendingSuggestionPageMax
// to keep the response cheap; Phase 3 UI doesn't need pagination.
func (s *ChainAssignmentService) ListPendingSuggestions(
	ctx context.Context,
	userID uuid.UUID,
	limit int,
) ([]PendingSuggestion, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("ListPendingSuggestions: user_id required")
	}
	if limit <= 0 || limit > pendingSuggestionPageMax {
		limit = pendingSuggestionPageMax
	}

	rows, err := s.decisionsRepo.ListPendingForUser(ctx, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListPendingSuggestions: query: %w", err)
	}

	out := make([]PendingSuggestion, 0, len(rows))
	for _, r := range rows {
		ps := PendingSuggestion{
			DecisionID:         r.ID,
			MeetingID:          r.MeetingID,
			CandidateChainID:   r.CandidateChainID,
			MatchedMeetingID:   r.MatchedMeetingID,
			ParticipantOverlap: r.ParticipantOverlap,
			TitleSimilarity:    r.TitleSimilarity,
			WeightedScore:      r.WeightedScore,
			DecisionCosine:     r.DecisionCosine,
			SuppressionKey:     r.SuppressionKey,
			CreatedAt:          r.CreatedAt,
		}
		// Best-effort enrichment. A failed lookup degrades to a
		// readable fallback rather than dropping the row.
		if m, err := s.meetingRepo.GetMeetingByIDSystem(ctx, r.MeetingID); err == nil {
			ps.MeetingTitle = m.Title
			ps.MeetingStartTime = m.StartTime
		}
		ps.CandidateLabel = s.candidateLabel(ctx, r)
		out = append(out, ps)
	}
	return out, nil
}

// candidateLabel resolves the human-friendly display string for the
// suggestion's candidate. Falls through to the raw key on lookup
// failure so the UI never shows blank.
func (s *ChainAssignmentService) candidateLabel(ctx context.Context, r repository.ChainDecisionV2Row) string {
	if r.CandidateChainID != nil {
		if c, err := s.chainRepo.FindByID(ctx, *r.CandidateChainID); err == nil && c != nil {
			if c.DisplayName != nil && *c.DisplayName != "" {
				return *c.DisplayName
			}
		}
	}
	if r.MatchedMeetingID != nil {
		if m, err := s.meetingRepo.GetMeetingByIDSystem(ctx, *r.MatchedMeetingID); err == nil {
			return m.Title
		}
	}
	return r.CandidateKey
}

// fetchPending loads a decision row scoped to userID and verifies
// it's still in 'pending' state. Centralised so accept and decline
// share the exact same pre-conditions and error mapping.
func (s *ChainAssignmentService) fetchPending(
	ctx context.Context,
	userID, decisionID uuid.UUID,
) (*repository.ChainDecisionV2Row, error) {
	row, err := s.decisionsRepo.GetByIDForUser(ctx, decisionID, userID)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(row.Status, "pending") {
		return nil, ErrSuggestionNotPending
	}
	return row, nil
}
