package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ChainMatcherV3Stage3 implements the v3 Stage 3 (decision-based)
// chain matcher from docs/CHAIN_DETECTION_ROADMAP.md.
//
// Phase 6 cutover semantics — for any orphan meeting that just had
// decisions extracted, the matcher embeds those decisions, picks the
// highest-cosine eligible chain, and:
//
//	cosine ≥ 0.80 → auto-attach (method='decision_match')
//	0.70 ≤ cosine < 0.80 → pending suggestion (suppression key prefix `decision:`)
//	cosine < 0.70 → orphan, no action
//
// Cold-start guards (roadmap §Stage 3):
//   - meeting must have ≥2 decision items
//   - chain must have ≥3 meetings with at least one decision_item each
//   - chain's most recent meeting must be within 90 days
//
// Trigger guards:
//   - meeting.ChainID is nil (orphan)
//   - meeting.ChainAssignmentMethod ≠ 'manual_removed'
//
// Lazy centroid invalidation: chain centroid is rebuilt from the
// chain's last 10 decision texts whenever the cached centroid is
// older than the chain's most recent decision_item.
//
// Suppression + rate-limit reuse the Phase 4 ChainDecisionsV2
// machinery (3 declines / 30 days, 5 pending / 24 h) but with the
// `decision:` suppression-key prefix so Stage 3 declines stay
// disjoint from Stage 1+2 metadata declines.
type ChainMatcherV3Stage3 struct {
	chainRepo     repository.ChainRepository
	meetingRepo   repository.MeetingRepository
	centroidsRepo repository.ChainCentroidsRepository
	decisionsRepo repository.ChainDecisionsV2Repository
	itemRepo      repository.DecisionRepository
	embedder      EmbeddingClient
}

// NewChainMatcherV3Stage3 wires the Stage 3 matcher with concrete
// production deps. Tests substitute fakes via the exported field
// assignment in chain_matcher_v3_stage3_test.go.
func NewChainMatcherV3Stage3() *ChainMatcherV3Stage3 {
	return &ChainMatcherV3Stage3{
		chainRepo:     repository.NewChainRepository(),
		meetingRepo:   repository.NewMeetingRepository(),
		centroidsRepo: repository.NewChainCentroidsRepository(),
		decisionsRepo: repository.NewChainDecisionsV2Repository(),
		itemRepo:      repository.NewDecisionRepository(),
		embedder:      NewEmbeddingClient(),
	}
}

// ChainMatcherV3Stage3AlgorithmVersion is the value persisted in
// chain_decisions_v2.algorithm_version for Stage 3 rows. Distinct
// from `chain-v3` (Stage 1+2) so the (user, meeting, candidate, ver)
// UNIQUE doesn't conflate the two stages and so PromQL/SQL queries
// can split shadow data by stage.
const ChainMatcherV3Stage3AlgorithmVersion = "chain-v3-stage3"

const (
	// Stage 3 cold-start thresholds (roadmap §Stage 3 / Eligible chain guards).
	stage3MinDecisionsPerMeeting        = 2
	stage3MinDecisionBoundChainMeetings = 3
	stage3RecencyWindow                 = 90 * 24 * time.Hour

	// Sliding-window size for the chain centroid: most recent N decisions.
	stage3CentroidWindowSize = 10

	// Cosine bands. Phase 5 logs every in-band cosine; Phase 6 will
	// auto-attach at >=auto and surface a suggestion in the
	// [suggestion, auto) band.
	stage3CosineAutoMin       = 0.80
	stage3CosineSuggestionMin = 0.70
)

// EvaluateAndApply is the post-meeting hook called once decision
// extraction completes for `meeting`. Runs guards, picks the
// highest-cosine eligible chain, and either auto-attaches
// (cosine ≥0.80) or queues a suggestion (0.70-0.80, subject to
// suppression + rate-limit). Returns nil for both "skipped (guard
// failed)" and "applied/queued" — callers can't distinguish, by
// design. Errors are only returned for genuine failures (DB /
// embedder).
func (m *ChainMatcherV3Stage3) EvaluateAndApply(ctx context.Context, meeting *models.Meeting) error {
	if meeting == nil {
		return fmt.Errorf("EvaluateAndApply: meeting is nil")
	}
	if meeting.UserID == uuid.Nil {
		return fmt.Errorf("EvaluateAndApply: meeting.UserID is nil")
	}

	// Trigger guards.
	if meeting.ChainID != nil {
		// Already chained — Stage 3 is for orphan recovery only.
		return nil
	}
	if meeting.ChainAssignmentMethod != nil &&
		*meeting.ChainAssignmentMethod == models.ChainAssignmentMethodManualRemoved {
		// User explicitly removed this meeting from chains; honour
		// the lock here too, not just in Stage 1+2.
		return nil
	}

	// Pull decision texts and apply the per-meeting cold-start guard.
	items, err := m.itemRepo.GetDecisionItemsByMeeting(ctx, meeting.ID)
	if err != nil {
		return fmt.Errorf("fetch meeting decisions: %w", err)
	}
	decisionTexts := decisionTextsForCentroid(items)
	if len(decisionTexts) < stage3MinDecisionsPerMeeting {
		slog.Info("stage3 skip: meeting below decision floor",
			"meeting_id", meeting.ID, "count", len(decisionTexts))
		return nil
	}

	// Eligible chains for this user (chain-side cold-start guards).
	eligible, err := m.centroidsRepo.GetEligibleChainsForUser(
		ctx, meeting.UserID, meeting.ID,
		stage3MinDecisionBoundChainMeetings, stage3RecencyWindow,
	)
	if err != nil {
		return fmt.Errorf("eligible chains: %w", err)
	}
	if len(eligible) == 0 {
		slog.Info("stage3 skip: no eligible chains", "meeting_id", meeting.ID)
		return nil
	}

	// Compute the meeting's decision centroid. Attribute the embedding
	// cost to the meeting owner so issue #39 quota counts it against
	// the right tenant.
	meetingEmbeddings, err := m.embedder.EmbedTexts(ctx, meeting.UserID, &meeting.ID, decisionTexts)
	if err != nil {
		return fmt.Errorf("embed meeting decisions: %w", err)
	}
	meetingCentroid, usableMeetingEmbeddings, err := meanEmbeddingWithCount(meetingEmbeddings)
	if err != nil {
		return fmt.Errorf("meeting centroid: %w", err)
	}
	if usableMeetingEmbeddings < stage3MinDecisionsPerMeeting {
		slog.Info("stage3 skip: meeting below usable embedding floor",
			"meeting_id", meeting.ID,
			"required", stage3MinDecisionsPerMeeting,
			"usable_embeddings", usableMeetingEmbeddings)
		return nil
	}

	// Pick the highest-cosine in-band chain. A single winner per
	// meeting matches the Phase 4 metadata behaviour and avoids
	// queueing multiple suggestions for the same orphan.
	var (
		winnerChain  uuid.UUID
		winnerCosine float64
		winnerFound  bool
	)
	for _, ec := range eligible {
		// Chains are tenant-scoped (migration 021), so the chain
		// centroid's embedding cost — when rebuilt — belongs to the
		// evaluating meeting's owner. Pass meeting.UserID for issue
		// #39 attribution; nil meeting_id since the rebuild isn't
		// tied to a single meeting.
		chainCentroid, err := m.getOrRebuildChainCentroid(ctx, meeting.UserID, ec.ChainID)
		if err != nil {
			slog.Warn("stage3 chain centroid unavailable",
				"chain_id", ec.ChainID, "error", err)
			continue
		}
		cos, err := cosineSimilarity(meetingCentroid, chainCentroid)
		if err != nil {
			slog.Warn("stage3 cosine failed",
				"chain_id", ec.ChainID, "error", err)
			continue
		}
		if cos < stage3CosineSuggestionMin {
			continue
		}
		if !winnerFound || cos > winnerCosine {
			winnerChain = ec.ChainID
			winnerCosine = cos
			winnerFound = true
		}
	}
	if !winnerFound {
		// No eligible chain crossed the suggestion floor.
		return nil
	}

	if winnerCosine >= stage3CosineAutoMin {
		return m.applyDecisionMatch(ctx, meeting, winnerChain, winnerCosine)
	}
	return m.applySuggestion(ctx, meeting, winnerChain, winnerCosine)
}

// applyDecisionMatch performs the auto-attach: writes chain_id +
// method='decision_match' on the meeting, cascades chain id to the
// meeting's decision_items so chain rollups stay consistent, and
// logs an applied row to chain_decisions_v2.
func (m *ChainMatcherV3Stage3) applyDecisionMatch(ctx context.Context, meeting *models.Meeting, chainID uuid.UUID, cosine float64) error {
	method := models.ChainAssignmentMethodDecisionMatch
	reason := marshalStage3Reason(cosine, "")

	updated, err := m.meetingRepo.SetChainAssignment(ctx, meeting.UserID, meeting.ID, repository.SetChainAssignmentInput{
		ChainID: &chainID,
		Method:  &method,
		Score:   &cosine,
		Reason:  reason,
		// LockedAt deliberately nil: decision_match is an automatic
		// assignment — the user can still move/remove it, and a
		// future ingest can still re-evaluate via Stage 1+2.
	})
	if err != nil {
		return fmt.Errorf("apply decision_match: %w", err)
	}
	if !updated {
		// Meeting was deleted between the trigger and now — log and
		// drop. No audit row because there's nothing to audit.
		slog.Warn("stage3 apply: meeting not found",
			"meeting_id", meeting.ID, "chain_id", chainID)
		return nil
	}
	// Reflect on the in-memory meeting so callers (and the audit
	// row) see the post-apply state.
	meeting.ChainID = &chainID

	// Cascade chain_id to the meeting's existing decision_items so
	// chain rollups + brief context include them. Logged but
	// non-fatal: meetings.chain_id is the source of truth.
	if err := m.itemRepo.UpdateChainIDForMeeting(ctx, meeting.ID, chainID); err != nil {
		slog.Error("stage3 decision_items cascade failed",
			"meeting_id", meeting.ID, "chain_id", chainID, "error", err)
	}

	if err := m.writeAppliedRow(ctx, meeting, chainID, cosine, reason); err != nil {
		slog.Warn("stage3 applied audit row failed",
			"meeting_id", meeting.ID, "chain_id", chainID, "error", err)
	}
	return nil
}

// applySuggestion runs the suppression + rate-limit gates and then
// writes either a pending suggestion row or a shadow analytics row
// (when the gates trip). Mirrors the Stage 1+2 metadata logic so the
// suggestion inbox + decline-counter machinery work the same way.
func (m *ChainMatcherV3Stage3) applySuggestion(ctx context.Context, meeting *models.Meeting, chainID uuid.UUID, cosine float64) error {
	suppressionKey := decisionSuppressionKey(chainCandidateKey(chainID))

	// Suppression streak: last N decisions on this key all declined
	// (consecutive — an accept resets the streak).
	suppressionSince := time.Now().Add(-suppressionWindow)
	if streakHit, err := m.decisionsRepo.LastNDecisionsAllDeclined(ctx, meeting.UserID, suppressionKey, suppressionDeclineLimit, suppressionSince); err == nil && streakHit {
		return m.writeShadowSuggestionRow(ctx, meeting, chainID, cosine, models.ChainDecisionStatusSuppressed)
	}

	// Per-user pending cap across ALL stages — Stage 3 shares the
	// inbox quota with Stage 1+2.
	pendingSince := time.Now().Add(-suggestionDailyWindow)
	if pending, err := m.decisionsRepo.CountPendingForUserSince(ctx, meeting.UserID, pendingSince); err == nil && pending >= suggestionDailyLimit {
		return m.writeShadowSuggestionRow(ctx, meeting, chainID, cosine, models.ChainDecisionStatusShadowOnly)
	}

	row := m.buildStage3Row(meeting, chainID, cosine,
		models.ChainDecisionModeSuggestion, models.ChainDecisionStatusPending,
		marshalStage3Reason(cosine, ""))
	if err := m.decisionsRepo.LogShadowDecision(ctx, row); err != nil {
		return fmt.Errorf("log stage3 suggestion: %w", err)
	}
	return nil
}

// writeAppliedRow persists the audit row for an auto-attach.
// Mode/status='applied' so analytics queries can join
// meetings.chain_assignment_method='decision_match' rows back to the
// matcher decision that produced them.
func (m *ChainMatcherV3Stage3) writeAppliedRow(ctx context.Context, meeting *models.Meeting, chainID uuid.UUID, cosine float64, reason []byte) error {
	row := m.buildStage3Row(meeting, chainID, cosine,
		models.ChainDecisionModeApplied, models.ChainDecisionStatusApplied, reason)
	if err := m.decisionsRepo.LogShadowDecision(ctx, row); err != nil {
		return fmt.Errorf("write stage3 applied row: %w", err)
	}
	return nil
}

// writeShadowSuggestionRow records a "would-have-suggested but
// gated" attempt for analytics. status differentiates the gate
// (suppressed vs shadow_only=rate-limited) so dashboards can split.
func (m *ChainMatcherV3Stage3) writeShadowSuggestionRow(ctx context.Context, meeting *models.Meeting, chainID uuid.UUID, cosine float64, status string) error {
	row := m.buildStage3Row(meeting, chainID, cosine,
		models.ChainDecisionModeShadow, status,
		marshalStage3Reason(cosine, status))
	if err := m.decisionsRepo.LogShadowDecision(ctx, row); err != nil {
		return fmt.Errorf("write stage3 shadow row (%s): %w", status, err)
	}
	return nil
}

// buildStage3Row constructs a chain_decisions_v2 audit row for a
// Stage 3 evaluation. Mode/status/reason are caller-controlled so the
// same shape produces applied, real suggestion, and shadow rows.
func (m *ChainMatcherV3Stage3) buildStage3Row(meeting *models.Meeting, chainID uuid.UUID, cosine float64, mode, status string, reason []byte) repository.ChainDecisionV2Row {
	candidateKey := chainCandidateKey(chainID)
	return repository.ChainDecisionV2Row{
		UserID:           meeting.UserID,
		MeetingID:        meeting.ID,
		CandidateChainID: ptrUUID(chainID),
		CandidateKey:     candidateKey,
		AlgorithmVersion: ChainMatcherV3Stage3AlgorithmVersion,
		Mode:             mode,
		Status:           status,
		DecisionCosine:   ptrFloat(cosine),
		FeatureScores:    reason,
		SuppressionKey:   ptrString(decisionSuppressionKey(candidateKey)),
	}
}

// marshalStage3Reason builds the chain_decisions_v2.feature_scores
// payload for Stage 3 audit rows. Pass an empty gatedStatus for
// applied / real suggestion rows; pass the gate name (`suppressed`,
// `shadow_only`) for shadow rows so dashboards can split by gate.
//
// Marshal is infallible here (the payload is a fixed map of a float
// and short strings) so we don't surface an error — falling back to
// `{}` keeps callers free of error plumbing that can't fire.
func marshalStage3Reason(cosine float64, gatedStatus string) []byte {
	payload := map[string]any{
		"decision_cosine": cosine,
		"stage":           "decision_match",
	}
	if gatedStatus != "" {
		payload["gated_status"] = gatedStatus
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return out
}

// getOrRebuildChainCentroid implements the lazy invalidation contract:
// if the cached centroid is missing or older than the chain's most
// recent decision_item, rebuild from the chain's last N decision
// texts and upsert. Returns the up-to-date centroid.
//
// userID is forwarded to the embedder for issue #39 cost attribution
// when (and only when) the cache is stale and a rebuild fires. Cache
// hits cost nothing on the OpenAI side so no usage record is needed.
func (m *ChainMatcherV3Stage3) getOrRebuildChainCentroid(ctx context.Context, userID uuid.UUID, chainID uuid.UUID) ([]float64, error) {
	cached, err := m.centroidsRepo.GetCentroid(ctx, chainID)
	if err != nil && !errors.Is(err, repository.ErrChainCentroidNotFound) {
		return nil, fmt.Errorf("get cached centroid: %w", err)
	}

	mostRecent, err := m.centroidsRepo.MostRecentDecisionTimeForChain(ctx, chainID)
	if err != nil {
		return nil, fmt.Errorf("most-recent decision: %w", err)
	}

	if cached != nil && cached.ComputedAt.After(mostRecent) {
		// Cache is fresh — chain hasn't gained new decisions since
		// the last build.
		return cached.Centroid, nil
	}

	texts, err := m.centroidsRepo.GetRecentDecisionTextsForChain(ctx, chainID, stage3CentroidWindowSize)
	if err != nil {
		return nil, fmt.Errorf("fetch decision texts: %w", err)
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("chain %s has no decision texts", chainID)
	}
	// Chain centroid rebuild — not tied to a single meeting, so pass
	// nil meeting_id. userID is the evaluating meeting's owner (chains
	// are tenant-scoped per migration 021, so this is unambiguous).
	embeddings, err := m.embedder.EmbedTexts(ctx, userID, nil, texts)
	if err != nil {
		return nil, fmt.Errorf("embed chain decisions: %w", err)
	}
	centroid, err := meanEmbedding(embeddings)
	if err != nil {
		return nil, fmt.Errorf("compute chain centroid: %w", err)
	}
	if err := m.centroidsRepo.UpsertCentroid(ctx, chainID, centroid, len(texts)); err != nil {
		// Logged but non-fatal: matcher can still use the in-memory
		// centroid for THIS run; next run rebuilds again.
		slog.Warn("stage3 centroid upsert failed",
			"chain_id", chainID, "error", err)
	}
	return centroid, nil
}

// IsStage3AutoBand reports whether a cosine would auto-attach.
// Exported so callers share the same threshold semantics.
func IsStage3AutoBand(cos float64) bool { return cos >= stage3CosineAutoMin }

// IsStage3SuggestionBand reports whether a cosine would surface as a
// user-facing suggestion (between the suggestion floor and the auto
// floor).
func IsStage3SuggestionBand(cos float64) bool {
	return cos >= stage3CosineSuggestionMin && cos < stage3CosineAutoMin
}

// runStage3Matcher is the boundary called from the post-meeting
// pipeline. Errors are logged and absorbed so a Stage 3 hiccup
// cannot block the user-visible post-processing.
func runStage3Matcher(ctx context.Context, m *ChainMatcherV3Stage3, meeting *models.Meeting) {
	if m == nil || meeting == nil {
		return
	}
	if err := m.EvaluateAndApply(ctx, meeting); err != nil {
		slog.Warn("stage3 evaluate failed",
			"meeting_id", meeting.ID, "error", err)
	}
}

// ===== pure helpers =====

// decisionTextsForCentroid pulls the user-facing text from each
// decision_item, dropping empties so the centroid mean isn't biased
// by zero vectors. Order doesn't matter for the mean.
func decisionTextsForCentroid(items []models.DecisionItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.Text == "" {
			continue
		}
		out = append(out, it.Text)
	}
	return out
}

// meanEmbedding returns the element-wise mean of a list of equal-
// length vectors. Filters nil entries (per-text embedding failures
// returned by the embedder); returns an error if NO usable vector
// remains.
func meanEmbedding(vectors [][]float64) ([]float64, error) {
	mean, _, err := meanEmbeddingWithCount(vectors)
	return mean, err
}

func meanEmbeddingWithCount(vectors [][]float64) ([]float64, int, error) {
	dim := 0
	used := 0
	var sum []float64
	for _, v := range vectors {
		if v == nil {
			continue
		}
		if dim == 0 {
			dim = len(v)
			sum = make([]float64, dim)
		} else if len(v) != dim {
			return nil, 0, fmt.Errorf("embedding dim mismatch: %d vs %d", len(v), dim)
		}
		for i, f := range v {
			sum[i] += f
		}
		used++
	}
	if used == 0 {
		return nil, 0, fmt.Errorf("no usable embeddings to average")
	}
	for i := range sum {
		sum[i] /= float64(used)
	}
	return sum, used, nil
}

// cosineSimilarity returns the cosine between two equal-length
// vectors. Dimension mismatch is an error so we don't silently
// compare against partial vectors.
func cosineSimilarity(a, b []float64) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("dim mismatch: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("empty vectors")
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("zero-norm vector")
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), nil
}
