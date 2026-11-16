package services

import (
	"context"
	"fmt"
	"log/slog"

	"mia-website/backend/internal/repository"
)

// MVPMetrics holds all pilot readiness metrics
type MVPMetrics struct {
	// Review metrics
	TotalMeetingsProcessed int     `json:"total_meetings_processed"`
	MeetingsWithReview     int     `json:"meetings_with_review"`
	ReviewCompletionRate   float64 `json:"review_completion_rate"`
	AvgConfirmedItems      float64 `json:"avg_confirmed_items_per_meeting"`

	// Brief metrics
	TotalBriefsGenerated int     `json:"total_briefs_generated"`
	BriefsOpened         int     `json:"briefs_opened"`
	BriefOpenRate        float64 `json:"brief_open_rate"`
	BriefEligibleCount   int     `json:"brief_eligible_meetings"`
	BriefEligibilityRate float64 `json:"brief_eligibility_rate"`

	// Feedback metrics
	TotalFeedback    int     `json:"total_feedback"`
	UsefulCount      int     `json:"useful_count"`
	NotUsefulCount   int     `json:"not_useful_count"`
	UsefulnessScore  float64 `json:"usefulness_score"`

	// Chain metrics
	TotalChains       int     `json:"total_chains"`
	ChainOverrideCount int    `json:"chain_override_count"`

	// Notes metrics
	MeetingsWithNotes int     `json:"meetings_with_notes"`
	NotesUsageRate    float64 `json:"notes_usage_rate"`

	// Item breakdown
	TotalDecisions  int `json:"total_decisions"`
	TotalActions    int `json:"total_actions"`
	TotalOpenTopics int `json:"total_open_topics"`
	TotalStaleItems int `json:"total_stale_items"`

	// Extraction metrics
	ExtractionCount       int     `json:"extraction_count"`
	ExtractionSuccessRate float64 `json:"extraction_success_rate"`
	AvgItemsPerExtraction float64 `json:"avg_items_per_extraction"`
	UpdateRate            float64 `json:"update_rate"`          // UPDATE operations / total merge operations
	ChunkFailureRate      float64 `json:"chunk_failure_rate"`   // failed chunks / total chunks
}

// MetricsService calculates MVP success metrics
type MetricsService struct{}

func NewMetricsService() *MetricsService {
	return &MetricsService{}
}

// CalculateMetrics queries DB and calculates all MVP metrics
func (s *MetricsService) CalculateMetrics(ctx context.Context) (*MVPMetrics, error) {
	m := &MVPMetrics{}

	// Review completion: meetings with at least 1 confirmed/completed/dismissed item
	err := repository.DB.QueryRow(ctx, `
		SELECT
			COUNT(DISTINCT m.id) as total_processed,
			COUNT(DISTINCT CASE WHEN di.id IS NOT NULL THEN m.id END) as with_review
		FROM meetings m
		LEFT JOIN decision_items di ON di.meeting_id = m.id AND di.status IN ('confirmed', 'completed', 'dismissed')
		WHERE m.processed_at IS NOT NULL
	`).Scan(&m.TotalMeetingsProcessed, &m.MeetingsWithReview)
	if err != nil {
		slog.Warn("failed to calculate review metrics", "error", err)
	}

	if m.TotalMeetingsProcessed > 0 {
		m.ReviewCompletionRate = float64(m.MeetingsWithReview) / float64(m.TotalMeetingsProcessed)
	}

	// Average confirmed items per reviewed meeting
	err = repository.DB.QueryRow(ctx, `
		SELECT COALESCE(AVG(item_count), 0) FROM (
			SELECT COUNT(*) as item_count
			FROM decision_items
			WHERE status IN ('confirmed', 'completed')
			GROUP BY meeting_id
		) sub
	`).Scan(&m.AvgConfirmedItems)
	if err != nil {
		slog.Warn("failed to calculate avg confirmed items", "error", err)
	}

	// Brief metrics
	err = repository.DB.QueryRow(ctx, `
		SELECT
			COUNT(*) as total,
			COUNT(CASE WHEN opened_at IS NOT NULL THEN 1 END) as opened
		FROM meeting_briefs
	`).Scan(&m.TotalBriefsGenerated, &m.BriefsOpened)
	if err != nil {
		slog.Warn("failed to calculate brief metrics", "error", err)
	}

	if m.TotalBriefsGenerated > 0 {
		m.BriefOpenRate = float64(m.BriefsOpened) / float64(m.TotalBriefsGenerated)
	}

	// Brief eligibility: meetings with chain_id
	err = repository.DB.QueryRow(ctx, `
		SELECT
			COUNT(*) as eligible
		FROM meetings
		WHERE chain_id IS NOT NULL AND start_time > NOW() - INTERVAL '30 days'
	`).Scan(&m.BriefEligibleCount)
	if err != nil {
		slog.Warn("failed to calculate brief eligibility", "error", err)
	}

	var totalMeetingsLast30 int
	repository.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM meetings WHERE start_time > NOW() - INTERVAL '30 days'
	`).Scan(&totalMeetingsLast30)

	if totalMeetingsLast30 > 0 {
		m.BriefEligibilityRate = float64(m.BriefEligibleCount) / float64(totalMeetingsLast30)
	}

	// Feedback
	err = repository.DB.QueryRow(ctx, `
		SELECT
			COUNT(*) as total,
			COUNT(CASE WHEN feedback = 'useful' THEN 1 END) as useful,
			COUNT(CASE WHEN feedback = 'not_useful' THEN 1 END) as not_useful
		FROM brief_feedback
	`).Scan(&m.TotalFeedback, &m.UsefulCount, &m.NotUsefulCount)
	if err != nil {
		slog.Warn("failed to calculate feedback metrics", "error", err)
	}

	if m.TotalFeedback > 0 {
		m.UsefulnessScore = float64(m.UsefulCount) / float64(m.TotalFeedback)
	}

	// Chains
	repository.DB.QueryRow(ctx, `SELECT COUNT(*) FROM meeting_chains`).Scan(&m.TotalChains)

	// Chain overrides (tracked via usage_logs)
	repository.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM usage_logs WHERE feature = 'chain_override'
	`).Scan(&m.ChainOverrideCount)

	// Notes usage
	repository.DB.QueryRow(ctx, `
		SELECT COUNT(DISTINCT meeting_id) FROM meeting_notes WHERE content IS NOT NULL AND content != ''
	`).Scan(&m.MeetingsWithNotes)

	if m.TotalMeetingsProcessed > 0 {
		m.NotesUsageRate = float64(m.MeetingsWithNotes) / float64(m.TotalMeetingsProcessed)
	}

	// Item breakdown
	repository.DB.QueryRow(ctx, `
		SELECT
			COUNT(CASE WHEN item_type = 'decision' AND status IN ('confirmed', 'completed') THEN 1 END),
			COUNT(CASE WHEN item_type = 'action_item' AND status IN ('confirmed', 'completed') THEN 1 END),
			COUNT(CASE WHEN item_type = 'open_topic' AND status IN ('confirmed', 'completed') THEN 1 END)
		FROM decision_items
	`).Scan(&m.TotalDecisions, &m.TotalActions, &m.TotalOpenTopics)

	// Stale items (confirmed, no activity for 14+ days — uses updated_at like brief generator)
	repository.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM decision_items
		WHERE status = 'confirmed'
		  AND COALESCE(updated_at, confirmed_at) < NOW() - INTERVAL '14 days'
	`).Scan(&m.TotalStaleItems)

	// Extraction metrics (from usage_logs metadata)
	var totalExtractions, successfulExtractions int
	repository.DB.QueryRow(ctx, `
		SELECT
			COUNT(*) as total,
			COUNT(CASE WHEN metadata->>'status' = 'success' THEN 1 END) as successful
		FROM usage_logs
		WHERE feature = 'decision_extraction'
	`).Scan(&totalExtractions, &successfulExtractions)
	m.ExtractionCount = totalExtractions
	if totalExtractions > 0 {
		m.ExtractionSuccessRate = float64(successfulExtractions) / float64(totalExtractions)
	}

	// Avg items per extraction (from metadata items_count, not from current table state)
	var avgItems float64
	repository.DB.QueryRow(ctx, `
		SELECT COALESCE(AVG((metadata->>'items_count')::int), 0)
		FROM usage_logs
		WHERE feature = 'decision_extraction' AND metadata->>'status' = 'success'
		  AND metadata->>'items_count' IS NOT NULL
	`).Scan(&avgItems)
	m.AvgItemsPerExtraction = avgItems

	// Update rate: UPDATE operations / total merge operations (from metadata)
	var totalMergeOps, updateOps int
	repository.DB.QueryRow(ctx, `
		SELECT
			COALESCE(SUM((metadata->'merge'->>'merged_count')::int), 0),
			COALESCE(SUM((metadata->'merge'->>'update_count')::int), 0)
		FROM usage_logs
		WHERE feature = 'decision_extraction' AND metadata->>'status' = 'success'
		  AND metadata->'merge' IS NOT NULL
	`).Scan(&totalMergeOps, &updateOps)
	if totalMergeOps > 0 {
		m.UpdateRate = float64(updateOps) / float64(totalMergeOps)
	}

	// Chunk failure rate (from metadata)
	var totalChunks, failedChunks int
	repository.DB.QueryRow(ctx, `
		SELECT
			COALESCE(SUM((metadata->>'chunks')::int), 0),
			COALESCE(SUM((metadata->>'failed_chunks')::int), 0)
		FROM usage_logs
		WHERE feature = 'decision_extraction' AND metadata->>'status' = 'success'
		  AND metadata->>'chunks' IS NOT NULL
	`).Scan(&totalChunks, &failedChunks)
	if totalChunks > 0 {
		m.ChunkFailureRate = float64(failedChunks) / float64(totalChunks)
	}

	return m, nil
}

// FormatMetricsSummary returns a human-readable summary
func FormatMetricsSummary(m *MVPMetrics) string {
	return fmt.Sprintf(
		"Review: %.0f%% (%d/%d) | Brief Open: %.0f%% (%d/%d) | Useful: %.0f%% (%d/%d) | Notes: %.0f%%",
		m.ReviewCompletionRate*100, m.MeetingsWithReview, m.TotalMeetingsProcessed,
		m.BriefOpenRate*100, m.BriefsOpened, m.TotalBriefsGenerated,
		m.UsefulnessScore*100, m.UsefulCount, m.TotalFeedback,
		m.NotesUsageRate*100,
	)
}
