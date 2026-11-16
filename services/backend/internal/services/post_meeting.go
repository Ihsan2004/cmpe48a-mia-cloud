package services

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// Error content patterns that indicate a failed summary (Turkish error markers from Python service)
var errorContentPatterns = []string{
	"işlenemedi",
	"oluşturulamadı",
	"Hata:",
	"[Chunk",
	"segment (Hata",
}

var (
	// ErrPostMeetingDeferred means processing should be retried later (transcript not ready yet).
	ErrPostMeetingDeferred = errors.New("post-meeting deferred")
	// ErrPostMeetingAlreadyProcessing means another worker already owns the processing lock.
	ErrPostMeetingAlreadyProcessing = errors.New("post-meeting already processing")
)

// IsSummaryContentValid checks if the summary content is a real summary (not an error placeholder)
func IsSummaryContentValid(content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	// Check for known error patterns
	for _, pattern := range errorContentPatterns {
		if strings.Contains(content, pattern) {
			return false
		}
	}
	// Minimum length check - a valid summary should be at least ~50 chars
	if len(strings.TrimSpace(content)) < 50 {
		return false
	}
	return true
}

// PostMeetingService handles post-meeting processing: summarization
type PostMeetingService struct {
	nlpServiceURL     string
	correctionService *CorrectionService
	client            *http.Client
	usageRecorder     *UsageRecorder
}

// ActionItemStructured represents a structured action item from NLP service (Faz 2)
type ActionItemStructured struct {
	Description     string  `json:"description"`
	AssigneeName    *string `json:"assignee_name"`
	Priority        *string `json:"priority,omitempty"` // low | medium | high
	CommitmentLevel *string `json:"commitment_level"`   // committed | candidate
	Deadline        *string `json:"deadline"`           // ISO date
	Context         *string `json:"context"`
	TopicIndices    []int   `json:"topic_indices,omitempty"` // Faz 3.2: Matched topic indices
}

// KeyTopicStructured represents a structured key topic from NLP service.
type KeyTopicStructured struct {
	Topic      string `json:"topic"`
	TopicOrder int    `json:"topic_order"` // Stable chronological order (NOT NULL in schema)
}

type ChunkTelemetry struct {
	ChunkIndex   int     `json:"chunk_index"`
	Status       string  `json:"status"`
	LatencyMs    int     `json:"latency_ms"`
	RetryCount   int     `json:"retry_count"`
	ErrorCode    *string `json:"error_code,omitempty"`
	ModelUsed    *string `json:"model_used,omitempty"`
	AttemptStage *string `json:"attempt_stage,omitempty"`
}

// SummarizeResponse represents the structured response from the NLP service
type SummarizeResponse struct {
	Success               bool                   `json:"success"`
	Summary               string                 `json:"summary"`
	TLDR                  string                 `json:"email_draft"`
	KeyTopics             []string               `json:"key_topics"`              // Legacy string list
	KeyTopicsStructured   []KeyTopicStructured   `json:"key_topics_structured"`   // Faz 3 structured
	ActionItems           []string               `json:"action_items"`            // Legacy string list
	ActionItemsStructured []ActionItemStructured `json:"action_items_structured"` // Faz 2 structured
	Participants          []string               `json:"participants"`
	TotalChunks           int                    `json:"total_chunks"`
	ProcessingStatus      string                 `json:"processing_status,omitempty"`
	CoverageRatio         *float64               `json:"coverage_ratio,omitempty"`
	SuccessfulChunks      int                    `json:"successful_chunks,omitempty"`
	FailedChunks          int                    `json:"failed_chunks,omitempty"`
	ChunkTelemetry        []ChunkTelemetry       `json:"chunk_telemetry,omitempty"`
	Warnings              []string               `json:"warnings,omitempty"`
	SummarySource         string                 `json:"summary_source,omitempty"`
	Error                 string                 `json:"error,omitempty"`
	Detail                string                 `json:"detail,omitempty"`
	// Usage tracking fields
	UsagePromptTokens     int `json:"usage_prompt_tokens,omitempty"`
	UsageCompletionTokens int `json:"usage_completion_tokens,omitempty"`
	UsageTotalTokens      int `json:"usage_total_tokens,omitempty"`
}

// MeetingInfo contains meeting details needed for post-processing.
// ChainID was added so the post-meeting hook can skip the Stage 3
// goroutine spawn for meetings that are already chained (the common
// case for recurring/series meetings) without paying for a second DB
// fetch.
type MeetingInfo struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	Title          string
	StartTime      time.Time
	EndTime        time.Time
	MeetingURL     *string
	Attendees      []string // Email addresses
	OrganizerEmail *string
	ChainID        *uuid.UUID
}

// NewPostMeetingService creates a new PostMeetingService
func NewPostMeetingService() *PostMeetingService {
	// Configuration constants
	const defaultNLPServiceURL = "http://mia-search:8000"
	const defaultTimeout = 300 * time.Second

	nlpURL := os.Getenv("NLP_SERVICE_URL")
	if nlpURL == "" {
		nlpURL = defaultNLPServiceURL
	}

	return &PostMeetingService{
		nlpServiceURL:     nlpURL,
		correctionService: NewCorrectionService(),
		client: &http.Client{
			Timeout: defaultTimeout,
		},
		usageRecorder: NewUsageRecorder(),
	}
}

// ProcessMeetingCompletion is the main entry point for post-meeting processing
// This is called asynchronously (fire-and-forget) when a meeting ends
func (s *PostMeetingService) ProcessMeetingCompletion(meetingID *uuid.UUID, meetingURL string) error {
	meetingIDStr := ""
	if meetingID != nil {
		meetingIDStr = meetingID.String()
	}

	slog.Info("starting post-meeting processing", "meeting_id", meetingIDStr)

	// Fetch meeting info
	var meeting *MeetingInfo
	var fetchErr error

	// Intelligent meeting lookup strategy
	if meetingID != nil {
		meeting, fetchErr = s.fetchMeetingByID(*meetingID)
		if fetchErr != nil && meetingURL != "" {
			slog.Warn("meeting not found by ID, attempting fallback lookup by URL", "meeting_id", meetingID.String(), "meeting_url", meetingURL)
			meeting, fetchErr = s.fetchMeetingByURL(meetingURL)
		}
	} else if meetingURL != "" {
		meeting, fetchErr = s.fetchMeetingByURL(meetingURL)
	} else {
		slog.Error("invalid arguments: both meetingID and meetingURL are missing")
		return fmt.Errorf("meeting_id and meeting_url are both missing")
	}

	if fetchErr != nil {
		slog.Error("failed to resolve meeting info", "meeting_id", meetingIDStr, "error", fetchErr)
		return fmt.Errorf("failed to resolve meeting info: %w", fetchErr)
	}

	if meeting == nil {
		slog.Error("meeting not found", "meeting_id", meetingIDStr, "meeting_url", meetingURL)
		return fmt.Errorf("meeting not found for lookup inputs (id=%v, url=%s)", meetingID, meetingURL)
	}

	slog.Info("processing meeting", "meeting_id", meeting.ID, "title", meeting.Title)

	// === REFINED COMPLETION WORKFLOW ===

	// Phase 0.1: Transcript Size Validation - Prevent processing meetings with anomalous chunk counts
	if err := s.validateTranscriptSize(meeting.ID); err != nil {
		slog.Error("transcript size validation failed", "meeting_id", meeting.ID, "error", err)
		s.LogSummaryError(meeting.ID, "data_anomaly", "Transcript size anomaly", err.Error(), "validation")
		s.markSummaryFailed(meeting.ID)
		return fmt.Errorf("transcript size validation failed: %w", err)
	}

	// Phase 0.2: Wait until transcript ingestion stabilizes (avoid tail-loss race).
	ready, reason, err := s.isTranscriptReadyForSummary(meeting.ID)
	if err != nil {
		slog.Error("transcript readiness check failed", "meeting_id", meeting.ID, "error", err)
		return fmt.Errorf("transcript readiness check failed: %w", err)
	}
	if !ready {
		// Bot finished but no transcript segments — permanently skip (no point retrying)
		if reason == "no_transcript" {
			slog.Warn("no transcript segments for meeting, marking as no_transcript", "meeting_id", meeting.ID)
			s.LogSummaryError(meeting.ID, "no_transcript", "Bot completed but no transcript segments recorded", "", "validation")
			s.markSummaryFailed(meeting.ID)
			return fmt.Errorf("no transcript segments recorded")
		}
		slog.Info("transcript not stable yet, deferring summary", "meeting_id", meeting.ID, "reason", reason)
		return fmt.Errorf("%w: %s", ErrPostMeetingDeferred, reason)
	}

	// Phase 0.3: Acquire Processing Lock (only after transcript is ready)
	// We must ensure we don't double-process with Scheduler or other webhooks
	if locked := s.acquireProcessingLock(meeting.ID); !locked {
		slog.Warn("meeting is already being processed, skipping", "meeting_id", meeting.ID)
		return ErrPostMeetingAlreadyProcessing
	}

	forceRawSegments := false
	sourceReason := ""

	// Phase 1: Deterministic input dependency - finalize corrected chat blocks before summary generation.
	// If correction fails, continue with raw-segment fallback instead of failing the whole summary run.
	if err := s.correctionService.ProcessMeetingTranscript(meeting.ID); err != nil {
		slog.Warn("transcript correction failed, forcing raw fallback", "meeting_id", meeting.ID, "error", err)
		s.LogSummaryError(meeting.ID, "correction_error", "Transcript correction failed, raw fallback activated", err.Error(), "correction")
		forceRawSegments = true
		sourceReason = "correction_failed"
	}

	// Phase 2: Parallel AI Processing — Summary + Extraction run concurrently
	// Each persists independently. If one fails, the other still saves.

	type summaryResult struct {
		response *SummarizeResponse
		err      error
	}
	type extractionResult struct {
		err error
	}

	summaryCh := make(chan summaryResult, 1)
	extractionCh := make(chan extractionResult, 1)

	// Goroutine 1: Summary Generation (via mia-search)
	go func() {
		mURL := ""
		if meeting.MeetingURL != nil {
			mURL = *meeting.MeetingURL
		}
		botLanguage := s.fetchBotLanguage(meeting.UserID)

		resp, err := s.generateSummary(meeting.ID, mURL, forceRawSegments, sourceReason, botLanguage)
		summaryCh <- summaryResult{response: resp, err: err}
	}()

	// Goroutine 2: Decision/Action/Topic Extraction (direct OpenAI)
	go func() {
		extractor := NewDecisionExtractor()
		// Get chain_id from meeting if available
		var chainID *uuid.UUID
		repository.DB.QueryRow(context.Background(),
			`SELECT chain_id FROM meetings WHERE id = $1`, meeting.ID,
		).Scan(&chainID)

		err := extractor.ExtractFromMeeting(meeting.ID, meeting.UserID, chainID)
		extractionCh <- extractionResult{err: err}
	}()

	// Wait for both to complete
	summaryRes := <-summaryCh
	extractionRes := <-extractionCh

	// Handle extraction result (independent — doesn't block summary)
	if extractionRes.err != nil {
		slog.Error("extraction failed (non-blocking)", "meeting_id", meeting.ID, "error", extractionRes.err)
		s.LogSummaryError(meeting.ID, "extraction_error", "Decision extraction failed", extractionRes.err.Error(), "extraction")
	} else {
		slog.Info("extraction completed successfully", "meeting_id", meeting.ID)

		// Chain detection v3 Stage 3 (Phase 6 cutover — auto-attach
		// at cosine ≥0.80, suggest at 0.70-0.80). Runs AFTER
		// successful decision extraction so the matcher has
		// decision_items to embed.
		//
		// Pre-spawn guard: skip the goroutine entirely when the
		// meeting is already chained. fetchMeetingByID populated
		// MeetingInfo.ChainID at the start of the pipeline, so this
		// check is free. The snapshot can lag by the duration of
		// summary+extraction (manual chain action during that
		// window), but a false-negative here is benign — the next
		// re-sync / manual retry covers the missed evaluation.
		if meeting.ChainID == nil {
			go func(meetingID uuid.UUID) {
				ctx := context.Background()
				repo := repository.NewMeetingRepository()
				fresh, err := repo.GetMeetingByIDSystem(ctx, meetingID)
				if err != nil {
					slog.Warn("stage3 trigger: meeting fetch failed",
						"meeting_id", meetingID, "error", err)
					return
				}
				runStage3Matcher(ctx, NewChainMatcherV3Stage3(), fresh)
			}(meeting.ID)
		}
	}

	// Handle summary result
	if summaryRes.err != nil {
		slog.Error("summary generation failed", "meeting_id", meeting.ID, "error", summaryRes.err)
		s.LogSummaryError(meeting.ID, "api_error", "Summary generation failed", summaryRes.err.Error(), "generation")
		s.markSummaryFailed(meeting.ID)
		return fmt.Errorf("summary generation failed: %w", summaryRes.err)
	}

	summaryResponse := summaryRes.response

	if forceRawSegments {
		slog.Warn("summary generated with forced raw fallback", "meeting_id", meeting.ID, "summary_source", summaryResponse.SummarySource)
	}

	// Record usage
	if summaryResponse.UsageTotalTokens > 0 {
		s.usageRecorder.Record(meeting.UserID, nil, &meeting.ID, "summarize", "openai", "gpt-5.4",
			summaryResponse.UsagePromptTokens, summaryResponse.UsageCompletionTokens, 0, nil)
	}

	if summaryResponse.Summary == "" {
		slog.Warn("NLP service returned empty summary", "meeting_id", meeting.ID)
		s.LogSummaryError(meeting.ID, "empty_response", "NLP service returned empty summary", "", "generation")
		s.markSummaryFailed(meeting.ID)
		return fmt.Errorf("empty summary from NLP service")
	}

	// Content Validation
	if !IsSummaryContentValid(summaryResponse.Summary) {
		slog.Warn("summary content failed validation", "meeting_id", meeting.ID)
		s.LogSummaryError(meeting.ID, "validation_error", "Summary content contains error markers", summaryResponse.Summary, "generation")
		s.markSummaryFailed(meeting.ID)
		return fmt.Errorf("summary content validation failed")
	}

	if summaryResponse.ProcessingStatus == "completed_with_gaps" {
		coverage := 0.0
		if summaryResponse.CoverageRatio != nil {
			coverage = *summaryResponse.CoverageRatio
		}
		detail := fmt.Sprintf(
			"coverage=%.2f successful_chunks=%d failed_chunks=%d total_chunks=%d",
			coverage, summaryResponse.SuccessfulChunks, summaryResponse.FailedChunks, summaryResponse.TotalChunks,
		)
		s.LogSummaryError(meeting.ID, "low_coverage", "Summary completed with gaps", detail, "generation")
	}

	// Persist Summary
	if err := s.SaveSummary(
		meeting.ID,
		summaryResponse.Summary,
		summaryResponse.TLDR,
		summaryResponse.ActionItems,
		summaryResponse.KeyTopics,
		summaryResponse.ActionItemsStructured,
		summaryResponse.KeyTopicsStructured,
		summaryResponse.ProcessingStatus,
		summaryResponse.CoverageRatio,
		summaryResponse.SuccessfulChunks,
		summaryResponse.FailedChunks,
		summaryResponse.ChunkTelemetry,
		summaryResponse.SummarySource,
	); err != nil {
		slog.Error("failed to save summary", "meeting_id", meeting.ID, "error", err)
		s.LogSummaryError(meeting.ID, "api_error", "Failed to save summary to DB", err.Error(), "save")
		return fmt.Errorf("failed to persist summary: %w", err)
	}

	// Mark previous errors as resolved after successful save
	repo := repository.NewSummaryRepository()
	if err := repo.ResolveErrors(context.Background(), meeting.ID); err != nil {
		slog.Warn("failed to resolve errors", "meeting_id", meeting.ID, "error", err)
	}

	slog.Info("post-meeting processing completed", "meeting_id", meeting.ID, "title", meeting.Title)
	return nil
}

// fetchBotLanguage retrieves the user's bot_language preference from the profile.
// Returns "auto" as default if not found.
func (s *PostMeetingService) fetchBotLanguage(userID uuid.UUID) string {
	var lang string
	err := repository.DB.QueryRow(context.Background(),
		`SELECT COALESCE(bot_language, 'auto') FROM profiles WHERE id = $1`, userID,
	).Scan(&lang)
	if err != nil {
		return "auto"
	}
	return lang
}

// generateSummary calls the NLP service to generate a meeting summary.
// forceRawSegments bypasses chat-block source selection and uses raw transcript segments for this run.
func (s *PostMeetingService) generateSummary(
	meetingID uuid.UUID,
	meetingURL string,
	forceRawSegments bool,
	sourceReason string,
	language string,
) (*SummarizeResponse, error) {
	slog.Info("generating summary", "meeting_id", meetingID, "language", language)

	// Prepare request
	reqBody := map[string]interface{}{
		"meeting_id":         meetingID.String(),
		"force_raw_segments": forceRawSegments,
		"language":           language,
	}
	if meetingURL != "" {
		reqBody["meeting_url"] = meetingURL
	}
	if sourceReason != "" {
		reqBody["source_reason"] = sourceReason
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Call NLP service
	req, err := http.NewRequest(
		http.MethodPost,
		s.nlpServiceURL+"/summarize",
		bytes.NewReader(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create NLP summarize request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := SetInternalServiceAuthHeader(req); err != nil {
		return nil, fmt.Errorf("failed to set internal service auth header: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call NLP service: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse response
	var summaryResp SummarizeResponse
	if err := json.Unmarshal(bodyBytes, &summaryResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for errors
	if resp.StatusCode != 200 {
		if summaryResp.Detail != "" {
			return nil, fmt.Errorf("NLP service error: %s", summaryResp.Detail)
		}
		return nil, fmt.Errorf("NLP service returned status %d", resp.StatusCode)
	}

	return &summaryResp, nil
}


// parseAttendeeEmails extracts email addresses from attendees JSONB
func parseAttendeeEmails(attendeesJSON []byte) []string {
	if len(attendeesJSON) == 0 {
		return nil
	}
	var attendees []struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(attendeesJSON, &attendees); err != nil {
		return nil
	}
	emails := make([]string, 0, len(attendees))
	for _, a := range attendees {
		emails = append(emails, a.Email)
	}
	return emails
}

// fetchMeetingByID fetches meeting info from database by ID
func (s *PostMeetingService) fetchMeetingByID(meetingID uuid.UUID) (*MeetingInfo, error) {
	query := `
		SELECT id, user_id, title, start_time, end_time, meeting_url, attendees, organizer_email, chain_id
		FROM meetings
		WHERE id = $1
	`

	var meeting MeetingInfo
	var attendeesJSON []byte
	var meetingURL, organizerEmail *string

	err := repository.DB.QueryRow(context.Background(), query, meetingID).Scan(
		&meeting.ID,
		&meeting.UserID,
		&meeting.Title,
		&meeting.StartTime,
		&meeting.EndTime,
		&meetingURL,
		&attendeesJSON,
		&organizerEmail,
		&meeting.ChainID,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to fetch meeting: %w", err)
	}

	meeting.MeetingURL = meetingURL
	meeting.OrganizerEmail = organizerEmail
	meeting.Attendees = parseAttendeeEmails(attendeesJSON)

	return &meeting, nil
}

// fetchMeetingByURL fetches meeting info from database by meeting URL
func (s *PostMeetingService) fetchMeetingByURL(meetingURL string) (*MeetingInfo, error) {
	// Extended time window from 24 hours to 7 days to handle:
	// - Long meetings that span multiple days
	// - Timezone edge cases
	// - Delayed webhook callbacks from bot services
	query := `
		SELECT id, user_id, title, start_time, end_time, meeting_url, attendees, organizer_email, chain_id
		FROM meetings
		WHERE meeting_url = $1
		AND start_time <= NOW()
		AND start_time >= NOW() - INTERVAL '7 DAYS'
		ORDER BY start_time DESC
		LIMIT 1
	`

	var meeting MeetingInfo
	var attendeesJSON []byte
	var meetingURLPtr, organizerEmail *string

	err := repository.DB.QueryRow(context.Background(), query, meetingURL).Scan(
		&meeting.ID,
		&meeting.UserID,
		&meeting.Title,
		&meeting.StartTime,
		&meeting.EndTime,
		&meetingURLPtr,
		&attendeesJSON,
		&organizerEmail,
		&meeting.ChainID,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to fetch meeting: %w", err)
	}

	meeting.MeetingURL = meetingURLPtr
	meeting.OrganizerEmail = organizerEmail
	meeting.Attendees = parseAttendeeEmails(attendeesJSON)

	return &meeting, nil
}

// SaveSummary saves the summary and structured data to the database
// Faz 2: Now accepts structured action items with deadline fields
// Faz 3: Now accepts structured key topics with stable topic_order for timeline ordering
func (s *PostMeetingService) SaveSummary(
	meetingID uuid.UUID,
	summary string,
	emailDraft string,
	actionItems []string,
	keyTopics []string,
	actionItemsStructured []ActionItemStructured,
	keyTopicsStructured []KeyTopicStructured,
	processingStatus string,
	coverageRatio *float64,
	successfulChunks int,
	failedChunks int,
	chunkTelemetry []ChunkTelemetry,
	summarySource string,
) error {
	switch processingStatus {
	case "completed", "completed_with_gaps", "failed", "processing", "pending":
		// valid status
	default:
		processingStatus = "completed"
	}

	// Save summary to meeting_summaries table (type final_detailed)
	// We use ON CONFLICT (meeting_id, summary_type) to update the existing 'processing' record
	query := `
		INSERT INTO meeting_summaries (
			id, meeting_id, content, summary_type, processing_status, retry_count,
			coverage_ratio, successful_chunks, failed_chunks, summary_source,
			created_at, updated_at
		)
		VALUES (gen_random_uuid(), $1, $2, 'final_detailed', $3, 0, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (meeting_id, summary_type) 
		DO UPDATE SET 
			content = EXCLUDED.content, 
			processing_status = EXCLUDED.processing_status,
			coverage_ratio = EXCLUDED.coverage_ratio,
			successful_chunks = EXCLUDED.successful_chunks,
			failed_chunks = EXCLUDED.failed_chunks,
			summary_source = EXCLUDED.summary_source,
			retry_count = 0,
			updated_at = NOW()
	`
	_, err := repository.DB.Exec(
		context.Background(),
		query,
		meetingID,
		summary,
		processingStatus,
		coverageRatio,
		successfulChunks,
		failedChunks,
		summarySource,
	)
	if err != nil {
		return fmt.Errorf("failed to save summary to meeting_summaries: %w", err)
	}

	if err := s.saveChunkTelemetry(meetingID, chunkTelemetry); err != nil {
		slog.Warn("failed to persist chunk telemetry", "meeting_id", meetingID, "error", err)
	}

	// action_items and key_topics tables have been removed; skipping inserts.

	// Save brief summary (email draft) as final_brief if provided
	if emailDraft != "" {
		briefQuery := `
			INSERT INTO meeting_summaries (id, meeting_id, content, summary_type, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, $2, 'final_brief', NOW(), NOW())
			ON CONFLICT (meeting_id, summary_type)
			DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
		`
		if _, err := repository.DB.Exec(context.Background(), briefQuery, meetingID, emailDraft); err != nil {
			slog.Warn("failed to save brief summary", "meeting_id", meetingID, "error", err)
		}
	}

	return nil
}

func (s *PostMeetingService) saveChunkTelemetry(meetingID uuid.UUID, chunkTelemetry []ChunkTelemetry) error {
	if len(chunkTelemetry) == 0 {
		return nil
	}

	_, err := repository.DB.Exec(
		context.Background(),
		"DELETE FROM summary_chunk_telemetry WHERE meeting_id = $1",
		meetingID,
	)
	if err != nil {
		return fmt.Errorf("failed to cleanup previous chunk telemetry: %w", err)
	}

	query := `
		INSERT INTO summary_chunk_telemetry (
			id, meeting_id, chunk_index, status, latency_ms, retry_count, error_code, model_used, attempt_stage, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`

	for _, chunk := range chunkTelemetry {
		if _, err := repository.DB.Exec(
			context.Background(),
			query,
			uuid.New(),
			meetingID,
			chunk.ChunkIndex,
			chunk.Status,
			chunk.LatencyMs,
			chunk.RetryCount,
			chunk.ErrorCode,
			chunk.ModelUsed,
			chunk.AttemptStage,
		); err != nil {
			return fmt.Errorf("failed to insert chunk telemetry (index=%d): %w", chunk.ChunkIndex, err)
		}
	}

	return nil
}

// UpdateBotStatus updates the bot status for a meeting.
// When status is "completed", also sets end_time = NOW() so that
// the post-meeting grace window uses the actual completion time.
func (s *PostMeetingService) UpdateBotStatus(meetingID uuid.UUID, status string) error {

	var query string
	if status == "completed" {
		query = `
			UPDATE meetings
			SET bot_status = $1, end_time = NOW()
			WHERE id = $2
		`
	} else {
		query = `
			UPDATE meetings
			SET bot_status = $1
			WHERE id = $2
		`
	}

	result, err := repository.DB.Exec(context.Background(), query, status, meetingID)

	if err != nil {
		return fmt.Errorf("failed to update bot status: %w", err)
	}

	rowsAffected := result.RowsAffected()

	if rowsAffected == 0 {
		slog.Warn("UpdateBotStatus: no rows affected, meeting may not exist", "meeting_id", meetingID)
		return fmt.Errorf("meeting not found or no rows updated")
	}

	slog.Info("bot status updated", "meeting_id", meetingID, "status", status, "rows_affected", rowsAffected)
	return nil
}

// acquireProcessingLock attempts to set status to 'processing' for a meeting
// Duplicated from SchedulerService to avoid cross-dependency cycles in simple architecture
func (s *PostMeetingService) acquireProcessingLock(meetingID uuid.UUID) bool {
	query := `
		INSERT INTO meeting_summaries (id, meeting_id, summary_type, content, processing_status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'final_detailed', NULL, 'processing', NOW(), NOW())
		ON CONFLICT (meeting_id, summary_type) 
		DO UPDATE SET 
			processing_status = 'processing',
			updated_at = NOW()
		WHERE meeting_summaries.processing_status IN ('pending', 'failed')
		   OR (meeting_summaries.processing_status = 'processing' AND meeting_summaries.updated_at < NOW() - INTERVAL '30 minutes')
		RETURNING id
	`

	var id uuid.UUID
	err := repository.DB.QueryRow(context.Background(), query, meetingID).Scan(&id)

	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return false
		}
		slog.Error("failed to acquire processing lock", "meeting_id", meetingID, "error", err)
		return false
	}

	return true
}

// logSummaryError records a summary processing error to the summary_errors table
func (s *PostMeetingService) LogSummaryError(meetingID uuid.UUID, errorType, errorMessage, errorDetail, phase string) {
	summaryErr := &models.SummaryError{
		ID:           uuid.New(),
		MeetingID:    meetingID,
		ErrorType:    errorType,
		ErrorMessage: errorMessage,
		ErrorDetail:  errorDetail,
		Phase:        phase,
	}
	repo := repository.NewSummaryRepository()
	if err := repo.CreateSummaryError(context.Background(), summaryErr); err != nil {
		slog.Warn("failed to log summary error", "meeting_id", meetingID, "error", err)
	}
}

// markSummaryFailed increments retry count and sets status to 'failed'
func (s *PostMeetingService) markSummaryFailed(meetingID uuid.UUID) {
	query := `
		UPDATE meeting_summaries
		SET processing_status = 'failed',
		    retry_count = COALESCE(retry_count, 0) + 1,
		    updated_at = NOW()
		WHERE meeting_id = $1 AND summary_type = 'final_detailed'
	`
	_, _ = repository.DB.Exec(context.Background(), query, meetingID)
}

// validateTranscriptSize checks if the number of transcript chunks is within a reasonable range for the meeting duration
func (s *PostMeetingService) validateTranscriptSize(meetingID uuid.UUID) error {
	var durationMinutes float64
	err := repository.DB.QueryRow(context.Background(),
		`SELECT EXTRACT(EPOCH FROM (end_time - start_time))/60 FROM meetings WHERE id = $1`,
		meetingID,
	).Scan(&durationMinutes)
	if err != nil {
		return fmt.Errorf("failed to fetch meeting duration: %w", err)
	}
	if durationMinutes <= 0 {
		durationMinutes = 1
	}

	var count int
	err = repository.DB.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM transcript_segments WHERE meeting_id = $1`,
		meetingID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to count segments: %w", err)
	}

	// Thresholds (chunks per minute)
	const (
		NORMAL_RATE = 17.0 // Normal: 1 hour = ~1000 chunks
		WARN_RATE   = 30.0 // Soft warning threshold
		REJECT_RATE = 50.0 // Hard reject threshold
	)

	normalExpected := int(durationMinutes * NORMAL_RATE)
	warnThreshold := int(durationMinutes * WARN_RATE)
	rejectThreshold := int(durationMinutes * REJECT_RATE)

	rate := float64(count) / durationMinutes

	if count > rejectThreshold {
		return fmt.Errorf(
			"CRITICAL ANOMALY: %d chunks for %.0f min meeting (%.1f chunks/min) - "+
				"exceeds hard limit of %.0f chunks/min (normal: %d chunks expected)",
			count, durationMinutes, rate, REJECT_RATE, normalExpected,
		)
	}

	if count > warnThreshold {
		slog.Warn("high chunk count detected", "meeting_id", meetingID, "chunk_count", count, "duration_minutes", durationMinutes, "chunks_per_min", rate, "warn_threshold", WARN_RATE, "normal_expected", normalExpected)
	} else {
		slog.Info("transcript validation passed", "meeting_id", meetingID, "chunk_count", count, "duration_minutes", durationMinutes, "chunks_per_min", rate)
	}

	return nil
}

// isTranscriptReadyForSummary ensures transcript ingestion has stabilized before final summary.
func (s *PostMeetingService) isTranscriptReadyForSummary(meetingID uuid.UUID) (bool, string, error) {
	query := `
		SELECT
			m.end_time,
			m.bot_status,
			COUNT(ts.id) AS segment_count,
			MAX(ts.created_at) AS last_segment_created_at
		FROM meetings m
		LEFT JOIN transcript_segments ts ON ts.meeting_id = m.id
		WHERE m.id = $1
		GROUP BY m.end_time, m.bot_status
	`

	var (
		endTime            time.Time
		botStatus          sql.NullString
		segmentCount       int
		lastSegmentCreated sql.NullTime
	)

	if err := repository.DB.QueryRow(context.Background(), query, meetingID).Scan(
		&endTime,
		&botStatus,
		&segmentCount,
		&lastSegmentCreated,
	); err != nil {
		return false, "", fmt.Errorf("failed to query transcript readiness: %w", err)
	}

	now := time.Now().UTC()
	botDone := botStatus.Valid && (botStatus.String == "completed" || botStatus.String == "failed")

	// Early exit: bot is done and no transcript segments exist — no point retrying
	if segmentCount == 0 && botDone {
		return false, "no_transcript", nil
	}

	if segmentCount == 0 || !lastSegmentCreated.Valid {
		return false, "no transcript segments yet", nil
	}

	if now.Sub(endTime) < summaryGraceAfterEnd {
		return false, fmt.Sprintf("grace window after end_time not elapsed (%v)", summaryGraceAfterEnd), nil
	}

	if now.Sub(lastSegmentCreated.Time) < summaryIngestionQuietPeriod {
		return false, fmt.Sprintf("last segment is too recent (%v quiet period)", summaryIngestionQuietPeriod), nil
	}

	return true, "", nil
}

// ===== RAG-Aware Embedding Preparation =====

const (
	// Context overlap: max tokens from previous block's text to prepend as context
	embeddingContextOverlapMaxChars = 200
	// Short block merging: blocks with fewer chars than this are merged with neighbors
	embeddingShortBlockThreshold = 80
)

// prepareEmbeddingTexts builds RAG-aware embedding input for each block.
// It applies: (1) short block merging, (2) context overlap (sliding window),
// and (3) speaker prefix injection.
// The returned slice has the same length and order as the input blocks.
// DB text is never modified — only the text sent to the embedding model is enriched.
func prepareEmbeddingTexts(blocks []models.TranscriptChatBlock) []string {
	if len(blocks) == 0 {
		return nil
	}

	// Phase 1: Short block merging — merge short blocks with their next neighbor.
	// We build "merged groups" where each group has one or more blocks whose texts
	// are concatenated for embedding. Each block in a group still gets the same embedding.
	type mergedGroup struct {
		text     string                        // concatenated text for embedding
		blockIDs []int                         // indices into the original blocks slice
	}

	var groups []mergedGroup
	i := 0
	for i < len(blocks) {
		groupText := blocks[i].Text
		groupIDs := []int{i}

		// Merge consecutive short blocks with the same speaker
		for i+1 < len(blocks) &&
			len(strings.TrimSpace(groupText)) < embeddingShortBlockThreshold &&
			blocks[i+1].Speaker == blocks[groupIDs[0]].Speaker {
			i++
			groupText += " " + blocks[i].Text
			groupIDs = append(groupIDs, i)
		}

		groups = append(groups, mergedGroup{text: groupText, blockIDs: groupIDs})
		i++
	}

	// Phase 2: Context overlap — prepend tail of previous group's text
	texts := make([]string, len(blocks))
	for gi, group := range groups {
		embText := group.text

		if gi > 0 {
			prevText := groups[gi-1].text
			overlap := prevText
			if len(overlap) > embeddingContextOverlapMaxChars {
				// Take the last N chars, but break at a word boundary
				overlap = overlap[len(overlap)-embeddingContextOverlapMaxChars:]
				if spaceIdx := strings.Index(overlap, " "); spaceIdx != -1 {
					overlap = overlap[spaceIdx+1:]
				}
			}
			embText = overlap + " " + embText
		}

		// Phase 3: Speaker prefix — prepend "[Speaker]: " for speaker-aware retrieval
		for _, blockIdx := range group.blockIDs {
			finalText := strings.TrimSpace(embText)
			speaker := strings.TrimSpace(blocks[blockIdx].Speaker)
			if speaker != "" && speaker != "unknown" {
				finalText = "[" + speaker + "]: " + finalText
			}
			texts[blockIdx] = finalText
		}
	}

	return texts
}

// EmbeddingRequest represents a request to generate a single embedding
type EmbeddingRequest struct {
	Text string `json:"text"`
}

// EmbeddingResponse represents the response from embedding generation
type EmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
	Model     string    `json:"model"`
	Dimension int       `json:"dimension"`
}

// BatchEmbeddingRequest represents a batch embedding request
type BatchEmbeddingRequest struct {
	Texts []string `json:"texts"`
}

// BatchEmbeddingResponse represents the response from batch embedding generation.
// Usage fields (issue #39) are populated by the Python router so the Go
// caller can record them into usage_logs + bump the quota counter.
// Zero-values when the server is too old to emit them (omitempty on
// the wire); callers treat 0 as "no usage info, skip the Record call".
type BatchEmbeddingResponse struct {
	Embeddings           [][]float64 `json:"embeddings"`
	Model                string      `json:"model"`
	Count                int         `json:"count"`
	UsagePromptTokens    int         `json:"usage_prompt_tokens,omitempty"`
	UsageTotalTokens     int         `json:"usage_total_tokens,omitempty"`
}

// GenerateEmbeddingsForMeeting generates embeddings for all chat blocks in a meeting
// This runs in the background after transcript processing is complete
func (s *PostMeetingService) GenerateEmbeddingsForMeeting(ctx context.Context, meetingID uuid.UUID) {
	slog.Info("starting embedding generation", "meeting_id", meetingID)

	// Lookup the meeting once so we can attribute usage to its owner
	// (issue #39). Fetch failure is non-fatal — embedding still runs,
	// but usage_logs entries are skipped for this meeting.
	var ownerID uuid.UUID
	if m, err := repository.NewMeetingRepository().GetMeetingByIDSystem(ctx, meetingID); err != nil {
		slog.Warn("embedding: meeting lookup failed — usage recording skipped",
			"meeting_id", meetingID, "error", err)
	} else {
		ownerID = m.UserID
	}

	// Get transcript repository
	transcriptRepo := repository.NewTranscriptRepository()

	// 1. Fetch chat blocks without embedding
	blocks, err := transcriptRepo.GetChatBlocksWithoutEmbedding(ctx, meetingID)
	if err != nil {
		slog.Error("failed to fetch chat blocks for embedding", "meeting_id", meetingID, "error", err)
		return
	}

	if len(blocks) == 0 {
		slog.Info("no blocks need embedding", "meeting_id", meetingID)
		return
	}

	slog.Info("found blocks to generate embeddings for", "meeting_id", meetingID, "block_count", len(blocks))

	// Prepare RAG-aware embedding texts (context overlap + short block merging)
	allTexts := prepareEmbeddingTexts(blocks)

	// 2. Process in batches of 100 (OpenAI API limit)
	const batchSize = 100
	successCount := 0
	failCount := 0

	for i := 0; i < len(blocks); i += batchSize {
		end := i + batchSize
		if end > len(blocks) {
			end = len(blocks)
		}

		batchTexts := allTexts[i:end]

		// 3. Call NLP service to generate embeddings
		batchResp, err := s.generateBatchEmbeddings(batchTexts)
		if err != nil {
			slog.Error("embedding batch failed", "meeting_id", meetingID, "batch", i/batchSize+1, "error", err)
			failCount += end - i
			continue
		}

		// 4. Save embeddings to database
		for j, embedding := range batchResp.Embeddings {
			if embedding == nil {
				failCount++
				continue
			}

			err := transcriptRepo.UpdateChatBlockEmbedding(ctx, blocks[i+j].ID, embedding)
			if err != nil {
				slog.Error("failed to save embedding for block", "meeting_id", meetingID, "block_id", blocks[i+j].ID, "error", err)
				failCount++
			} else {
				successCount++
			}
		}

		// Issue #39 — record embedding cost. CalculateCost picks up
		// text-embedding-3-small @ $0.02/1M; UsageRecorder writes to
		// usage_logs AND bumps usage_quota_periods atomically.
		// Skip when we couldn't resolve the owner or the server didn't
		// emit usage (back-compat with older mia-search).
		if ownerID != uuid.Nil && batchResp.UsageTotalTokens > 0 {
			model := batchResp.Model
			if model == "" {
				model = "text-embedding-3-small"
			}
			s.usageRecorder.Record(
				ownerID, nil, &meetingID,
				"embedding", "openai", model,
				batchResp.UsagePromptTokens, 0, 0,
				map[string]interface{}{
					"batch_index": i / batchSize,
					"block_count": len(batchTexts),
				},
			)
		}

		slog.Info("embedding batch completed", "meeting_id", meetingID, "batch", i/batchSize+1, "total_batches", (len(blocks)+batchSize-1)/batchSize, "success_count", successCount, "total_blocks", len(blocks))

		// Rate limiting: 2 seconds between batches (OpenAI: 3000 req/min = 50 req/sec)
		time.Sleep(2 * time.Second)
	}

	slog.Info("embedding generation completed", "meeting_id", meetingID, "success_count", successCount, "fail_count", failCount, "total_blocks", len(blocks))
}

// GenerateEmbeddingsForStableBlocks generates embeddings for chat blocks before the given time cutoff.
// Used during live meetings to embed only stable blocks that won't be rebuilt by the next incremental pass.
func (s *PostMeetingService) GenerateEmbeddingsForStableBlocks(ctx context.Context, meetingID uuid.UUID, stableBefore float64) {
	// Issue #39 — lookup owner once for usage attribution. See note on
	// GenerateEmbeddingsForMeeting; same fail-soft behaviour.
	var ownerID uuid.UUID
	if m, err := repository.NewMeetingRepository().GetMeetingByIDSystem(ctx, meetingID); err != nil {
		slog.Warn("stable embedding: meeting lookup failed — usage recording skipped",
			"meeting_id", meetingID, "error", err)
	} else {
		ownerID = m.UserID
	}

	transcriptRepo := repository.NewTranscriptRepository()

	blocks, err := transcriptRepo.GetChatBlocksWithoutEmbeddingBeforeTime(ctx, meetingID, stableBefore)
	if err != nil {
		slog.Error("failed to fetch stable chat blocks for embedding", "meeting_id", meetingID, "error", err)
		return
	}

	if len(blocks) == 0 {
		return
	}

	slog.Info("generating embeddings for stable blocks", "meeting_id", meetingID, "block_count", len(blocks), "stable_before", stableBefore)

	// Prepare RAG-aware embedding texts
	allTexts := prepareEmbeddingTexts(blocks)

	const batchSize = 100
	successCount := 0

	for i := 0; i < len(blocks); i += batchSize {
		end := i + batchSize
		if end > len(blocks) {
			end = len(blocks)
		}

		batchTexts := allTexts[i:end]

		batchResp, err := s.generateBatchEmbeddings(batchTexts)
		if err != nil {
			slog.Error("stable embedding batch failed", "meeting_id", meetingID, "error", err)
			continue
		}

		for j, embedding := range batchResp.Embeddings {
			if embedding == nil {
				continue
			}
			if err := transcriptRepo.UpdateChatBlockEmbedding(ctx, blocks[i+j].ID, embedding); err != nil {
				slog.Error("failed to save stable embedding for block", "meeting_id", meetingID, "block_id", blocks[i+j].ID, "error", err)
			} else {
				successCount++
			}
		}

		if ownerID != uuid.Nil && batchResp.UsageTotalTokens > 0 {
			model := batchResp.Model
			if model == "" {
				model = "text-embedding-3-small"
			}
			s.usageRecorder.Record(
				ownerID, nil, &meetingID,
				"embedding", "openai", model,
				batchResp.UsagePromptTokens, 0, 0,
				map[string]interface{}{
					"batch_index": i / batchSize,
					"block_count": len(batchTexts),
					"stable":      true,
				},
			)
		}
	}

	slog.Info("stable embedding generation done", "meeting_id", meetingID, "success_count", successCount, "total_blocks", len(blocks))
}

// generateBatchEmbeddings calls the NLP service to generate embeddings
// for a batch of texts. Returns the parsed response so callers can both
// consume embeddings AND read the usage payload (issue #39 — embedding
// cost was previously dropped at this seam).
func (s *PostMeetingService) generateBatchEmbeddings(texts []string) (BatchEmbeddingResponse, error) {
	var empty BatchEmbeddingResponse

	reqBody := BatchEmbeddingRequest{
		Texts: texts,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return empty, fmt.Errorf("failed to marshal batch embedding request: %w", err)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		s.nlpServiceURL+"/embeddings/generate-batch",
		bytes.NewReader(jsonData),
	)
	if err != nil {
		return empty, fmt.Errorf("failed to create batch embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := SetInternalServiceAuthHeader(req); err != nil {
		return empty, fmt.Errorf("failed to set internal service auth header: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return empty, fmt.Errorf("NLP service request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return empty, fmt.Errorf("NLP service returned status %d: %s", resp.StatusCode, string(body))
	}

	var batchResp BatchEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&batchResp); err != nil {
		return empty, fmt.Errorf("failed to decode batch embedding response: %w", err)
	}

	return batchResp, nil
}
