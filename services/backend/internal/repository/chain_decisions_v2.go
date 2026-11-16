package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ChainDecisionV2Row is the wire shape for chain_decisions_v2 inserts
// and reads. Nullable SQL columns are *T pointers so callers can
// distinguish "0" from "absent" without per-column NullFloat64 noise.
//
// FeatureScores is raw JSON so the matcher can attach version-specific
// per-feature breakdowns (Stage 1+2 keys vs Stage 3 cosine etc.)
// without a schema migration each iteration.
type ChainDecisionV2Row struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	MeetingID          uuid.UUID
	CandidateChainID   *uuid.UUID
	MatchedMeetingID   *uuid.UUID
	CandidateKey       string
	AlgorithmVersion   string
	Mode               string
	Status             string
	ParticipantOverlap *float64
	TitleSimilarity    *float64
	WeightedScore      *float64
	DecisionCosine     *float64
	FeatureScores      []byte
	LLMDecision        *string
	LLMConfidence      *float64
	SuppressionKey     *string
	CreatedAt          time.Time
	DecidedAt          *time.Time
}

// ChainDecisionsV2Repository is the persistence surface for
// chain_decisions_v2. Phase 2 added LogShadowDecision; Phase 3 added
// the suggestion-management methods (List/Get/MarkStatus/Count) so
// the user-facing inbox can render and act on pending suggestions.
type ChainDecisionsV2Repository interface {
	// LogShadowDecision INSERTs a row for a v3 shadow evaluation. The
	// (user_id, meeting_id, candidate_key, algorithm_version) UNIQUE
	// makes this a no-op on conflict — the matcher can re-run for the
	// same meeting (e.g., calendar resync) without duplicating shadow
	// data. Returns nil error on conflict (the row already exists from
	// a prior run); only real DB failures bubble up.
	LogShadowDecision(ctx context.Context, row ChainDecisionV2Row) error

	// ListPendingForUser returns a user's pending suggestion rows, most
	// recent first. Hits idx_chain_decisions_v2_pending (partial index
	// on status='pending'). limit is required and clamped at the
	// caller's discretion.
	ListPendingForUser(ctx context.Context, userID uuid.UUID, limit int) ([]ChainDecisionV2Row, error)

	// GetByIDForUser fetches one row, scoped to the owning user. The
	// user_id check goes in WHERE so cross-tenant access becomes a
	// "not found" rather than leaking row existence via 403.
	GetByIDForUser(ctx context.Context, id, userID uuid.UUID) (*ChainDecisionV2Row, error)

	// AcceptPendingSuggestion atomically resolves a pending suggestion
	// and writes the meeting assignment it represents. Keeping the
	// decision-row status flip and meetings.chain_id write in one
	// transaction prevents accept/decline races and stale pending rows
	// from overriding a later manual move/remove.
	AcceptPendingSuggestion(ctx context.Context, id, userID uuid.UUID, method string) (*ChainDecisionV2Row, error)

	// MarkStatus transitions an existing row to a new status (accept /
	// decline / suppress / expire / apply). decided_at is set to NOW
	// for terminal transitions. Returns ErrChainDecisionNotFound when
	// the row doesn't exist or doesn't belong to userID.
	MarkStatus(ctx context.Context, id, userID uuid.UUID, status string) error

	// ExpirePendingForMeeting marks all currently-pending suggestions
	// for a meeting as expired. Manual move/remove paths call this so
	// the inbox no longer offers actions that would contradict the
	// user's explicit correction.
	ExpirePendingForMeeting(ctx context.Context, userID, meetingID uuid.UUID) (int, error)

	// CountDeclinedByKeySince counts how many declined rows the user
	// accumulated for a given suppression_key since `since`. Kept for
	// analytics queries; the suppression-trigger path now uses
	// LastNDecisionsAllDeclined to match the roadmap's "3 ardışık
	// decline" wording rather than "any 3 declines in 30 days".
	CountDeclinedByKeySince(ctx context.Context, userID uuid.UUID, suppressionKey string, since time.Time) (int, error)

	// LastNDecisionsAllDeclined reports whether the user's most-recent
	// `n` decided (accepted|declined) rows for `suppressionKey` are
	// ALL declined. Returns (false, nil) when fewer than `n` decided
	// rows exist — not enough history to suppress.
	//
	// Implements the roadmap's "3 ardışık decline" semantics: if the
	// user accepted in between, the streak resets and the suggestion
	// is allowed to surface again. The earlier "count declines in 30d"
	// form was more aggressive than spec — accepts in the window
	// didn't reset the counter.
	LastNDecisionsAllDeclined(ctx context.Context, userID uuid.UUID, suppressionKey string, n int, since time.Time) (bool, error)

	// MarkSuppressedByKey transitions every PENDING row for the given
	// (user, suppression_key) pair to status='suppressed'. Used when
	// the decline-counter trip wire fires so the suggestion inbox
	// stops showing the pattern immediately, not on next render.
	MarkSuppressedByKey(ctx context.Context, userID uuid.UUID, suppressionKey string) (int, error)

	// CountPendingForUserSince counts the user's currently-pending
	// suggestion rows created on or after `since`. Powers the Phase 4
	// "max 5 pending suggestions per day" rate limit — when this hits
	// the daily ceiling the matcher demotes new mid-band candidates
	// to silent-shadow rather than queue another notification.
	CountPendingForUserSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error)
}

// ErrChainDecisionNotFound is returned when a chain_decisions_v2 row
// doesn't exist or doesn't belong to the requesting user. Handlers
// translate this to 404.
var ErrChainDecisionNotFound = fmt.Errorf("chain decision not found")

// ErrChainDecisionMissingCandidateChain is returned when an accept is
// attempted for an orphan suggestion. Orphan promotion is intentionally
// a separate UX path; accepting a suggestion can only attach to an
// existing chain.
var ErrChainDecisionMissingCandidateChain = fmt.Errorf("chain decision has no candidate chain")

// validChainDecisionStatuses matches the CHECK constraint in the SQL
// schema (migration 019). MarkStatus rejects anything else early so a
// typo doesn't bubble up as a DB constraint violation.
var validChainDecisionStatuses = map[string]bool{
	"shadow_only": true,
	"pending":     true,
	"accepted":    true,
	"declined":    true,
	"expired":     true,
	"suppressed":  true,
	"applied":     true,
}

type postgresChainDecisionsV2Repository struct{}

func NewChainDecisionsV2Repository() ChainDecisionsV2Repository {
	return &postgresChainDecisionsV2Repository{}
}

func (r *postgresChainDecisionsV2Repository) LogShadowDecision(ctx context.Context, row ChainDecisionV2Row) error {
	if row.UserID == uuid.Nil {
		return fmt.Errorf("LogShadowDecision: user_id is required")
	}
	if row.MeetingID == uuid.Nil {
		return fmt.Errorf("LogShadowDecision: meeting_id is required")
	}
	if row.CandidateKey == "" {
		return fmt.Errorf("LogShadowDecision: candidate_key is required")
	}
	if row.AlgorithmVersion == "" {
		row.AlgorithmVersion = "chain-v3"
	}
	if row.Mode == "" {
		row.Mode = "shadow"
	}
	if row.Status == "" {
		row.Status = "shadow_only"
	}
	if len(row.FeatureScores) == 0 {
		row.FeatureScores = []byte("{}")
	}

	// ON CONFLICT semantics:
	//   - If the existing row is `mode='shadow' status='shadow_only'` and
	//     the new row is `suggestion` or `applied`, PROMOTE: update the
	//     row in place to the higher-priority state. This is critical at
	//     cutover (Phase 2/5 → Phase 4/6): shadow rows previously written
	//     for the same (user, meeting, candidate) would otherwise block
	//     the suggestion/applied write — leaving the suggestion inbox
	//     empty for any meeting that was shadow-evaluated first.
	//   - If the existing row is still pending and the new row is
	//     applied, PROMOTE: the matcher has since found a deterministic
	//     auto-attach and the old inbox row must not stay actionable.
	//   - All other conflicts (re-runs of the same algo, suppressed
	//     shadow trying to overwrite anything) → DO NOTHING. State is
	//     owned by the higher-priority writer; we never demote.
	//   - decided_at is reset to NULL for pending suggestions and set
	//     for applied promotions.
	//   - created_at is bumped to NOW so the inbox sorts the promoted
	//     row as a fresh suggestion rather than a stale shadow timestamp.
	const query = `
		INSERT INTO chain_decisions_v2 (
			user_id, meeting_id, candidate_chain_id, matched_meeting_id,
			candidate_key, algorithm_version, mode, status,
			participant_overlap, title_similarity, weighted_score, decision_cosine,
			feature_scores, llm_decision, llm_confidence, suppression_key
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15, $16
		)
		ON CONFLICT (user_id, meeting_id, candidate_key, algorithm_version)
		DO UPDATE SET
		    mode = EXCLUDED.mode,
		    status = EXCLUDED.status,
		    candidate_chain_id = EXCLUDED.candidate_chain_id,
		    matched_meeting_id = EXCLUDED.matched_meeting_id,
		    participant_overlap = EXCLUDED.participant_overlap,
		    title_similarity = EXCLUDED.title_similarity,
		    weighted_score = EXCLUDED.weighted_score,
		    decision_cosine = EXCLUDED.decision_cosine,
		    feature_scores = EXCLUDED.feature_scores,
		    llm_decision = EXCLUDED.llm_decision,
		    llm_confidence = EXCLUDED.llm_confidence,
		    suppression_key = EXCLUDED.suppression_key,
		    created_at = now(),
		    decided_at = CASE
		        WHEN EXCLUDED.status IN ('accepted','declined','expired','suppressed','applied')
		            THEN now()
		        ELSE NULL
		    END
		WHERE (
		    chain_decisions_v2.mode = 'shadow'
		    AND chain_decisions_v2.status = 'shadow_only'
		    AND EXCLUDED.mode IN ('suggestion', 'applied')
		) OR (
		    chain_decisions_v2.status = 'pending'
		    AND EXCLUDED.mode = 'applied'
		    AND EXCLUDED.status = 'applied'
		)
	`

	_, err := DB.Exec(ctx, query,
		row.UserID, row.MeetingID, row.CandidateChainID, row.MatchedMeetingID,
		row.CandidateKey, row.AlgorithmVersion, row.Mode, row.Status,
		row.ParticipantOverlap, row.TitleSimilarity, row.WeightedScore, row.DecisionCosine,
		row.FeatureScores, row.LLMDecision, row.LLMConfidence, row.SuppressionKey,
	)
	if err != nil {
		return fmt.Errorf("insert chain_decisions_v2: %w", err)
	}
	return nil
}

// scanRow centralises the SELECT column order so the three read paths
// (List/Get) stay in lockstep. Adding a column means updating the
// SELECT and one Scan rather than three.
func scanChainDecisionV2Row(scanner interface {
	Scan(dest ...any) error
}) (ChainDecisionV2Row, error) {
	var r ChainDecisionV2Row
	err := scanner.Scan(
		&r.ID, &r.UserID, &r.MeetingID, &r.CandidateChainID, &r.MatchedMeetingID,
		&r.CandidateKey, &r.AlgorithmVersion, &r.Mode, &r.Status,
		&r.ParticipantOverlap, &r.TitleSimilarity, &r.WeightedScore, &r.DecisionCosine,
		&r.FeatureScores, &r.LLMDecision, &r.LLMConfidence, &r.SuppressionKey,
		&r.CreatedAt, &r.DecidedAt,
	)
	return r, err
}

const chainDecisionV2Columns = `
	id, user_id, meeting_id, candidate_chain_id, matched_meeting_id,
	candidate_key, algorithm_version, mode, status,
	participant_overlap, title_similarity, weighted_score, decision_cosine,
	feature_scores, llm_decision, llm_confidence, suppression_key,
	created_at, decided_at
`

func (r *postgresChainDecisionsV2Repository) ListPendingForUser(ctx context.Context, userID uuid.UUID, limit int) ([]ChainDecisionV2Row, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("ListPendingForUser: user_id is required")
	}
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT ` + chainDecisionV2Columns + `
		FROM chain_decisions_v2
		WHERE user_id = $1 AND status = 'pending'
		ORDER BY created_at DESC
		LIMIT $2
	`
	rows, err := DB.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending chain decisions: %w", err)
	}
	defer rows.Close()

	var out []ChainDecisionV2Row
	for rows.Next() {
		row, err := scanChainDecisionV2Row(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending chain decision: %w", err)
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *postgresChainDecisionsV2Repository) GetByIDForUser(ctx context.Context, id, userID uuid.UUID) (*ChainDecisionV2Row, error) {
	if id == uuid.Nil || userID == uuid.Nil {
		return nil, ErrChainDecisionNotFound
	}
	query := `
		SELECT ` + chainDecisionV2Columns + `
		FROM chain_decisions_v2
		WHERE id = $1 AND user_id = $2
	`
	row, err := scanChainDecisionV2Row(DB.QueryRow(ctx, query, id, userID))
	if err != nil {
		// pgx returns "no rows in result set" — collapse to our sentinel
		// so handlers don't have to import pgx just to check.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChainDecisionNotFound
		}
		return nil, fmt.Errorf("get chain decision: %w", err)
	}
	return &row, nil
}

func (r *postgresChainDecisionsV2Repository) AcceptPendingSuggestion(ctx context.Context, id, userID uuid.UUID, method string) (*ChainDecisionV2Row, error) {
	if id == uuid.Nil || userID == uuid.Nil {
		return nil, ErrChainDecisionNotFound
	}
	if method == "" {
		return nil, fmt.Errorf("AcceptPendingSuggestion: method is required")
	}

	tx, err := DB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("accept chain suggestion: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	row, err := scanChainDecisionV2Row(tx.QueryRow(ctx, `
		SELECT `+chainDecisionV2Columns+`
		FROM chain_decisions_v2
		WHERE id = $1 AND user_id = $2 AND status = 'pending'
		FOR UPDATE
	`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrChainDecisionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("accept chain suggestion: lock decision: %w", err)
	}
	if row.CandidateChainID == nil {
		return nil, ErrChainDecisionMissingCandidateChain
	}

	score := row.WeightedScore
	if score == nil {
		score = row.DecisionCosine
	}
	tag, err := tx.Exec(ctx, `
		UPDATE meetings
		SET chain_id = $3,
		    chain_assignment_method = $4,
		    chain_assignment_score = $5,
		    chain_assignment_reason = $6,
		    chain_assignment_locked_at = NULL,
		    updated_at = now()
		WHERE id = $1
		  AND user_id = $2
		  AND (
		      chain_assignment_method IS NULL
		      OR chain_assignment_method NOT IN ('manual_user', 'manual_removed')
		  )
	`, row.MeetingID, userID, row.CandidateChainID, method, score, row.FeatureScores)
	if err != nil {
		return nil, fmt.Errorf("accept chain suggestion: update meeting: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrChainDecisionNotFound
	}

	tag, err = tx.Exec(ctx, `
		UPDATE chain_decisions_v2
		SET status = 'accepted',
		    decided_at = COALESCE(decided_at, now())
		WHERE id = $1 AND user_id = $2 AND status = 'pending'
	`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("accept chain suggestion: mark accepted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrChainDecisionNotFound
	}
	row.Status = "accepted"
	now := time.Now()
	row.DecidedAt = &now

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("accept chain suggestion: commit: %w", err)
	}
	return &row, nil
}

func (r *postgresChainDecisionsV2Repository) MarkStatus(ctx context.Context, id, userID uuid.UUID, status string) error {
	if id == uuid.Nil || userID == uuid.Nil {
		return ErrChainDecisionNotFound
	}
	if !validChainDecisionStatuses[status] {
		return fmt.Errorf("MarkStatus: invalid status %q", status)
	}

	// CAS guard: terminal transitions (accept / decline / expire /
	// suppress / apply) are only allowed FROM pending. Without this,
	// two concurrent accept/decline calls could both succeed —
	// leaving the meeting attached to a chain (set by accept's
	// SetChainAssignment) while the row records "declined" (set by
	// decline's MarkStatus). The guard makes the second writer get
	// ErrChainDecisionNotFound; the caller can render "already
	// resolved".
	//
	// decided_at is set for terminal transitions only; "pending" /
	// "shadow_only" leave it nil so the timestamp reflects the actual
	// decision moment, not row state churn.
	const query = `
		UPDATE chain_decisions_v2
		SET status = $3,
		    decided_at = CASE
		        WHEN $3 IN ('accepted','declined','expired','suppressed','applied')
		            THEN COALESCE(decided_at, now())
		        ELSE decided_at
		    END
		WHERE id = $1 AND user_id = $2
		  AND (
		      $3 NOT IN ('accepted','declined','expired','suppressed','applied')
		      OR status = 'pending'
		  )
	`
	tag, err := DB.Exec(ctx, query, id, userID, status)
	if err != nil {
		return fmt.Errorf("update chain_decisions_v2 status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrChainDecisionNotFound
	}
	return nil
}

func (r *postgresChainDecisionsV2Repository) ExpirePendingForMeeting(ctx context.Context, userID, meetingID uuid.UUID) (int, error) {
	if userID == uuid.Nil || meetingID == uuid.Nil {
		return 0, nil
	}
	const query = `
		UPDATE chain_decisions_v2
		SET status = 'expired',
		    decided_at = COALESCE(decided_at, now())
		WHERE user_id = $1
		  AND meeting_id = $2
		  AND status = 'pending'
	`
	tag, err := DB.Exec(ctx, query, userID, meetingID)
	if err != nil {
		return 0, fmt.Errorf("expire pending chain decisions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *postgresChainDecisionsV2Repository) CountDeclinedByKeySince(ctx context.Context, userID uuid.UUID, suppressionKey string, since time.Time) (int, error) {
	if userID == uuid.Nil || suppressionKey == "" {
		return 0, nil
	}
	const query = `
		SELECT COUNT(*)
		FROM chain_decisions_v2
		WHERE user_id = $1
		  AND suppression_key = $2
		  AND status = 'declined'
		  AND COALESCE(decided_at, created_at) >= $3
	`
	var count int
	if err := DB.QueryRow(ctx, query, userID, suppressionKey, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count declined chain decisions: %w", err)
	}
	return count, nil
}

func (r *postgresChainDecisionsV2Repository) LastNDecisionsAllDeclined(ctx context.Context, userID uuid.UUID, suppressionKey string, n int, since time.Time) (bool, error) {
	if userID == uuid.Nil || suppressionKey == "" || n <= 0 {
		return false, nil
	}
	// Pull the n most-recent DECIDED rows (accepted | declined). If
	// we don't have n yet, the streak doesn't qualify. Otherwise the
	// streak is "all declined" only when none of the n is accepted.
	const query = `
		SELECT status
		FROM chain_decisions_v2
		WHERE user_id = $1
		  AND suppression_key = $2
		  AND status IN ('accepted', 'declined')
		  AND ($4::timestamptz IS NULL OR COALESCE(decided_at, created_at) >= $4)
		ORDER BY COALESCE(decided_at, created_at) DESC
		LIMIT $3
	`
	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := DB.Query(ctx, query, userID, suppressionKey, n, sinceArg)
	if err != nil {
		return false, fmt.Errorf("query recent decided chain decisions: %w", err)
	}
	defer rows.Close()

	total := 0
	declined := 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return false, fmt.Errorf("scan recent decided row: %w", err)
		}
		total++
		if status == "declined" {
			declined++
		}
	}
	return total >= n && declined == n, nil
}

func (r *postgresChainDecisionsV2Repository) CountPendingForUserSince(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	if userID == uuid.Nil {
		return 0, nil
	}
	const query = `
		SELECT COUNT(*)
		FROM chain_decisions_v2
		WHERE user_id = $1
		  AND status = 'pending'
		  AND created_at >= $2
	`
	var count int
	if err := DB.QueryRow(ctx, query, userID, since).Scan(&count); err != nil {
		return 0, fmt.Errorf("count pending chain decisions: %w", err)
	}
	return count, nil
}

func (r *postgresChainDecisionsV2Repository) MarkSuppressedByKey(ctx context.Context, userID uuid.UUID, suppressionKey string) (int, error) {
	if userID == uuid.Nil || suppressionKey == "" {
		return 0, nil
	}
	const query = `
		UPDATE chain_decisions_v2
		SET status = 'suppressed',
		    decided_at = COALESCE(decided_at, now())
		WHERE user_id = $1
		  AND suppression_key = $2
		  AND status = 'pending'
	`
	tag, err := DB.Exec(ctx, query, userID, suppressionKey)
	if err != nil {
		return 0, fmt.Errorf("mark suppressed: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
