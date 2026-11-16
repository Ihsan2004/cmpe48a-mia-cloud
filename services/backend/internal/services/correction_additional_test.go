package services

import (
	"testing"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== deduplicateSegments ====================

func TestDeduplicateSegments_NoDuplicates(t *testing.T) {
	t.Parallel()

	segments := []models.TranscriptSegment{
		{ID: uuid.New(), StartTime: 1.0, Text: "Hello"},
		{ID: uuid.New(), StartTime: 2.0, Text: "World"},
	}

	result := deduplicateSegments(segments)
	if len(result) != 2 {
		t.Errorf("expected 2 segments, got %d", len(result))
	}
}

func TestDeduplicateSegments_WithDuplicates(t *testing.T) {
	t.Parallel()

	segments := []models.TranscriptSegment{
		{ID: uuid.New(), StartTime: 1.0, Text: "Hello"},
		{ID: uuid.New(), StartTime: 1.0, Text: "Hello"}, // duplicate: same timestamp + text
		{ID: uuid.New(), StartTime: 2.0, Text: "World"},
	}

	result := deduplicateSegments(segments)
	if len(result) != 2 {
		t.Errorf("expected 2 unique segments, got %d", len(result))
	}
}

func TestDeduplicateSegments_SameTextDifferentTime(t *testing.T) {
	t.Parallel()

	segments := []models.TranscriptSegment{
		{ID: uuid.New(), StartTime: 1.0, Text: "Hello"},
		{ID: uuid.New(), StartTime: 5.0, Text: "Hello"}, // same text, different time — NOT duplicate
	}

	result := deduplicateSegments(segments)
	if len(result) != 2 {
		t.Errorf("expected 2 segments (different timestamps), got %d", len(result))
	}
}

func TestDeduplicateSegments_Empty(t *testing.T) {
	t.Parallel()

	result := deduplicateSegments([]models.TranscriptSegment{})
	if len(result) != 0 {
		t.Errorf("expected 0 segments, got %d", len(result))
	}
}

// ==================== filterEmptyBlocks ====================

func TestFilterEmptyBlocks_AllValid(t *testing.T) {
	t.Parallel()

	blocks := []aggregatedBlock{
		{Segments: []models.TranscriptSegment{{Text: "Hello world"}}},
		{Segments: []models.TranscriptSegment{{Text: "Another block"}}},
	}

	result := filterEmptyBlocks(blocks)
	if len(result) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(result))
	}
}

func TestFilterEmptyBlocks_RemovesEmpty(t *testing.T) {
	t.Parallel()

	blocks := []aggregatedBlock{
		{Segments: []models.TranscriptSegment{{Text: "Valid"}}},
		{Segments: []models.TranscriptSegment{{Text: ""}}}, // empty
		{Segments: []models.TranscriptSegment{{Text: "Also valid"}}},
	}

	result := filterEmptyBlocks(blocks)
	if len(result) != 2 {
		t.Errorf("expected 2 non-empty blocks, got %d", len(result))
	}
}

func TestFilterEmptyBlocks_AllEmpty(t *testing.T) {
	t.Parallel()

	blocks := []aggregatedBlock{
		{Segments: []models.TranscriptSegment{{Text: ""}}},
		{Segments: []models.TranscriptSegment{{Text: ""}}},
	}

	result := filterEmptyBlocks(blocks)
	if len(result) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(result))
	}
}

func TestFilterEmptyBlocks_EmptyInput(t *testing.T) {
	t.Parallel()

	result := filterEmptyBlocks([]aggregatedBlock{})
	if len(result) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(result))
	}
}

func TestFilterEmptyBlocks_MultiSegmentBlock(t *testing.T) {
	t.Parallel()

	// Block with multiple segments where combined text is non-empty
	blocks := []aggregatedBlock{
		{Segments: []models.TranscriptSegment{
			{Text: "Hello "},
			{Text: "world"},
		}},
	}

	result := filterEmptyBlocks(blocks)
	if len(result) != 1 {
		t.Errorf("expected 1 block with combined text, got %d", len(result))
	}
}

// ==================== isRetryableStatus ====================

func TestIsRetryableStatus_Retryable(t *testing.T) {
	t.Parallel()

	retryable := []int{429, 500, 502, 503}
	for _, code := range retryable {
		if !isRetryableStatus(code) {
			t.Errorf("expected status %d to be retryable", code)
		}
	}
}

func TestIsRetryableStatus_NotRetryable(t *testing.T) {
	t.Parallel()

	notRetryable := []int{200, 201, 400, 401, 403, 404, 422}
	for _, code := range notRetryable {
		if isRetryableStatus(code) {
			t.Errorf("expected status %d to NOT be retryable", code)
		}
	}
}

// ==================== aggregateSegments ====================

func TestAggregateSegments_Empty(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	blocks := svc.aggregateSegments([]models.TranscriptSegment{}, map[uuid.UUID]bool{})
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks for empty input, got %d", len(blocks))
	}
}

func TestAggregateSegments_SingleSegment(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	segments := []models.TranscriptSegment{
		{ID: uuid.New(), Speaker: "Alice", Text: "Hello", StartTime: 0.0, EndTime: 1.0},
	}

	blocks := svc.aggregateSegments(segments, map[uuid.UUID]bool{})
	if len(blocks) != 1 {
		t.Errorf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Speaker != "Alice" {
		t.Errorf("expected speaker Alice, got %s", blocks[0].Speaker)
	}
}

func TestAggregateSegments_SplitOnSpeakerChange(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	segments := []models.TranscriptSegment{
		{ID: uuid.New(), Speaker: "Alice", Text: "Hello", StartTime: 0.0, EndTime: 1.0},
		{ID: uuid.New(), Speaker: "Alice", Text: "How are you", StartTime: 1.0, EndTime: 2.0},
		{ID: uuid.New(), Speaker: "Bob", Text: "Fine thanks", StartTime: 2.0, EndTime: 3.0},
	}

	blocks := svc.aggregateSegments(segments, map[uuid.UUID]bool{})
	if len(blocks) != 2 {
		t.Errorf("expected 2 blocks (speaker change), got %d", len(blocks))
	}
	if blocks[0].Speaker != "Alice" {
		t.Errorf("first block speaker should be Alice, got %s", blocks[0].Speaker)
	}
	if blocks[1].Speaker != "Bob" {
		t.Errorf("second block speaker should be Bob, got %s", blocks[1].Speaker)
	}
}

func TestAggregateSegments_SplitOnMaxDuration(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	// MaxBlockDuration = 15.0s — create segments spanning > 15s
	segments := []models.TranscriptSegment{
		{ID: uuid.New(), Speaker: "Alice", Text: "Start", StartTime: 0.0, EndTime: 5.0},
		{ID: uuid.New(), Speaker: "Alice", Text: "Middle", StartTime: 5.0, EndTime: 10.0},
		{ID: uuid.New(), Speaker: "Alice", Text: "End", StartTime: 10.0, EndTime: 16.0}, // 0-16 > 15s
	}

	blocks := svc.aggregateSegments(segments, map[uuid.UUID]bool{})
	if len(blocks) < 2 {
		t.Errorf("expected at least 2 blocks (duration split), got %d", len(blocks))
	}
}

func TestAggregateSegments_SplitOnSilenceGap(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	// MaxSilenceGap = 2.0s
	segments := []models.TranscriptSegment{
		{ID: uuid.New(), Speaker: "Alice", Text: "Before gap", StartTime: 0.0, EndTime: 1.0},
		{ID: uuid.New(), Speaker: "Alice", Text: "After gap", StartTime: 4.0, EndTime: 5.0}, // 3s gap > 2s
	}

	blocks := svc.aggregateSegments(segments, map[uuid.UUID]bool{})
	if len(blocks) != 2 {
		t.Errorf("expected 2 blocks (silence gap), got %d", len(blocks))
	}
}

func TestAggregateSegments_PropagatedFlag(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	seg1ID := uuid.New()
	seg2ID := uuid.New()

	segments := []models.TranscriptSegment{
		{ID: seg1ID, Speaker: "Alice", Text: "Hello", StartTime: 0.0, EndTime: 1.0},
		{ID: seg2ID, Speaker: "Alice", Text: "World", StartTime: 1.0, EndTime: 2.0},
	}

	propagated := map[uuid.UUID]bool{seg2ID: true}
	blocks := svc.aggregateSegments(segments, propagated)

	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if !blocks[0].SpeakerPropagated {
		t.Error("expected block to have SpeakerPropagated=true")
	}
	if blocks[0].PropagatedCount != 1 {
		t.Errorf("expected PropagatedCount=1, got %d", blocks[0].PropagatedCount)
	}
}

// ==================== Circuit Breaker (basic state) ====================

func TestCircuitBreaker_InitiallyClosed(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()
	if svc.cbIsOpen() {
		t.Error("circuit breaker should be closed initially")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()

	// cbFailThreshold = 3
	for i := 0; i < 3; i++ {
		svc.cbRecordFailure()
	}

	if !svc.cbIsOpen() {
		t.Error("circuit breaker should be open after 3 failures")
	}
}

func TestCircuitBreaker_ResetsOnSuccess(t *testing.T) {
	t.Parallel()

	svc := NewCorrectionService()

	// Fail twice (not yet threshold)
	svc.cbRecordFailure()
	svc.cbRecordFailure()

	// Success resets
	svc.cbRecordSuccess()

	if svc.cbIsOpen() {
		t.Error("circuit breaker should be closed after success reset")
	}
}
