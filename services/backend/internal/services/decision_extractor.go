package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// DecisionExtractor handles LLM-based extraction of decisions, actions, and open topics
// Delegates to mia-search Python service for LLM orchestration (same pattern as summary)
type DecisionExtractor struct {
	repo          repository.DecisionRepository
	usageRecorder *UsageRecorder
	nlpServiceURL string
	client        *http.Client
}

func NewDecisionExtractor() *DecisionExtractor {
	nlpURL := os.Getenv("NLP_SERVICE_URL")
	if nlpURL == "" {
		nlpURL = "http://mia-search:8000"
	}
	return &DecisionExtractor{
		repo:          repository.NewDecisionRepository(),
		usageRecorder: NewUsageRecorder(),
		nlpServiceURL: nlpURL,
		client:        &http.Client{Timeout: 90 * time.Second},
	}
}

// extractionResponse represents the response from mia-search /extract endpoint
type extractionResponse struct {
	Success               bool                    `json:"success"`
	Items                 []extractedItem         `json:"items"`
	UsagePromptTokens     *int                    `json:"usage_prompt_tokens"`
	UsageCompletionTokens *int                    `json:"usage_completion_tokens"`
	UsageTotalTokens      *int                    `json:"usage_total_tokens"`
	ChunkTelemetry        []chunkTelemetryEntry   `json:"chunk_telemetry"`
	MergeTelemetry        *mergeTelemetryEntry    `json:"merge_telemetry"`
}

type chunkTelemetryEntry struct {
	ChunkIndex     int    `json:"chunk_index"`
	Status         string `json:"status"`
	LatencyMs      int    `json:"latency_ms"`
	RetryCount     int    `json:"retry_count"`
	ItemsExtracted int    `json:"items_extracted"`
}

type mergeTelemetryEntry struct {
	RawCount    int `json:"raw_count"`
	MergedCount int `json:"merged_count"`
	AddCount    int `json:"add_count"`
	UpdateCount int `json:"update_count"`
	DeleteCount int `json:"delete_count"`
	NoopCount   int `json:"noop_count"`
}

type extractedItem struct {
	Text                   string   `json:"text"`
	ItemType               string   `json:"item_type"`
	OwnerName              *string  `json:"owner_name"`
	SourceQuote            string   `json:"source_quote"`
	Context                *string  `json:"context"`
	AlternativesConsidered *string  `json:"alternatives_considered"`
	TimestampStart         *float64 `json:"timestamp_start"`
	TimestampEnd           *float64 `json:"timestamp_end"`
	Operation              string   `json:"operation"`          // ADD, UPDATE, DELETE
	UpdatedChainIdx        *int     `json:"updated_chain_idx"`  // index into chain_items for UPDATE
	DeletedChainIdx        *int     `json:"deleted_chain_idx"`  // index into chain_items for DELETE
}

// ExtractFromMeeting runs extraction via mia-search and persists results
func (e *DecisionExtractor) ExtractFromMeeting(meetingID, userID uuid.UUID, chainID *uuid.UUID) error {
	ctx := context.Background()

	slog.Info("starting decision extraction", "meeting_id", meetingID)

	// 1. Fetch corrected transcript with timing
	segments, err := e.fetchTranscriptSegments(ctx, meetingID)
	if err != nil {
		slog.Error("failed to fetch transcript for extraction", "meeting_id", meetingID, "error", err)
		return fmt.Errorf("failed to fetch transcript: %w", err)
	}

	transcript := segmentsToText(segments)
	if strings.TrimSpace(transcript) == "" {
		slog.Warn("empty transcript for extraction", "meeting_id", meetingID)
		return nil
	}

	// 2. Fetch user-edited item texts for idempotency + chain items for merge
	confirmedTexts, editErr := e.repo.GetUserEditedItemTexts(ctx, meetingID)
	if editErr != nil {
		slog.Warn("failed to get user-edited texts, continuing without exclusion list", "meeting_id", meetingID, "error", editErr)
	}

	var chainItems []map[string]interface{}
	if chainID != nil {
		chainConfirmed, chainErr := e.repo.GetConfirmedItemsByChain(ctx, *chainID)
		if chainErr != nil {
			slog.Warn("failed to get chain items, continuing without chain context", "chain_id", *chainID, "error", chainErr)
		}
		for _, ci := range chainConfirmed {
			chainItems = append(chainItems, map[string]interface{}{
				"text":      ci.Text,
				"item_type": ci.ItemType,
				"owner_name": ci.OwnerName,
				"id":        ci.ID.String(),
			})
		}
	}

	// 3. Call mia-search extraction endpoint
	extractResp, err := e.callExtractionService(meetingID, transcript, segments, confirmedTexts, chainItems)
	if err != nil {
		// Record failure in usage_logs so metrics can track extraction_success_rate
		e.usageRecorder.Record(userID, nil, &meetingID, "decision_extraction", "openai", "gpt-5.4",
			0, 0, 0, map[string]interface{}{"status": "failed", "error": err.Error()})
		slog.Error("extraction service call failed", "meeting_id", meetingID, "error", err)
		return fmt.Errorf("extraction failed: %w", err)
	}
	items := extractResp.Items

	// 4. Record usage with full telemetry metadata
	metadata := map[string]interface{}{
		"status":      "success",
		"items_count": len(items),
	}
	if extractResp.MergeTelemetry != nil {
		metadata["merge"] = extractResp.MergeTelemetry
	}
	if len(extractResp.ChunkTelemetry) > 0 {
		metadata["chunk_telemetry"] = extractResp.ChunkTelemetry
		failedChunks := 0
		for _, ct := range extractResp.ChunkTelemetry {
			if ct.Status != "success" {
				failedChunks++
			}
		}
		metadata["chunks"] = len(extractResp.ChunkTelemetry)
		metadata["failed_chunks"] = failedChunks
	}
	e.usageRecorder.Record(userID, nil, &meetingID, "decision_extraction", "openai", "gpt-5.4",
		derefInt(extractResp.UsagePromptTokens), derefInt(extractResp.UsageCompletionTokens), 0, metadata)

	// 5. Handle merge operations
	// First pass: collect operations and resolve DELETE/UPDATE conflicts
	// If same chain_idx has both DELETE and UPDATE → UPDATE wins (old decision evolved, not just cancelled)
	updateIndices := make(map[int]bool)
	for _, item := range items {
		if item.Operation == "UPDATE" && item.UpdatedChainIdx != nil {
			updateIndices[*item.UpdatedChainIdx] = true
		}
	}

	var addItems []models.DecisionItem
	updateCount := 0
	deleteCount := 0

	for _, item := range items {
		switch item.Operation {
		case "UPDATE":
			if item.UpdatedChainIdx != nil && *item.UpdatedChainIdx < len(chainItems) {
				chainItemID, ok := chainItems[*item.UpdatedChainIdx]["id"].(string)
				if !ok {
					slog.Error("invalid chain item ID type for UPDATE", "index", *item.UpdatedChainIdx)
					continue
				}
				parsedID, parseErr := uuid.Parse(chainItemID)
				if parseErr != nil {
					slog.Error("invalid chain item ID for UPDATE", "raw_id", chainItemID, "error", parseErr)
					continue
				}
				if err := e.repo.UpdateDecisionItemFull(ctx, parsedID, item.Text, item.ItemType, item.Context, item.AlternativesConsidered, &item.SourceQuote, item.TimestampStart, item.TimestampEnd); err != nil {
					slog.Error("failed to UPDATE chain item", "chain_item_id", parsedID, "error", err)
					continue
				}
				updateCount++
				slog.Info("updated chain item", "chain_item_id", parsedID, "new_text", item.Text)
			}
		case "DELETE":
			if item.DeletedChainIdx != nil && *item.DeletedChainIdx < len(chainItems) {
				// Conflict resolution: if same index also has UPDATE, skip DELETE (UPDATE wins)
				if updateIndices[*item.DeletedChainIdx] {
					slog.Info("DELETE/UPDATE conflict on same chain index, UPDATE wins",
						"chain_idx", *item.DeletedChainIdx, "delete_reason", item.Text)
					continue
				}
				chainItemID, ok := chainItems[*item.DeletedChainIdx]["id"].(string)
				if !ok {
					slog.Error("invalid chain item ID type for DELETE", "index", *item.DeletedChainIdx)
					continue
				}
				parsedID, parseErr := uuid.Parse(chainItemID)
				if parseErr != nil {
					slog.Error("invalid chain item ID for DELETE", "raw_id", chainItemID, "error", parseErr)
					continue
				}
				if err := e.repo.UpdateDecisionItemStatus(ctx, parsedID, "dismissed"); err != nil {
					slog.Error("failed to DELETE (dismiss) chain item", "chain_item_id", parsedID, "error", err)
					continue
				}
				deleteCount++
				slog.Info("dismissed chain item", "chain_item_id", parsedID, "reason", item.Text)
			}
		default: // ADD
			now := time.Now()
			addItems = append(addItems, models.DecisionItem{
				ID:                     uuid.New(),
				MeetingID:              meetingID,
				ChainID:                chainID,
				UserID:                 userID,
				ItemType:               item.ItemType,
				Text:                   item.Text,
				OwnerName:              item.OwnerName,
				SourceQuote:            &item.SourceQuote,
				Context:                item.Context,
				AlternativesConsidered: item.AlternativesConsidered,
				DecisionTimestampStart: item.TimestampStart,
				DecisionTimestampEnd:   item.TimestampEnd,
				Status:                 "confirmed",
				ConfirmedAt:            &now,
			})
		}
	}

	// 6. Atomic replace: delete unedited items + re-check exclusions + insert new items in one transaction
	if err := e.repo.ReplaceUneditedItems(ctx, meetingID, addItems); err != nil {
		slog.Error("failed to replace unedited items", "meeting_id", meetingID, "error", err)
		return fmt.Errorf("failed to replace items: %w", err)
	}

	// 7. Mark meeting as processed
	repository.DB.Exec(ctx, `UPDATE meetings SET processed_at = NOW() WHERE id = $1`, meetingID)

	slog.Info("decision extraction completed",
		"meeting_id", meetingID,
		"added", len(addItems),
		"updated", updateCount,
		"deleted", deleteCount,
		"decisions", countByType(addItems, "decision"),
		"actions", countByType(addItems, "action_item"),
		"open_topics", countByType(addItems, "open_topic"),
	)

	return nil
}

// callExtractionService calls mia-search /extract endpoint
func (e *DecisionExtractor) callExtractionService(meetingID uuid.UUID, transcript string, segments []transcriptSegment, confirmedTexts []string, chainItems []map[string]interface{}) (*extractionResponse, error) {
	reqBody := map[string]interface{}{
		"meeting_id": meetingID.String(),
		"transcript": transcript,
		"segments":   segments,
		"language":   "tr",
	}
	if len(confirmedTexts) > 0 {
		reqBody["confirmed_items"] = confirmedTexts
	}
	if len(chainItems) > 0 {
		reqBody["chain_items"] = chainItems
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, e.nlpServiceURL+"/extract", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := SetInternalServiceAuthHeader(req); err != nil {
		return nil, fmt.Errorf("failed to set auth header: %w", err)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extraction service request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("extraction service returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var extractResp extractionResponse
	if err := json.Unmarshal(bodyBytes, &extractResp); err != nil {
		return nil, fmt.Errorf("failed to parse extraction response: %w", err)
	}

	return &extractResp, nil
}

// transcriptSegment represents a single speaker-attributed line with timing
type transcriptSegment struct {
	Speaker   string   `json:"speaker"`
	Text      string   `json:"text"`
	StartTime *float64 `json:"start_time"`
	EndTime   *float64 `json:"end_time"`
}

// fetchTranscriptSegments gets corrected transcript as structured segments with timing
func (e *DecisionExtractor) fetchTranscriptSegments(ctx context.Context, meetingID uuid.UUID) ([]transcriptSegment, error) {
	// Try chat blocks first (corrected), fall back to raw segments
	rows, err := repository.DB.Query(ctx, `
		SELECT speaker, text, start_time, end_time FROM transcript_chat_blocks
		WHERE meeting_id = $1 AND is_noise = false
		ORDER BY start_time ASC
	`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var segments []transcriptSegment
	for rows.Next() {
		var seg transcriptSegment
		if err := rows.Scan(&seg.Speaker, &seg.Text, &seg.StartTime, &seg.EndTime); err != nil {
			continue
		}
		segments = append(segments, seg)
	}

	if len(segments) == 0 {
		// Fallback to raw segments
		rows2, err := repository.DB.Query(ctx, `
			SELECT speaker, text, start_time, end_time FROM transcript_segments
			WHERE meeting_id = $1
			ORDER BY start_time ASC
		`, meetingID)
		if err != nil {
			return nil, err
		}
		defer rows2.Close()

		for rows2.Next() {
			var seg transcriptSegment
			if err := rows2.Scan(&seg.Speaker, &seg.Text, &seg.StartTime, &seg.EndTime); err != nil {
				continue
			}
			if seg.Speaker == "" {
				seg.Speaker = "Konuşmacı"
			}
			segments = append(segments, seg)
		}
	}

	return segments, nil
}

// segmentsToText converts structured segments to flat transcript text for the LLM prompt
func segmentsToText(segments []transcriptSegment) string {
	var parts []string
	for _, seg := range segments {
		parts = append(parts, fmt.Sprintf("[%s]: %s", seg.Speaker, seg.Text))
	}
	return strings.Join(parts, "\n")
}

// Helper functions

func isValidItemType(t string) bool {
	return t == "decision" || t == "action_item" || t == "open_topic"
}

func countByType(items []models.DecisionItem, itemType string) int {
	count := 0
	for _, item := range items {
		if item.ItemType == itemType {
			count++
		}
	}
	return count
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return defaultVal
}
