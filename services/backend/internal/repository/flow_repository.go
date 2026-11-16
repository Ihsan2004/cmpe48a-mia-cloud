package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// FlowMeeting is a lightweight meeting projection for the flow visualization endpoint.
type FlowMeeting struct {
	ID            uuid.UUID        `json:"id"`
	Title         string           `json:"title"`
	StartTime     time.Time        `json:"start_time"`
	EndTime       time.Time        `json:"end_time"`
	ChainID       *uuid.UUID       `json:"chain_id"`
	Attendees     []models.Attendee `json:"attendees"`
	BotStatus     *string          `json:"bot_status"`
	Description   *string          `json:"description"`
	DecisionCount int              `json:"decision_count"`
}

// FlowChain is a chain summary with its associated meeting IDs.
type FlowChain struct {
	ID               uuid.UUID `json:"id"`
	ChainType        string    `json:"chain_type"`
	DisplayName      *string   `json:"display_name"`
	CalendarSeriesID *string   `json:"calendar_series_id"`
	MeetingIDs       []string  `json:"meeting_ids"`
}

// FlowDecision is a lightweight decision item for the flow visualization endpoint.
type FlowDecision struct {
	ID        uuid.UUID  `json:"id"`
	MeetingID uuid.UUID  `json:"meeting_id"`
	ChainID   *uuid.UUID `json:"chain_id"`
	ItemType  string     `json:"item_type"`
	Text      string     `json:"text"`
	Status    string     `json:"status"`
	OwnerName *string    `json:"owner_name"`
}

// FlowData is the aggregated response for the flow visualization endpoint.
type FlowData struct {
	Meetings  []FlowMeeting  `json:"meetings"`
	Chains    []FlowChain    `json:"chains"`
	Decisions []FlowDecision `json:"decisions"`
}

// FlowRepository defines data access methods for the flow visualization endpoint.
type FlowRepository interface {
	GetFlowData(ctx context.Context, userID uuid.UUID, userEmail string, start, end time.Time, scope string) (*FlowData, error)
}

type postgresFlowRepository struct{}

// NewFlowRepository creates a new FlowRepository backed by PostgreSQL.
func NewFlowRepository() FlowRepository {
	return &postgresFlowRepository{}
}

// GetFlowData fetches meetings, chains, and decisions for the flow visualization.
// Uses three sequential queries to avoid cartesian product JOINs.
func (r *postgresFlowRepository) GetFlowData(ctx context.Context, userID uuid.UUID, userEmail string, start, end time.Time, scope string) (*FlowData, error) {
	// Query 1: Meetings within date range, filtered by ownership/attendance
	meetingQuery := `
		SELECT m.id, m.title, m.start_time, m.end_time, m.chain_id, m.attendees, m.bot_status, m.description
		FROM meetings m
		WHERE m.start_time >= $1 AND m.end_time <= $2
		AND (
			m.user_id = $3
			OR ($4 <> '' AND m.attendees @> ('[{"email": "' || $4 || '"}]')::jsonb)
		)
		ORDER BY m.start_time ASC
	`

	// When scope is "my", pass empty string so the OR clause is skipped
	emailParam := ""
	if scope == "team" {
		emailParam = userEmail
	}

	rows, err := DB.Query(ctx, meetingQuery, start, end, userID, emailParam)
	if err != nil {
		return nil, fmt.Errorf("failed to query meetings: %w", err)
	}
	defer rows.Close()

	var meetings []FlowMeeting
	var meetingIDs []uuid.UUID
	chainIDSet := make(map[uuid.UUID]bool)

	for rows.Next() {
		var m FlowMeeting
		var attendeesJSON []byte

		if err := rows.Scan(
			&m.ID, &m.Title, &m.StartTime, &m.EndTime,
			&m.ChainID, &attendeesJSON, &m.BotStatus, &m.Description,
		); err != nil {
			return nil, fmt.Errorf("failed to scan meeting: %w", err)
		}

		// Parse attendees JSONB; handle null/empty gracefully
		if len(attendeesJSON) > 0 {
			if err := json.Unmarshal(attendeesJSON, &m.Attendees); err != nil {
				m.Attendees = []models.Attendee{}
			}
		} else {
			m.Attendees = []models.Attendee{}
		}

		meetings = append(meetings, m)
		meetingIDs = append(meetingIDs, m.ID)

		if m.ChainID != nil {
			chainIDSet[*m.ChainID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating meetings: %w", err)
	}

	// Initialize empty slices for JSON serialization (avoid null)
	if meetings == nil {
		meetings = []FlowMeeting{}
	}

	// Query 2: Chains referenced by the meetings
	var chains []FlowChain
	if len(chainIDSet) > 0 {
		chainIDs := make([]uuid.UUID, 0, len(chainIDSet))
		for id := range chainIDSet {
			chainIDs = append(chainIDs, id)
		}

		chainQuery := `
			SELECT id, chain_type, display_name, calendar_series_id
			FROM meeting_chains
			WHERE id = ANY($1)
		`
		chainRows, err := DB.Query(ctx, chainQuery, chainIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to query chains: %w", err)
		}
		defer chainRows.Close()

		for chainRows.Next() {
			var ch FlowChain
			if err := chainRows.Scan(&ch.ID, &ch.ChainType, &ch.DisplayName, &ch.CalendarSeriesID); err != nil {
				return nil, fmt.Errorf("failed to scan chain: %w", err)
			}
			// Populate meeting IDs for this chain
			ch.MeetingIDs = []string{}
			for _, m := range meetings {
				if m.ChainID != nil && *m.ChainID == ch.ID {
					ch.MeetingIDs = append(ch.MeetingIDs, m.ID.String())
				}
			}
			chains = append(chains, ch)
		}
		if err := chainRows.Err(); err != nil {
			return nil, fmt.Errorf("error iterating chains: %w", err)
		}
	}
	if chains == nil {
		chains = []FlowChain{}
	}

	// Query 3: Decision items for the fetched meetings
	var decisions []FlowDecision
	if len(meetingIDs) > 0 {
		decisionQuery := `
			SELECT id, meeting_id, chain_id, item_type, text, status, owner_name
			FROM decision_items
			WHERE meeting_id = ANY($1)
			ORDER BY created_at ASC
		`
		decRows, err := DB.Query(ctx, decisionQuery, meetingIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to query decisions: %w", err)
		}
		defer decRows.Close()

		// Count decisions per meeting for DecisionCount
		decisionCountMap := make(map[uuid.UUID]int)

		for decRows.Next() {
			var d FlowDecision
			if err := decRows.Scan(
				&d.ID, &d.MeetingID, &d.ChainID, &d.ItemType,
				&d.Text, &d.Status, &d.OwnerName,
			); err != nil {
				return nil, fmt.Errorf("failed to scan decision: %w", err)
			}
			decisions = append(decisions, d)
			decisionCountMap[d.MeetingID]++
		}
		if err := decRows.Err(); err != nil {
			return nil, fmt.Errorf("error iterating decisions: %w", err)
		}

		// Set DecisionCount on each meeting
		for i := range meetings {
			if count, ok := decisionCountMap[meetings[i].ID]; ok {
				meetings[i].DecisionCount = count
			}
		}
	}
	if decisions == nil {
		decisions = []FlowDecision{}
	}

	return &FlowData{
		Meetings:  meetings,
		Chains:    chains,
		Decisions: decisions,
	}, nil
}
