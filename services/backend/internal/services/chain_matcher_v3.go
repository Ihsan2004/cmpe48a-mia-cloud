package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ChainMatcherV3 is the v3 metadata chain matcher (Stage 1+2) from
// docs/CHAIN_DETECTION_ROADMAP.md. After Phase 4 cutover it is the
// production chain decider — the legacy ChainMatcher in
// chain_matcher.go is retained only for its scoring helpers
// (TitleSimilarity / ParticipantOverlap / normalizeTitle / stop-word
// list) which v3 still relies on.
//
// The matcher is split into two phases for ingestion ordering:
//
//  1. Decide(ctx, meeting) — pure read; returns a Decision describing
//     what to do (skip / series_id / auto-attach / promote orphan /
//     suggest / etc.) without writing anything yet.
//  2. MutateMeetingForDecision — applies the BEFORE-INSERT side
//     effects (creates a chain row for the promote case, sets
//     meeting.ChainID + provenance fields). Caller MUST then persist
//     the meeting row.
//  3. PersistAfterIngest — applies the AFTER-INSERT side effects
//     (orphan repoint, chain_decisions_v2 row write).
//
// The split exists because chain_decisions_v2.meeting_id is a
// non-deferred FK to meetings(id); persisting decision rows before the
// meeting INSERT would 23503-violate.
type ChainMatcherV3 struct {
	chainRepo     repository.ChainRepository
	meetingRepo   repository.MeetingRepository
	decisionsRepo repository.ChainDecisionsV2Repository
	itemRepo      repository.DecisionRepository
}

// NewChainMatcherV3 wires the matcher to the concrete Postgres
// repositories. Tests substitute fakes via the exported field
// assignment in chain_matcher_v3_test.go.
func NewChainMatcherV3() *ChainMatcherV3 {
	return &ChainMatcherV3{
		chainRepo:     repository.NewChainRepository(),
		meetingRepo:   repository.NewMeetingRepository(),
		decisionsRepo: repository.NewChainDecisionsV2Repository(),
		itemRepo:      repository.NewDecisionRepository(),
	}
}

// ChainMatcherV3AlgorithmVersion is the value persisted in
// chain_decisions_v2.algorithm_version. Bumping this is the standard
// way to invalidate prior shadow rows when the scoring formula
// changes — the (user, meeting, candidate, version) UNIQUE forces a
// fresh row instead of overwriting history.
const ChainMatcherV3AlgorithmVersion = "chain-v3"

// Stage 1 compound hard gate thresholds (roadmap §Stage 1).
const (
	stage1MinParticipant    = 0.25 // floor: drops noise pairs
	stage1MinTitle          = 0.20 // floor: drops noise pairs
	stage1StrongParticipant = 0.50 // OR-side: strong-enough participant signal
	stage1StrongTitle       = 0.60 // OR-side: strong-enough title signal
)

// Stage 2 feature weights (roadmap §Stage 2 table). Sum is exactly 1.0
// so the weighted score lives in [0, 1] and the band thresholds below
// can be read directly as cutoffs on that interval.
const (
	weightParticipant = 0.42
	weightTitle       = 0.28
	weightWeekday     = 0.12
	weightTimeOfDay   = 0.08
	weightRecency     = 0.05
	weightDuration    = 0.05
)

// Decision band thresholds (roadmap §Stage 2).
//
//	score >= 0.50 → auto-chain (`auto_score`)
//	0.40 <= score < 0.50 → user-facing suggestion (`llm_suggestion`)
//	score < 0.40 → orphan
const (
	bandAutoMin       = 0.50
	bandSuggestionMin = 0.40
)

// Suggestion rate-limit (roadmap §Suggestion limits).
const (
	suggestionDailyLimit  = 5
	suggestionDailyWindow = 24 * time.Hour
)

// PairScore is the per-candidate breakdown the v3 matcher computes.
// All fields are populated when Stage1Pass is true; when false, only
// the inputs to the gate (Participant, Title, SpecificCommon) are
// guaranteed populated.
type PairScore struct {
	Candidate           repository.CandidateMeeting
	ParticipantOverlap  float64
	TitleSimilarity     float64 // v3-weighted (generic guard applied)
	TitleSpecificCommon int
	WeekdayMatch        float64
	TimeProximity       float64
	RecencyDecay        float64
	DurationBucketMatch float64
	WeightedScore       float64
	Stage1Pass          bool
}

// ChainCandidate is a chain-level (or pseudo-chain for orphans) view
// of the best pair score against that candidate. The matcher
// aggregates by chain so a chain with 5 historical meetings produces
// one ChainCandidate, scored by its best pair.
type ChainCandidate struct {
	ChainID  uuid.UUID
	IsOrphan bool
	BestPair PairScore
}

// CandidateKey is the string written to chain_decisions_v2.candidate_key.
// `chain:<uuid>` for an existing chain, `meeting:<uuid>` for an orphan.
// Format is load-bearing — suppression queries match on the prefix.
func (c ChainCandidate) CandidateKey() string {
	if c.IsOrphan {
		return "meeting:" + c.BestPair.Candidate.ID.String()
	}
	return chainCandidateKey(c.ChainID)
}

// SuppressionKey namespaces the candidate under the Stage 1+2
// `metadata:` prefix. Stage 3 uses `decision:` independently.
func (c ChainCandidate) SuppressionKey() string {
	return metadataSuppressionKey(c.CandidateKey())
}

// DecisionAction enumerates the things the matcher can decide to do
// for a single meeting. Each value maps to deterministic side effects
// in MutateMeetingForDecision + PersistAfterIngest.
type DecisionAction string

const (
	// ActionSkip — the meeting has a manual_user/manual_removed lock,
	// or it already has a chain_id we won't override. Matcher does
	// nothing; caller proceeds with the meeting as-is.
	ActionSkip DecisionAction = "skip"

	// ActionSeriesID — the meeting carries a CalendarSeriesID and
	// the matcher attaches it to the per-user series chain (creating
	// the chain row on first occurrence).
	ActionSeriesID DecisionAction = "series_id"

	// ActionAutoScoreChain — the metadata winner is an existing chain
	// scoring >= 0.50; attach.
	ActionAutoScoreChain DecisionAction = "auto_score_chain"

	// ActionAutoScoreOrphan — the metadata winner is an orphan meeting
	// scoring >= 0.50; promote both meetings into a fresh chain.
	ActionAutoScoreOrphan DecisionAction = "auto_score_orphan"

	// ActionSuggest — the metadata winner is an existing chain scoring
	// 0.40–0.50; queue a pending LLM suggestion row.
	ActionSuggest DecisionAction = "suggest"

	// ActionRateLimited — would have produced a suggestion but the
	// user already has 5 pending in the last 24h; downgrades to a
	// shadow row for analytics.
	ActionRateLimited DecisionAction = "rate_limited"

	// ActionSuppressed — would have produced a suggestion but the
	// suppression_key has 3+ declines in the last 30 days; downgrades
	// to a shadow row.
	ActionSuppressed DecisionAction = "suppressed"

	// ActionOrphan — no eligible candidate; the meeting stays
	// chain-less. No side effects.
	ActionOrphan DecisionAction = "orphan"
)

// Decision is the full output of Decide. It is data-only (no side
// effects), shaped so MutateMeetingForDecision and PersistAfterIngest
// can act on it deterministically.
type Decision struct {
	Action DecisionAction

	// Chain is the target chain row for series_id / auto_score_chain
	// / auto_score_orphan actions. nil for the rest.
	Chain *models.MeetingChain

	// Winner is the top metadata candidate for auto_score_* / suggest
	// / rate_limited / suppressed actions. nil for series_id / skip /
	// orphan.
	Winner *ChainCandidate

	// FeatureScoresJSON is the per-feature breakdown encoded for
	// chain_decisions_v2.feature_scores. Populated when Winner is
	// populated; empty otherwise.
	FeatureScoresJSON []byte
}

// Decide is the pure read path: gathers candidates, runs Stage 1+2,
// and returns the appropriate Decision. No DB writes.
func (m *ChainMatcherV3) Decide(ctx context.Context, meeting *models.Meeting) (Decision, error) {
	if meeting == nil {
		return Decision{Action: ActionOrphan}, fmt.Errorf("Decide: meeting is nil")
	}
	if meeting.UserID == uuid.Nil {
		return Decision{Action: ActionOrphan}, fmt.Errorf("Decide: meeting.UserID is nil")
	}

	// Manual lock bypass (roadmap §Persistence Contract): manual_user
	// and manual_removed entries opt out of all auto matching. This
	// fires even when chain_id is already set from a previous run, so
	// re-syncs of a manually-corrected meeting don't get clobbered.
	if meeting.ChainAssignmentMethod != nil {
		switch *meeting.ChainAssignmentMethod {
		case models.ChainAssignmentMethodManualUser,
			models.ChainAssignmentMethodManualRemoved:
			return Decision{Action: ActionSkip}, nil
		}
	}

	// Recurring series direct path. A series ID is the most reliable
	// signal we have — skip the metadata score entirely. This also
	// outranks an existing non-manual chain_id on resync, because the
	// series path is stable metadata from the calendar provider while
	// auto_score / llm_suggestion assignments are probabilistic.
	if meeting.CalendarSeriesID != nil && *meeting.CalendarSeriesID != "" {
		chain, err := m.chainRepo.FindBySeriesID(ctx, meeting.UserID, *meeting.CalendarSeriesID)
		if err == nil && chain != nil {
			return Decision{Action: ActionSeriesID, Chain: chain}, nil
		}
		// First occurrence — flag for chain creation. The chain row
		// itself is materialised in MutateMeetingForDecision so
		// Decide stays read-only.
		return Decision{
			Action: ActionSeriesID,
			Chain:  &models.MeetingChain{}, // sentinel: caller creates
		}, nil
	}

	// Already-assigned bypass: if a non-manual, non-series chain_id is
	// already in place (e.g., calendar resync of a meeting we matched
	// last week) we leave it alone. Re-evaluating could flip the chain
	// on every resync which is exactly the false-positive churn the
	// roadmap guards against.
	if meeting.ChainID != nil {
		return Decision{Action: ActionSkip}, nil
	}

	// Metadata path. Window is anchored to the meeting's start_time
	// (not NOW) so future calendar events can't pose as past evidence
	// — see GetUserRecentMeetingsForMatching docstring.
	candidates, err := m.chainRepo.GetUserRecentMeetingsForMatching(
		ctx, meeting.UserID, chainMatchWindowDays, meeting.StartTime, meeting.ID,
	)
	if err != nil {
		return Decision{Action: ActionOrphan}, fmt.Errorf("Decide: fetch candidates: %w", err)
	}

	// Hoist the new-meeting normalisation out of the loop — these
	// don't change per candidate and the inner ParticipantOverlap /
	// TitleSimilarityV3 would otherwise rebuild them N times.
	meetingEmails := attendeeEmailSet(meeting.Attendees)
	meetingTitleWords := normalizeTitle(meeting.Title)

	pairs := make([]PairScore, 0, len(candidates))
	for i := range candidates {
		// Defence-in-depth: SQL already filters manual_removed
		// candidates, but if a future caller queries through a
		// different code path we still skip them here.
		if candidates[i].ChainAssignmentMethod != nil &&
			*candidates[i].ChainAssignmentMethod == models.ChainAssignmentMethodManualRemoved {
			continue
		}
		ps := scorePairCached(meeting, &candidates[i], meetingEmails, meetingTitleWords)
		if ps.Stage1Pass {
			pairs = append(pairs, ps)
		}
	}
	if len(pairs) == 0 {
		return Decision{Action: ActionOrphan}, nil
	}

	chainCandidates := aggregateByChain(pairs)
	winner := chainCandidates[0] // sortChainCandidates pinned the global winner

	score := winner.BestPair.WeightedScore
	if score < bandSuggestionMin {
		return Decision{Action: ActionOrphan}, nil
	}

	featureScores, err := winnerFeatureScoresJSON(&winner)
	if err != nil {
		// JSON marshal failure is exotic; degrade to orphan rather
		// than auto-attach with no audit trail.
		return Decision{Action: ActionOrphan}, fmt.Errorf("Decide: marshal feature scores: %w", err)
	}

	// Auto-band: attach (or promote orphan).
	//
	// Roadmap safety net: even when the weighted score crosses the
	// auto threshold, refuse to auto-attach if the title overlap is
	// purely generic (no specific common token). "Engineering Sync"
	// vs "Backend Sync" with the same team + time of day can score
	// >= 0.50 on participant + weekday + recency alone — but the
	// roadmap's "false positive > false negative" + "generic token
	// tek başına güçlü sinyal olmasın" principles say we should NOT
	// auto-link those. Demote to suggestion (still surfaced; user
	// confirms) instead of silently chaining.
	if score >= bandAutoMin &&
		winner.BestPair.TitleSpecificCommon >= 1 &&
		!hasEqualStrengthChainConflict(chainCandidates) {
		if winner.IsOrphan {
			return Decision{
				Action:            ActionAutoScoreOrphan,
				Winner:            &winner,
				FeatureScoresJSON: featureScores,
			}, nil
		}
		chain, err := m.chainRepo.FindByID(ctx, winner.ChainID)
		if err != nil || chain == nil {
			// Candidate's chain disappeared between the candidates
			// query and this lookup. Defensive degrade — drop to
			// orphan so we don't write a dangling chain_id.
			return Decision{Action: ActionOrphan}, nil
		}
		return Decision{
			Action:            ActionAutoScoreChain,
			Chain:             chain,
			Winner:            &winner,
			FeatureScoresJSON: featureScores,
		}, nil
	}

	// Suggestion band — chain candidates only. Orphan winners in this
	// band stay orphan (no machinery to promote on accept yet; would
	// leave dead chain rows on decline).
	if winner.IsOrphan {
		return Decision{Action: ActionOrphan}, nil
	}

	// Suppression check — if the user's last N decisions for this key
	// were ALL declined (consecutive — an accept resets the streak),
	// downgrade to a shadow row instead of queueing yet another
	// notification the user already said no to. Roadmap §Suggestion
	// limits: "3 ardışık decline".
	suppressionSince := time.Now().Add(-suppressionWindow)
	streakHit, err := m.decisionsRepo.LastNDecisionsAllDeclined(ctx, meeting.UserID, winner.SuppressionKey(), suppressionDeclineLimit, suppressionSince)
	if err == nil && streakHit {
		return Decision{
			Action:            ActionSuppressed,
			Winner:            &winner,
			FeatureScoresJSON: featureScores,
		}, nil
	}

	// Daily rate limit — max 5 pending suggestions per user per 24h.
	pendingSince := time.Now().Add(-suggestionDailyWindow)
	pending, err := m.decisionsRepo.CountPendingForUserSince(ctx, meeting.UserID, pendingSince)
	if err == nil && pending >= suggestionDailyLimit {
		return Decision{
			Action:            ActionRateLimited,
			Winner:            &winner,
			FeatureScoresJSON: featureScores,
		}, nil
	}

	// Suggestion: need the chain row populated for the suggestion
	// detail view + accept flow.
	chain, err := m.chainRepo.FindByID(ctx, winner.ChainID)
	if err != nil || chain == nil {
		return Decision{Action: ActionOrphan}, nil
	}
	return Decision{
		Action:            ActionSuggest,
		Chain:             chain,
		Winner:            &winner,
		FeatureScoresJSON: featureScores,
	}, nil
}

// MutateMeetingForDecision applies the BEFORE-INSERT side effects of
// a Decision: creates the chain row when needed and stamps the chain
// assignment fields onto the meeting struct. Caller MUST then persist
// the meeting row (CreateMeeting / UpdateMeetingIngestion) for the
// stamps to take effect.
//
// Decision is taken by pointer so chain materialisation in the
// series-new and orphan-promotion paths can publish the freshly-
// created chain back to the caller. PersistAfterIngest reads
// `decision.Chain` to attribute the audit row to the correct chain;
// without back-propagation those rows would persist with
// CandidateChainID=NULL and a "chain:unknown" key.
func (m *ChainMatcherV3) MutateMeetingForDecision(ctx context.Context, meeting *models.Meeting, decision *Decision) error {
	if decision == nil {
		return fmt.Errorf("MutateMeetingForDecision: decision is nil")
	}
	switch decision.Action {
	case ActionSkip, ActionOrphan, ActionSuggest, ActionRateLimited, ActionSuppressed:
		return nil

	case ActionSeriesID:
		// Existing series chain → just stamp; new series → create now.
		if decision.Chain == nil || decision.Chain.ID == uuid.Nil {
			newChain := &models.MeetingChain{
				ID:               uuid.New(),
				UserID:           meeting.UserID,
				ChainType:        "calendar_series",
				CalendarSeriesID: meeting.CalendarSeriesID,
				DisplayName:      &meeting.Title,
			}
			if err := m.chainRepo.CreateChain(ctx, newChain); err != nil {
				return fmt.Errorf("MutateMeetingForDecision: create series chain: %w", err)
			}
			decision.Chain = newChain
		}
		stampMeetingChain(meeting, decision.Chain.ID, models.ChainAssignmentMethodSeriesID, nil, nil)
		return nil

	case ActionAutoScoreChain:
		if decision.Chain == nil {
			return fmt.Errorf("MutateMeetingForDecision: auto_score_chain requires a chain")
		}
		score := decision.Winner.BestPair.WeightedScore
		stampMeetingChain(meeting, decision.Chain.ID, models.ChainAssignmentMethodAutoScore, &score, decision.FeatureScoresJSON)
		return nil

	case ActionAutoScoreOrphan:
		if decision.Winner == nil {
			return fmt.Errorf("MutateMeetingForDecision: auto_score_orphan requires a winner")
		}
		newChain := &models.MeetingChain{
			ID:          uuid.New(),
			UserID:      meeting.UserID,
			ChainType:   "participant_overlap_title",
			DisplayName: &meeting.Title,
		}
		if err := m.chainRepo.CreateChain(ctx, newChain); err != nil {
			return fmt.Errorf("MutateMeetingForDecision: create orphan-promotion chain: %w", err)
		}
		score := decision.Winner.BestPair.WeightedScore
		stampMeetingChain(meeting, newChain.ID, models.ChainAssignmentMethodAutoScore, &score, decision.FeatureScoresJSON)
		// Publish the freshly-created chain back to caller so
		// PersistAfterIngest can repoint the orphan and write a
		// correctly-attributed audit row.
		decision.Chain = newChain
		return nil
	}
	return nil
}

// PersistAfterIngest applies the AFTER-INSERT side effects of a
// Decision. Safe to call for any Decision; no-ops for actions with
// nothing to persist after meeting INSERT.
//
// Errors are returned but the calling code path
// (MeetingService.UpsertMeeting) deliberately log-and-continues so a
// matcher hiccup never blocks ingest.
func (m *ChainMatcherV3) PersistAfterIngest(ctx context.Context, meeting *models.Meeting, decision Decision) error {
	switch decision.Action {
	case ActionSkip, ActionOrphan:
		return nil

	case ActionSeriesID, ActionAutoScoreChain:
		return m.writeAppliedDecision(ctx, meeting, decision)

	case ActionAutoScoreOrphan:
		// Repoint the orphan candidate at the new chain. We use the
		// dedicated RepointMeetingChain rather than UpdateMeetingFields
		// because the latter's allowlist deliberately excludes
		// chain_id (calendar sync code path must not write it). The
		// orphan's existing decision_items follow ONLY if the
		// meeting-side repoint actually landed — otherwise we'd leave
		// items pointing at a chain whose owning meeting is still
		// orphan in the meetings table, the worst kind of split state.
		if decision.Winner != nil && meeting.ChainID != nil {
			orphanID := decision.Winner.BestPair.Candidate.ID
			updated, err := m.meetingRepo.RepointMeetingChain(ctx, orphanID, *meeting.ChainID)
			if err != nil {
				slog.Error("orphan repoint failed",
					"orphan_meeting_id", orphanID,
					"chain_id", meeting.ChainID, "error", err)
			} else if !updated {
				slog.Warn("orphan repoint matched no row",
					"orphan_meeting_id", orphanID,
					"chain_id", meeting.ChainID)
			} else {
				// Repoint succeeded — safe to cascade decision_items.
				if cErr := m.itemRepo.UpdateChainIDForMeeting(ctx, orphanID, *meeting.ChainID); cErr != nil {
					slog.Error("orphan decision_items cascade failed",
						"orphan_meeting_id", orphanID,
						"chain_id", meeting.ChainID, "error", cErr)
				}
			}
		}
		return m.writeAppliedDecision(ctx, meeting, decision)

	case ActionSuggest:
		return m.writeSuggestionRow(ctx, meeting, decision, models.ChainDecisionModeSuggestion, models.ChainDecisionStatusPending)

	case ActionRateLimited:
		// Analytics row — log that we declined to surface a suggestion.
		// `shadow_only` is the neutral "logged but took no action"
		// status; `expired` would imply this row had previously been
		// pending, which it never was.
		return m.writeSuggestionRow(ctx, meeting, decision, models.ChainDecisionModeShadow, models.ChainDecisionStatusShadowOnly)

	case ActionSuppressed:
		// Suppression status — the suggestion *was* the kind we'd
		// normally queue but the user has already declined this
		// candidate enough times to mute it.
		return m.writeSuggestionRow(ctx, meeting, decision, models.ChainDecisionModeShadow, models.ChainDecisionStatusSuppressed)
	}
	return nil
}

// writeAppliedDecision logs a chain_decisions_v2 row marking that v3
// applied this candidate. Mode='applied' / status='applied' so the
// downstream comparison query can attribute meetings.chain_id back to
// the matcher decision that produced it.
//
// Caller MUST hand a Decision whose Chain field has been resolved
// (Decide does this for series-existing / auto-score-chain;
// MutateMeetingForDecision does it for series-new / orphan-promotion).
// A nil Chain here means "no row to write" — applied rows without a
// chain are meaningless.
func (m *ChainMatcherV3) writeAppliedDecision(ctx context.Context, meeting *models.Meeting, decision Decision) error {
	if decision.Chain == nil || decision.Chain.ID == uuid.Nil {
		return fmt.Errorf("writeAppliedDecision: decision.Chain not resolved for action %s", decision.Action)
	}
	candidateKey := chainCandidateKey(decision.Chain.ID)
	row := repository.ChainDecisionV2Row{
		UserID:           meeting.UserID,
		MeetingID:        meeting.ID,
		CandidateChainID: ptrUUID(decision.Chain.ID),
		CandidateKey:     candidateKey,
		AlgorithmVersion: ChainMatcherV3AlgorithmVersion,
		Mode:             models.ChainDecisionModeApplied,
		Status:           models.ChainDecisionStatusApplied,
		FeatureScores:    decision.FeatureScoresJSON,
		SuppressionKey:   ptrString(metadataSuppressionKey(candidateKey)),
	}
	if decision.Winner != nil {
		row.MatchedMeetingID = ptrUUID(decision.Winner.BestPair.Candidate.ID)
		row.ParticipantOverlap = ptrFloat(decision.Winner.BestPair.ParticipantOverlap)
		row.TitleSimilarity = ptrFloat(decision.Winner.BestPair.TitleSimilarity)
		row.WeightedScore = ptrFloat(decision.Winner.BestPair.WeightedScore)
	}
	if err := m.decisionsRepo.LogShadowDecision(ctx, row); err != nil {
		return fmt.Errorf("writeAppliedDecision: %w", err)
	}
	return nil
}

// writeSuggestionRow inserts a chain_decisions_v2 row for the
// suggest / rate_limited / suppressed actions. mode and status are
// caller-controlled so the same shape can produce a real pending
// suggestion or a shadow analytics row.
func (m *ChainMatcherV3) writeSuggestionRow(ctx context.Context, meeting *models.Meeting, decision Decision, mode, status string) error {
	if decision.Winner == nil {
		return nil
	}
	row := repository.ChainDecisionV2Row{
		UserID:             meeting.UserID,
		MeetingID:          meeting.ID,
		MatchedMeetingID:   ptrUUID(decision.Winner.BestPair.Candidate.ID),
		CandidateKey:       decision.Winner.CandidateKey(),
		AlgorithmVersion:   ChainMatcherV3AlgorithmVersion,
		Mode:               mode,
		Status:             status,
		ParticipantOverlap: ptrFloat(decision.Winner.BestPair.ParticipantOverlap),
		TitleSimilarity:    ptrFloat(decision.Winner.BestPair.TitleSimilarity),
		WeightedScore:      ptrFloat(decision.Winner.BestPair.WeightedScore),
		FeatureScores:      decision.FeatureScoresJSON,
		SuppressionKey:     ptrString(decision.Winner.SuppressionKey()),
	}
	if !decision.Winner.IsOrphan {
		row.CandidateChainID = ptrUUID(decision.Winner.ChainID)
	}
	if err := m.decisionsRepo.LogShadowDecision(ctx, row); err != nil {
		return fmt.Errorf("writeSuggestionRow (%s): %w", mode, err)
	}
	return nil
}

// stampMeetingChain mutates the meeting struct so the upcoming
// CreateMeeting / UpdateMeetingIngestion picks up the assignment
// fields in a single INSERT/UPDATE.
func stampMeetingChain(meeting *models.Meeting, chainID uuid.UUID, method string, score *float64, reason []byte) {
	meeting.ChainID = &chainID
	method = strings.TrimSpace(method)
	meeting.ChainAssignmentMethod = &method
	meeting.ChainAssignmentScore = score
	meeting.ChainAssignmentReason = reason
	// LockedAt left untouched — only manual actions set the lock.
}

// winnerFeatureScoresJSON marshals the chain candidate's feature
// breakdown into the JSON shape persisted in chain_decisions_v2.
// Centralised so MutateMeetingForDecision and PersistAfterIngest
// agree on the field set.
func winnerFeatureScoresJSON(c *ChainCandidate) ([]byte, error) {
	return json.Marshal(map[string]any{
		"participant_overlap":   c.BestPair.ParticipantOverlap,
		"title_similarity":      c.BestPair.TitleSimilarity,
		"title_specific_common": c.BestPair.TitleSpecificCommon,
		"weekday_match":         c.BestPair.WeekdayMatch,
		"time_proximity":        c.BestPair.TimeProximity,
		"recency_decay":         c.BestPair.RecencyDecay,
		"duration_bucket_match": c.BestPair.DurationBucketMatch,
		"matched_meeting_id":    c.BestPair.Candidate.ID.String(),
		"matched_meeting_title": c.BestPair.Candidate.Title,
	})
}

// scorePair runs Stage 1 (compound hard gate) and Stage 2 (weighted
// score) for a single new-meeting / candidate pair.
// scorePair is the slow path used by tests and any one-shot caller —
// it normalises the meeting's attendee set + title words on every
// call. Hot loops (Decide) should call scorePairCached with hoisted
// inputs to avoid the per-candidate rebuild.
func scorePair(m *models.Meeting, cand *repository.CandidateMeeting) PairScore {
	return scorePairCached(m, cand, attendeeEmailSet(m.Attendees), normalizeTitle(m.Title))
}

// scorePairCached evaluates the pair using already-normalised
// new-meeting inputs. Identical scoring rules; only the upfront
// allocation changes.
func scorePairCached(m *models.Meeting, cand *repository.CandidateMeeting, mEmails map[string]bool, mTitleWords []string) PairScore {
	ps := PairScore{Candidate: *cand}

	if len(m.Attendees) < chainMinAttendees || len(cand.Attendees) < chainMinAttendees {
		// Stage 0 floor: both sides must clear the 3-attendee minimum
		// so 1-on-1's don't pollute the matcher.
		return ps
	}

	_, p := participantOverlapWithSet(mEmails, cand.Attendees)
	t, specific := titleSimilarityV3WithWords(mTitleWords, cand.Title)

	ps.ParticipantOverlap = p
	ps.TitleSimilarity = t
	ps.TitleSpecificCommon = specific

	floorOK := p >= stage1MinParticipant && t >= stage1MinTitle
	strongOK := p >= stage1StrongParticipant || t >= stage1StrongTitle
	if !floorOK || !strongOK {
		return ps
	}
	ps.Stage1Pass = true

	ps.WeekdayMatch = WeekdayMatch(m.StartTime, cand.StartTime)
	ps.TimeProximity = TimeProximity(m.StartTime, cand.StartTime)
	ps.RecencyDecay = RecencyDecay(cand.StartTime, m.StartTime)
	ps.DurationBucketMatch = DurationBucketMatch(
		m.EndTime.Sub(m.StartTime),
		cand.EndTime.Sub(cand.StartTime),
	)

	ps.WeightedScore = weightParticipant*p +
		weightTitle*t +
		weightWeekday*ps.WeekdayMatch +
		weightTimeOfDay*ps.TimeProximity +
		weightRecency*ps.RecencyDecay +
		weightDuration*ps.DurationBucketMatch

	return ps
}

// aggregateByChain groups pair scores by chain (or treats each orphan
// as its own pseudo-chain) and keeps the best pair per chain. Output
// is sorted descending by score with the standard tie-break.
func aggregateByChain(pairs []PairScore) []ChainCandidate {
	byChain := make(map[uuid.UUID]*ChainCandidate)
	var orphans []ChainCandidate

	for _, ps := range pairs {
		if ps.Candidate.ChainID != nil {
			cid := *ps.Candidate.ChainID
			existing, ok := byChain[cid]
			if !ok {
				byChain[cid] = &ChainCandidate{
					ChainID:  cid,
					IsOrphan: false,
					BestPair: ps,
				}
				continue
			}
			if pairBetter(ps, existing.BestPair) {
				existing.BestPair = ps
			}
		} else {
			orphans = append(orphans, ChainCandidate{
				ChainID:  uuid.Nil,
				IsOrphan: true,
				BestPair: ps,
			})
		}
	}

	out := make([]ChainCandidate, 0, len(byChain)+len(orphans))
	for _, cc := range byChain {
		out = append(out, *cc)
	}
	out = append(out, orphans...)

	sortChainCandidates(out)
	return out
}

// pairBetter — full deterministic ordering: weighted score → title
// similarity → most recent → ascending UUID. Determinism matters
// because the same pair-set must always produce the same winner.
func pairBetter(a, b PairScore) bool {
	if a.WeightedScore != b.WeightedScore {
		return a.WeightedScore > b.WeightedScore
	}
	if a.TitleSimilarity != b.TitleSimilarity {
		return a.TitleSimilarity > b.TitleSimilarity
	}
	if !a.Candidate.StartTime.Equal(b.Candidate.StartTime) {
		return a.Candidate.StartTime.After(b.Candidate.StartTime)
	}
	return strings.Compare(a.Candidate.ID.String(), b.Candidate.ID.String()) < 0
}

func sortChainCandidates(cs []ChainCandidate) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && pairBetter(cs[j].BestPair, cs[j-1].BestPair); j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}

func hasEqualStrengthChainConflict(cs []ChainCandidate) bool {
	if len(cs) < 2 || cs[0].IsOrphan {
		return false
	}
	top := cs[0]
	const eps = 1e-9
	for _, other := range cs[1:] {
		if other.IsOrphan {
			continue
		}
		if math.Abs(top.BestPair.WeightedScore-other.BestPair.WeightedScore) <= eps &&
			math.Abs(top.BestPair.TitleSimilarity-other.BestPair.TitleSimilarity) <= eps {
			return true
		}
		// Candidates are sorted by weighted score first; once score
		// falls away, later entries cannot be equal-strength conflicts.
		if top.BestPair.WeightedScore-other.BestPair.WeightedScore > eps {
			return false
		}
	}
	return false
}

// IsAutoScoreBand reports whether a weighted score would auto-chain.
func IsAutoScoreBand(score float64) bool { return score >= bandAutoMin }

// IsSuggestionBand reports whether a weighted score would surface as
// a user-facing suggestion (between the suggestion floor and the
// auto floor).
func IsSuggestionBand(score float64) bool {
	return score >= bandSuggestionMin && score < bandAutoMin
}

// runProductionMatcher is the boundary called from
// MeetingService.UpsertMeeting. Returns the Decision for any
// post-INSERT side effects to be applied via PersistAfterIngest.
//
// Errors during Decide degrade silently to ActionOrphan so a v3
// regression cannot block ingest. MutateMeetingForDecision errors
// short-circuit to no-op (meeting persists without chain assignment).
func runProductionMatcher(ctx context.Context, m *ChainMatcherV3, meeting *models.Meeting) Decision {
	if m == nil || meeting == nil {
		return Decision{Action: ActionOrphan}
	}
	decision, err := m.Decide(ctx, meeting)
	if err != nil {
		slog.Warn("chain matcher v3 decide failed",
			"meeting_id", meeting.ID, "error", err)
		return Decision{Action: ActionOrphan}
	}
	if err := m.MutateMeetingForDecision(ctx, meeting, &decision); err != nil {
		slog.Warn("chain matcher v3 mutate failed",
			"meeting_id", meeting.ID, "action", decision.Action, "error", err)
		return Decision{Action: ActionOrphan}
	}
	return decision
}

// Pointer helpers — chain_decisions_v2 columns are nullable in SQL
// and *T is the cleanest way to disambiguate "0.0" from "absent" at
// scan time without per-column NullX types.
func ptrUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

func ptrFloat(f float64) *float64 { return &f }
func ptrString(s string) *string  { return &s }
