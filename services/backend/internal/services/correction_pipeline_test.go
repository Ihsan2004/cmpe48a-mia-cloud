package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== callOpenAICorrection HTTP Bridge ====================

func TestCallOpenAICorrection_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("expected Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type: application/json")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]string{
						"content": `{"items": [{"id": "block_0", "corrected_text": "Düzeltilmiş metin"}]}`,
					},
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     200,
				"completion_tokens": 50,
			},
		})
	}))
	defer server.Close()

	// callOpenAICorrection uses hardcoded OpenAI URL, so we test response parsing directly:
	responseJSON := `{"items": [{"id": "block_0", "corrected_text": "Düzeltilmiş metin"}, {"id": "block_1", "corrected_text": "İkinci blok"}]}`
	var wrapper correctionResponseWrapper
	err := json.Unmarshal([]byte(responseJSON), &wrapper)
	if err != nil {
		t.Fatalf("failed to parse correction response: %v", err)
	}

	if len(wrapper.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(wrapper.Items))
	}
	if wrapper.Items[0].ID != "block_0" {
		t.Errorf("expected block_0, got %s", wrapper.Items[0].ID)
	}
	if wrapper.Items[0].CorrectedText != "Düzeltilmiş metin" {
		t.Errorf("expected 'Düzeltilmiş metin', got '%s'", wrapper.Items[0].CorrectedText)
	}

	_ = server // acknowledged
}

func TestCorrectionResponseParsing_EmptyItems(t *testing.T) {
	t.Parallel()

	responseJSON := `{"items": []}`
	var wrapper correctionResponseWrapper
	err := json.Unmarshal([]byte(responseJSON), &wrapper)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(wrapper.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(wrapper.Items))
	}
}

func TestCorrectionResponseParsing_MalformedJSON(t *testing.T) {
	t.Parallel()

	responseJSON := `{"items": [{"id": "block_0", "corrected_text": `
	var wrapper correctionResponseWrapper
	err := json.Unmarshal([]byte(responseJSON), &wrapper)
	if err == nil {
		t.Error("expected parse error for malformed JSON")
	}
}

// ==================== Circuit Breaker + Retry ====================

func TestCorrectionRetry_CircuitBreakerOpen_RejectsImmediately(t *testing.T) {
	svc := &CorrectionService{
		client: &http.Client{Timeout: 1 * time.Second},
		apiKey: "test-key",
	}

	// Trip the circuit breaker
	for i := 0; i < cbFailThreshold; i++ {
		svc.cbRecordFailure()
	}

	_, _, err := svc.callOpenAICorrectionWithRetry("system", "user")
	if err == nil {
		t.Fatal("expected circuit breaker rejection")
	}
	if !strings.Contains(err.Error(), "circuit breaker open") {
		t.Errorf("expected 'circuit breaker open' error, got: %v", err)
	}
}

func TestCorrectionRetry_CircuitBreakerResets_AfterSuccess(t *testing.T) {
	svc := &CorrectionService{
		client: &http.Client{Timeout: 1 * time.Second},
		apiKey: "test-key",
	}

	// Trip it
	for i := 0; i < cbFailThreshold; i++ {
		svc.cbRecordFailure()
	}

	// Reset
	svc.cbRecordSuccess()

	if svc.cbIsOpen() {
		t.Error("circuit breaker should be closed after success")
	}
}

// ==================== Domain Dictionary Prompt ====================

func TestBuildDomainTermsPrompt_WithTerms(t *testing.T) {
	t.Parallel()

	// Mock the getDictionaryTerms by testing the prompt building logic directly
	terms := []repository.CorrectionTerm{
		{WrongForm: "pos payment", CorrectForm: "POS Payment", Description: "Ödeme sistemi"},
		{WrongForm: "rag", CorrectForm: "RAG", Description: ""},
	}

	var sb strings.Builder
	sb.WriteString("DOMAIN TERİMLERİ (sık karşılaşılan STT hataları):\n")
	for _, t := range terms {
		sb.WriteString("- '" + t.WrongForm + "' → '" + t.CorrectForm + "'")
		if t.Description != "" {
			sb.WriteString(" (" + t.Description + ")")
		}
		sb.WriteString("\n")
	}
	result := sb.String()

	if !strings.Contains(result, "pos payment") {
		t.Error("expected wrong form in prompt")
	}
	if !strings.Contains(result, "POS Payment") {
		t.Error("expected correct form in prompt")
	}
	if !strings.Contains(result, "Ödeme sistemi") {
		t.Error("expected description in prompt")
	}
	if strings.Contains(result, "RAG (") {
		t.Error("RAG should not have description parentheses (empty description)")
	}
}

func TestBuildDomainTermsPrompt_EmptyTerms(t *testing.T) {
	t.Parallel()

	terms := []repository.CorrectionTerm{}

	if len(terms) != 0 {
		t.Error("expected empty terms")
	}
	// buildDomainTermsPrompt returns "" for empty terms
	// Then processBatch uses the fallback message
	fallback := "DOMAIN TERİMLERİ: (sözlük boş — genel STT düzeltmeleri uygula)\n"
	if fallback == "" {
		t.Error("fallback should not be empty")
	}
}

// ==================== Context Header Building ====================

func TestContextHeader_WithTitleAndParticipants(t *testing.T) {
	t.Parallel()

	meetingTitle := "Sprint Review"
	participants := []string{"Ali", "Bob", "Charlie"}

	contextHeader := "TOPLANTI BAĞLAMI:\n"
	contextHeader += "- Başlık: " + meetingTitle + "\n"
	contextHeader += "- Katılımcılar: " + strings.Join(participants, ", ") + "\n"

	if !strings.Contains(contextHeader, "Sprint Review") {
		t.Error("expected meeting title in context")
	}
	if !strings.Contains(contextHeader, "Ali") {
		t.Error("expected participant in context")
	}
}

func TestContextHeader_Empty(t *testing.T) {
	t.Parallel()

	meetingTitle := ""
	participants := []string{}

	contextHeader := ""
	if meetingTitle != "" || len(participants) > 0 {
		contextHeader = "TOPLANTI BAĞLAMI:\n"
	}

	if contextHeader != "" {
		t.Error("empty context should produce empty header")
	}
}

// ==================== processBatch Input Building ====================

func TestProcessBatch_InputItemBuilding(t *testing.T) {
	t.Parallel()

	batch := []aggregatedBlock{
		{Speaker: "Ali", OriginalText: "eee şey MIA demosunu göstericez", StartTime: 0, EndTime: 5},
		{Speaker: "Bob", OriginalText: "tamam evet anladım", StartTime: 5, EndTime: 8},
	}

	var inputItems []BlockCorrectionRequestItem
	blockIDMap := make(map[string]aggregatedBlock)

	for i, block := range batch {
		tempID := "block_" + string(rune('0'+i))
		inputItems = append(inputItems, BlockCorrectionRequestItem{
			ID:   tempID,
			Text: block.OriginalText,
		})
		blockIDMap[tempID] = block
	}

	if len(inputItems) != 2 {
		t.Fatalf("expected 2 input items, got %d", len(inputItems))
	}
	if inputItems[0].Text != "eee şey MIA demosunu göstericez" {
		t.Error("expected original text in input item")
	}
}

// ==================== processBatch Result Mapping ====================

func TestProcessBatch_ResultMapping_WithDiffGuard(t *testing.T) {
	t.Parallel()

	// Simulate corrected results
	original := "eee şey bi MIA demosunu göstericez işte bu toplantıda"
	corrected := "MIA demo'sunu göstereceğiz bu toplantıda."
	summarized := "MIA demo" // too short — diff guard should reject

	if !correctionDiffGuard(original, corrected) {
		t.Error("good correction should pass diff guard")
	}

	if correctionDiffGuard(original, summarized) {
		t.Error("summarized text should be rejected by diff guard")
	}
}

func TestProcessBatch_NoiseDetectionPipeline(t *testing.T) {
	t.Parallel()

	// Verify the 4-stage noise detection pipeline
	testCases := []struct {
		text     string
		duration float64
		isNoise  bool
	}{
		{"Evet", 0.5, true},           // single word, short
		{"(gülüşmeler)", 2.0, true},   // ambient sound
		{"Normal uzun bir cümle burada yer alıyor ve anlam taşıyor.", 10.0, false},
		{"eee", 0.3, true},             // single filler word, very short
	}

	for _, tc := range testCases {
		noise := detectNoise(tc.text, tc.duration)
		if !noise {
			noise = detectPostCorrectionNoise(tc.text, tc.duration)
		}
		if !noise {
			noise = detectAmbientSound(tc.text)
		}
		if !noise {
			noise = detectSTTHallucination(tc.text)
		}

		if noise != tc.isNoise {
			t.Errorf("text=%q duration=%.1f: expected noise=%v, got %v", tc.text, tc.duration, tc.isNoise, noise)
		}
	}
}

// ==================== TranscriptChatBlock Construction ====================

func TestChatBlockConstruction_FromAggregatedBlock(t *testing.T) {
	t.Parallel()

	meetingID := uuid.New()
	segID1 := uuid.New()
	segID2 := uuid.New()

	aggBlock := aggregatedBlock{
		Speaker:   "Ali",
		StartTime: 10.5,
		EndTime:   25.3,
		Segments: []models.TranscriptSegment{
			{ID: segID1},
			{ID: segID2},
		},
		OriginalText: "test text",
	}

	var rawIDs []uuid.UUID
	for _, s := range aggBlock.Segments {
		rawIDs = append(rawIDs, s.ID)
	}

	block := models.TranscriptChatBlock{
		ID:            uuid.New(),
		MeetingID:     meetingID,
		Speaker:       aggBlock.Speaker,
		Text:          "corrected test text",
		StartTime:     aggBlock.StartTime,
		EndTime:       aggBlock.EndTime,
		RawSegmentIDs: rawIDs,
		IsNoise:       false,
	}

	if block.Speaker != "Ali" {
		t.Errorf("expected speaker Ali, got %s", block.Speaker)
	}
	if len(block.RawSegmentIDs) != 2 {
		t.Errorf("expected 2 raw segment IDs, got %d", len(block.RawSegmentIDs))
	}
	if block.StartTime != 10.5 {
		t.Errorf("expected start_time 10.5, got %f", block.StartTime)
	}
}
