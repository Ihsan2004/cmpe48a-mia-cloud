package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// CandidateMeeting is a lightweight projection of a meeting used by the
// chain matcher when scanning a user's recent meetings (chain'd + orphan)
// for participant + title overlap. EndTime joined the projection in
// chain detection v3 (Phase 2) so the metadata score can compute the
// duration-bucket feature without a second round-trip; ChainAssignment
// Method joined in the post-Phase-6 review so the matcher can drop
// manual_removed candidates before they get scored.
type CandidateMeeting struct {
	ID                    uuid.UUID
	ChainID               *uuid.UUID
	Title                 string
	StartTime             time.Time
	EndTime               time.Time
	ChainAssignmentMethod *string
	Attendees             []models.Attendee
}

// ChainSummary projects a user's chain with the bits the move-to-chain
// picker needs: a recognisable label (display_name with latest-title
// fallback so legacy/blank-name chains still render usefully), the most
// recent meeting timestamp (drives sort order + secondary label), and a
// count so the UI can hint at chain "size" without a second query.
type ChainSummary struct {
	ID             uuid.UUID
	DisplayName    string
	LastMeetingAt  time.Time
	MeetingCount   int
}

type ChainRepository interface {
	// FindBySeriesID became tenant-scoped in chain detection v3 (migration 019)
	// — every chain row carries a user_id now and a Google calendar_series_id
	// shared across users would otherwise return another tenant's chain.
	// Always pass the meeting's owning user_id alongside the series id.
	FindBySeriesID(ctx context.Context, userID uuid.UUID, seriesID string) (*models.MeetingChain, error)
	FindByID(ctx context.Context, chainID uuid.UUID) (*models.MeetingChain, error)
	CreateChain(ctx context.Context, chain *models.MeetingChain) error
	GetCandidateChainsByOverlap(ctx context.Context, userID uuid.UUID, attendeeEmails []string, minOverlap float64, minCommon int) ([]models.MeetingChain, error)
	// GetUserRecentMeetingsForMatching returns the user's meetings within the
	// last `windowDays` days (both chain'd and orphan), excluding the meeting
	// identified by excludeID. Used by ChainMatcher Rule 2 to compare a new
	// meeting against the user's recent meeting history.
	// GetUserRecentMeetingsForMatching returns the user's meetings within
	// `windowDays` days BEFORE `meetingStartTime`, excluding the meeting
	// identified by excludeID. The window is anchored to the meeting
	// being processed (not NOW) so:
	//   - past calendar resync runs see the same candidate set every time
	//   - future calendar events do not pollute the matcher (a meeting
	//     scheduled tomorrow shouldn't count an event scheduled next week
	//     as evidence — the roadmap's "son 45 gün" intent is past-only)
	GetUserRecentMeetingsForMatching(ctx context.Context, userID uuid.UUID, windowDays int, meetingStartTime time.Time, excludeID uuid.UUID) ([]CandidateMeeting, error)
	// ListUserChains returns the user's chains ordered by most-recent
	// meeting first. Backs the move-to-chain picker; chains with zero
	// meetings (theoretically impossible after Phase 4 cleanup but a
	// defence-in-depth filter against stale rows) are excluded so the
	// UI doesn't surface options the user wouldn't recognise.
	ListUserChains(ctx context.Context, userID uuid.UUID, limit int) ([]ChainSummary, error)
}

type postgresChainRepository struct{}

func NewChainRepository() ChainRepository {
	return &postgresChainRepository{}
}

func (r *postgresChainRepository) FindBySeriesID(ctx context.Context, userID uuid.UUID, seriesID string) (*models.MeetingChain, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("FindBySeriesID requires a non-nil userID")
	}
	var chain models.MeetingChain
	err := DB.QueryRow(ctx, `
		SELECT id, user_id, chain_type, calendar_series_id, participant_hash, display_name, created_at
		FROM meeting_chains
		WHERE user_id = $1 AND calendar_series_id = $2
	`, userID, seriesID).Scan(
		&chain.ID, &chain.UserID, &chain.ChainType, &chain.CalendarSeriesID,
		&chain.ParticipantHash, &chain.DisplayName, &chain.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("chain not found for series %s (user %s): %w", seriesID, userID, err)
	}
	return &chain, nil
}

func (r *postgresChainRepository) FindByID(ctx context.Context, chainID uuid.UUID) (*models.MeetingChain, error) {
	var chain models.MeetingChain
	err := DB.QueryRow(ctx, `
		SELECT id, user_id, chain_type, calendar_series_id, participant_hash, display_name, created_at
		FROM meeting_chains WHERE id = $1
	`, chainID).Scan(
		&chain.ID, &chain.UserID, &chain.ChainType, &chain.CalendarSeriesID,
		&chain.ParticipantHash, &chain.DisplayName, &chain.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("chain not found: %w", err)
	}
	return &chain, nil
}

func (r *postgresChainRepository) CreateChain(ctx context.Context, chain *models.MeetingChain) error {
	if chain == nil {
		return fmt.Errorf("chain cannot be nil")
	}
	if chain.UserID == uuid.Nil {
		// Migration 019 made user_id NOT NULL; refusing here gives a
		// clearer error than the DB constraint violation that would
		// otherwise come from the INSERT.
		return fmt.Errorf("chain.UserID cannot be nil — chain detection v3 requires tenant-scoped chains")
	}
	if chain.ID == uuid.Nil {
		chain.ID = uuid.New()
	}
	_, err := DB.Exec(ctx, `
		INSERT INTO meeting_chains (id, user_id, chain_type, calendar_series_id, participant_hash, display_name)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, chain.ID, chain.UserID, chain.ChainType, chain.CalendarSeriesID, chain.ParticipantHash, chain.DisplayName)
	if err != nil {
		return fmt.Errorf("failed to create chain: %w", err)
	}
	return nil
}

func (r *postgresChainRepository) GetCandidateChainsByOverlap(ctx context.Context, userID uuid.UUID, attendeeEmails []string, minOverlap float64, minCommon int) ([]models.MeetingChain, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("GetCandidateChainsByOverlap requires a non-nil userID")
	}
	if len(attendeeEmails) == 0 {
		return nil, nil
	}

	// Find chains where the latest meeting has sufficient participant overlap.
	// We extract attendee emails from the JSONB array, compute intersection with
	// the provided list, and filter by minCommon and minOverlap thresholds.
	query := `
		WITH latest_chain_meeting AS (
			SELECT DISTINCT ON (chain_id)
				chain_id, attendees
			FROM meetings
			WHERE chain_id IS NOT NULL
			  AND user_id = $1
			ORDER BY chain_id, start_time DESC
		),
		overlap_calc AS (
			SELECT
				lcm.chain_id,
				(
					SELECT COUNT(DISTINCT e.email)
					FROM jsonb_array_elements(lcm.attendees) AS elem,
					LATERAL (SELECT lower(trim(elem->>'email')) AS email) AS e
					WHERE e.email = ANY($2)
				) AS common_count,
				GREATEST(
					(SELECT COUNT(DISTINCT lower(trim(elem->>'email'))) FROM jsonb_array_elements(lcm.attendees) AS elem),
					$5::int
				) AS max_len
			FROM latest_chain_meeting lcm
		)
		SELECT mc.id, mc.user_id, mc.chain_type, mc.calendar_series_id, mc.participant_hash, mc.display_name, mc.created_at
		FROM meeting_chains mc
		JOIN overlap_calc oc ON oc.chain_id = mc.id
		WHERE mc.user_id = $1
		  AND mc.chain_type = 'participant_overlap_title'
		  AND oc.common_count >= $3
		  AND (oc.common_count::float / NULLIF(oc.max_len, 0)) >= $4
	`

	rows, err := DB.Query(ctx, query, userID, attendeeEmails, minCommon, minOverlap, len(attendeeEmails))
	if err != nil {
		return nil, fmt.Errorf("failed to query candidate chains: %w", err)
	}
	defer rows.Close()

	var chains []models.MeetingChain
	for rows.Next() {
		var chain models.MeetingChain
		if err := rows.Scan(
			&chain.ID, &chain.UserID, &chain.ChainType, &chain.CalendarSeriesID,
			&chain.ParticipantHash, &chain.DisplayName, &chain.CreatedAt,
		); err != nil {
			continue
		}
		chains = append(chains, chain)
	}
	return chains, nil
}

func (r *postgresChainRepository) ListUserChains(ctx context.Context, userID uuid.UUID, limit int) ([]ChainSummary, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("ListUserChains requires a non-nil userID")
	}
	if limit <= 0 {
		limit = 100
	}

	// stats subquery is INNER-joined so chains with zero member meetings
	// drop out (kept the picker free of orphan-chain noise without a
	// HAVING clause). The lateral subquery resolves a friendly label:
	// prefer the chain's stored display_name; fall back to the title of
	// the most recent member meeting so legacy chains created before
	// display_name backfill still render something the user recognises.
	query := `
		SELECT
			mc.id,
			COALESCE(NULLIF(mc.display_name, ''), lt.title, '') AS display_name,
			stats.last_meeting_at,
			stats.meeting_count
		FROM meeting_chains mc
		JOIN (
			SELECT
				chain_id,
				MAX(start_time) AS last_meeting_at,
				COUNT(*)        AS meeting_count
			FROM meetings
			WHERE user_id = $1 AND chain_id IS NOT NULL
			GROUP BY chain_id
		) stats ON stats.chain_id = mc.id
		LEFT JOIN LATERAL (
			SELECT title
			FROM meetings
			WHERE user_id = $1 AND chain_id = mc.id
			ORDER BY start_time DESC
			LIMIT 1
		) lt ON TRUE
		WHERE mc.user_id = $1
		ORDER BY stats.last_meeting_at DESC, mc.id ASC
		LIMIT $2
	`
	rows, err := DB.Query(ctx, query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list user chains: %w", err)
	}
	defer rows.Close()

	out := make([]ChainSummary, 0, 16)
	for rows.Next() {
		var s ChainSummary
		if err := rows.Scan(&s.ID, &s.DisplayName, &s.LastMeetingAt, &s.MeetingCount); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *postgresChainRepository) GetUserRecentMeetingsForMatching(ctx context.Context, userID uuid.UUID, windowDays int, meetingStartTime time.Time, excludeID uuid.UUID) ([]CandidateMeeting, error) {
	// Window is [meetingStartTime - windowDays, meetingStartTime).
	// INTERVAL multiplication form is pgx-compatible (the `($n || ' days')`
	// concat trick refuses to encode an int arg into the text slot).
	//
	// Excludes:
	//   - the meeting being processed (excludeID)
	//   - meetings the user explicitly removed from a chain
	//     (chain_assignment_method = 'manual_removed') — those are
	//     locked out of auto-matching by roadmap policy. Filtering at
	//     the SQL layer is defence-in-depth against the matcher loop
	//     forgetting to skip them.
	query := `
		SELECT id, chain_id, title, start_time, end_time,
		       chain_assignment_method, attendees
		FROM meetings
		WHERE user_id = $1
		  AND id <> $2
		  AND start_time >= $4 - (INTERVAL '1 day' * $3)
		  AND start_time < $4
		  AND (chain_assignment_method IS NULL
		       OR chain_assignment_method <> 'manual_removed')
		ORDER BY start_time DESC
	`
	rows, err := DB.Query(ctx, query, userID, excludeID, windowDays, meetingStartTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent meetings for matching: %w", err)
	}
	defer rows.Close()

	var out []CandidateMeeting
	for rows.Next() {
		var c CandidateMeeting
		var attendeesJSON []byte
		if err := rows.Scan(&c.ID, &c.ChainID, &c.Title, &c.StartTime, &c.EndTime, &c.ChainAssignmentMethod, &attendeesJSON); err != nil {
			continue
		}
		if len(attendeesJSON) > 0 {
			_ = json.Unmarshal(attendeesJSON, &c.Attendees)
		}
		out = append(out, c)
	}
	return out, nil
}
