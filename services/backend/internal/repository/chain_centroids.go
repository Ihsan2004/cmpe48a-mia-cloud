package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ChainCentroid is the cached vector representation of a chain's
// recent decision content. Used by the Stage 3 (decision-based)
// chain matcher: a meeting's decision centroid is compared against
// each eligible chain's centroid via cosine similarity.
type ChainCentroid struct {
	ChainID             uuid.UUID
	Centroid            []float64
	SourceDecisionCount int
	ComputedAt          time.Time
}

// EligibleChainRow is the per-chain summary returned by
// GetEligibleChainsForUser. The matcher uses LastMeetingStart for
// the 90-day recency guard and DecisionBoundCount for the ≥3-meeting
// guard.
type EligibleChainRow struct {
	ChainID                   uuid.UUID
	LastMeetingStart          time.Time
	DecisionBoundMeetingCount int
}

// ErrChainCentroidNotFound is returned when GetCentroid finds no
// cached row for the chain. Distinct from a real DB error so callers
// can decide between "rebuild" and "fail".
var ErrChainCentroidNotFound = errors.New("chain centroid not cached")

// ChainCentroidsRepository persists and queries the
// chain_decision_centroids cache plus the read-side queries the
// Stage 3 matcher needs (eligible chains, recent decision texts,
// freshness check).
type ChainCentroidsRepository interface {
	// GetCentroid returns the cached centroid row, or
	// ErrChainCentroidNotFound when no cache exists yet.
	GetCentroid(ctx context.Context, chainID uuid.UUID) (*ChainCentroid, error)

	// UpsertCentroid replaces the cached centroid for the chain.
	// computed_at is set to NOW server-side so freshness comparisons
	// don't drift with client clock skew.
	UpsertCentroid(ctx context.Context, chainID uuid.UUID, centroid []float64, sourceCount int) error

	// GetEligibleChainsForUser returns the user's chains that satisfy
	// the Stage 3 cold-start guards:
	//   - ≥3 meetings with at least one decision_item attached
	//   - most recent meeting started within `recencyWindow`
	// excludeMeetingID drops the meeting being processed from the
	// count so a brand-new orphan doesn't accidentally count itself.
	GetEligibleChainsForUser(
		ctx context.Context,
		userID uuid.UUID,
		excludeMeetingID uuid.UUID,
		minDecisionBoundMeetings int,
		recencyWindow time.Duration,
	) ([]EligibleChainRow, error)

	// GetRecentDecisionTextsForChain returns up to `limit` most-recent
	// decision_item texts across the chain's meetings. Powers centroid
	// rebuild when the cache is missing/stale.
	GetRecentDecisionTextsForChain(ctx context.Context, chainID uuid.UUID, limit int) ([]string, error)

	// MostRecentDecisionTimeForChain returns the latest decision_item
	// created_at across the chain. Used by the lazy-invalidation check
	// (centroid cache is stale when MostRecent > ComputedAt).
	MostRecentDecisionTimeForChain(ctx context.Context, chainID uuid.UUID) (time.Time, error)
}

type postgresChainCentroidsRepository struct{}

func NewChainCentroidsRepository() ChainCentroidsRepository {
	return &postgresChainCentroidsRepository{}
}

func (r *postgresChainCentroidsRepository) GetCentroid(ctx context.Context, chainID uuid.UUID) (*ChainCentroid, error) {
	if chainID == uuid.Nil {
		return nil, fmt.Errorf("GetCentroid: chain_id required")
	}
	var (
		raw       string
		count     int
		computed  time.Time
	)
	err := DB.QueryRow(ctx, `
		SELECT centroid::text, source_decision_count, computed_at
		FROM chain_decision_centroids
		WHERE chain_id = $1
	`, chainID).Scan(&raw, &count, &computed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrChainCentroidNotFound
		}
		return nil, fmt.Errorf("get chain centroid: %w", err)
	}
	vec, err := parsePgVector(raw)
	if err != nil {
		return nil, fmt.Errorf("get chain centroid: %w", err)
	}
	return &ChainCentroid{
		ChainID:             chainID,
		Centroid:            vec,
		SourceDecisionCount: count,
		ComputedAt:          computed,
	}, nil
}

func (r *postgresChainCentroidsRepository) UpsertCentroid(ctx context.Context, chainID uuid.UUID, centroid []float64, sourceCount int) error {
	if chainID == uuid.Nil {
		return fmt.Errorf("UpsertCentroid: chain_id required")
	}
	if sourceCount <= 0 {
		return fmt.Errorf("UpsertCentroid: source_decision_count must be > 0 (CHECK constraint)")
	}
	if len(centroid) == 0 {
		return fmt.Errorf("UpsertCentroid: centroid is empty")
	}
	vec := formatPgVector(centroid)
	_, err := DB.Exec(ctx, `
		INSERT INTO chain_decision_centroids (chain_id, centroid, source_decision_count, computed_at)
		VALUES ($1, $2::vector, $3, now())
		ON CONFLICT (chain_id) DO UPDATE SET
		    centroid = EXCLUDED.centroid,
		    source_decision_count = EXCLUDED.source_decision_count,
		    computed_at = EXCLUDED.computed_at
	`, chainID, vec, sourceCount)
	if err != nil {
		return fmt.Errorf("upsert chain centroid: %w", err)
	}
	return nil
}

func (r *postgresChainCentroidsRepository) GetEligibleChainsForUser(
	ctx context.Context,
	userID uuid.UUID,
	excludeMeetingID uuid.UUID,
	minDecisionBoundMeetings int,
	recencyWindow time.Duration,
) ([]EligibleChainRow, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("GetEligibleChainsForUser: user_id required")
	}
	if minDecisionBoundMeetings <= 0 {
		minDecisionBoundMeetings = 3
	}

	// Days as int because pgx can't pgEncode INTERVAL '... days' against
	// a parameter slot directly; multiplying a literal interval keeps it
	// pgx-compatible (same trick the chain matcher v2 query uses).
	days := int(recencyWindow / (24 * time.Hour))
	if days <= 0 {
		days = 90
	}

	rows, err := DB.Query(ctx, `
		WITH chain_meetings AS (
			SELECT
				mc.id AS chain_id,
				m.id AS meeting_id,
				m.start_time,
				EXISTS (
					SELECT 1 FROM decision_items di WHERE di.meeting_id = m.id
				) AS has_decisions
			FROM meeting_chains mc
			JOIN meetings m ON m.chain_id = mc.id
			WHERE mc.user_id = $1
			  AND m.id <> $2
		)
		SELECT
			chain_id,
			MAX(start_time) AS last_meeting_start,
			COUNT(*) FILTER (WHERE has_decisions)::int AS decision_bound_count
		FROM chain_meetings
		GROUP BY chain_id
		HAVING COUNT(*) FILTER (WHERE has_decisions) >= $3
		   AND MAX(start_time) >= NOW() - (INTERVAL '1 day' * $4)
	`, userID, excludeMeetingID, minDecisionBoundMeetings, days)
	if err != nil {
		return nil, fmt.Errorf("eligible chains query: %w", err)
	}
	defer rows.Close()

	var out []EligibleChainRow
	for rows.Next() {
		var row EligibleChainRow
		if err := rows.Scan(&row.ChainID, &row.LastMeetingStart, &row.DecisionBoundMeetingCount); err != nil {
			return nil, fmt.Errorf("eligible chains scan: %w", err)
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *postgresChainCentroidsRepository) GetRecentDecisionTextsForChain(ctx context.Context, chainID uuid.UUID, limit int) ([]string, error) {
	if chainID == uuid.Nil {
		return nil, fmt.Errorf("GetRecentDecisionTextsForChain: chain_id required")
	}
	if limit <= 0 {
		limit = 10
	}
	// chain_id on decision_items is the denormalised chain pointer
	// kept in sync by the cascade writers (see chain_assignment_service.go).
	// We trust it here for the centroid build; the source-of-truth FK
	// path (meetings.chain_id) would force an extra JOIN that the
	// hot-path post-meeting trigger doesn't need.
	rows, err := DB.Query(ctx, `
		SELECT text
		FROM decision_items
		WHERE chain_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, chainID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent decision texts query: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("recent decision texts scan: %w", err)
		}
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *postgresChainCentroidsRepository) MostRecentDecisionTimeForChain(ctx context.Context, chainID uuid.UUID) (time.Time, error) {
	if chainID == uuid.Nil {
		return time.Time{}, fmt.Errorf("MostRecentDecisionTimeForChain: chain_id required")
	}
	var t *time.Time
	err := DB.QueryRow(ctx, `
		SELECT MAX(created_at) FROM decision_items WHERE chain_id = $1
	`, chainID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("most-recent decision query: %w", err)
	}
	if t == nil {
		// No decisions on the chain — return the zero time so the
		// caller's "centroid stale?" check trivially says no (any
		// computed_at > zero).
		return time.Time{}, nil
	}
	return *t, nil
}

// ===== pgvector helpers =====
//
// The transcript_chat_blocks UpdateChatBlockEmbedding path uses the
// same `[v1,v2,...]::vector` text format (see repository/transcript.go).
// Centralised here because Stage 3 has both directions (read + write).

// formatPgVector renders []float64 as pgvector's text input form.
// `%g` mirrors the chat-block writer to keep the on-disk
// representation byte-identical for downstream tooling.
func formatPgVector(v []float64) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(f, 'g', -1, 64)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// parsePgVector decodes pgvector's text form (`[v1,v2,...]`) into
// []float64. Whitespace tolerant; rejects malformed or empty input.
func parsePgVector(raw string) ([]float64, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("invalid pgvector text: %q", raw)
	}
	inner := strings.TrimSpace(raw[1 : len(raw)-1])
	if inner == "" {
		return nil, fmt.Errorf("empty pgvector")
	}
	parts := strings.Split(inner, ",")
	out := make([]float64, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("parse pgvector index %d: %w", i, err)
		}
		out[i] = f
	}
	return out, nil
}
