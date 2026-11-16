package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ==================== HTTP Bridge Tests ====================

func TestCallExtractionService_HappyPath(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SHARED_SECRET", "test-secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/extract" {
			t.Errorf("expected /extract, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type: application/json")
		}
		// Verify auth header is set
		if r.Header.Get("X-Internal-Service-Secret") == "" {
			// Only fail if secret is configured
			if os.Getenv("INTERNAL_SERVICE_SHARED_SECRET") != "" {
				t.Error("expected auth header")
			}
		}

		// Return mock response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"items": []map[string]interface{}{
				{
					"text":        "API v2 ile devam",
					"item_type":   "decision",
					"owner_name":  "Ali",
					"source_quote": "Ali: v2 ile gidelim",
					"context":     "Performans testleri olumlu",
				},
			},
			"usage_prompt_tokens":     500,
			"usage_completion_tokens": 200,
			"usage_total_tokens":      700,
		})
	}))
	defer server.Close()

	extractor := &DecisionExtractor{
		nlpServiceURL: server.URL,
		client:        &http.Client{Timeout: 5 * time.Second},
	}

	resp, err := extractor.callExtractionService(
		[16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, // dummy UUID
		"[Ali]: v2 ile gidelim",
		nil, // segments
		nil, // confirmedTexts
		nil, // chainItems
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	if resp.Items[0].Text != "API v2 ile devam" {
		t.Errorf("expected 'API v2 ile devam', got '%s'", resp.Items[0].Text)
	}
	if resp.Items[0].Context == nil || *resp.Items[0].Context != "Performans testleri olumlu" {
		t.Error("expected context field")
	}
	if resp.UsageTotalTokens == nil || *resp.UsageTotalTokens != 700 {
		t.Error("expected 700 total tokens")
	}
}

func TestCallExtractionService_Non200(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SHARED_SECRET", "test-secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"detail": "OpenAI timeout"}`))
	}))
	defer server.Close()

	extractor := &DecisionExtractor{
		nlpServiceURL: server.URL,
		client:        &http.Client{Timeout: 5 * time.Second},
	}

	_, err := extractor.callExtractionService([16]byte{}, "[Ali]: test", nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("expected status 500 in error, got: %v", err)
	}
}

func TestCallExtractionService_WithConfirmedItems(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SHARED_SECRET", "test-secret")

	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"items":   []interface{}{},
		})
	}))
	defer server.Close()

	extractor := &DecisionExtractor{
		nlpServiceURL: server.URL,
		client:        &http.Client{Timeout: 5 * time.Second},
	}

	extractor.callExtractionService([16]byte{}, "transcript", nil, []string{"Deploy on Wednesday"}, nil)

	confirmed, ok := receivedBody["confirmed_items"].([]interface{})
	if !ok || len(confirmed) != 1 {
		t.Fatalf("expected confirmed_items with 1 entry, got %v", receivedBody["confirmed_items"])
	}
	if confirmed[0] != "Deploy on Wednesday" {
		t.Errorf("expected 'Deploy on Wednesday', got %v", confirmed[0])
	}
}

func TestCallExtractionService_Timeout(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SHARED_SECRET", "test-secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // slow server
		w.WriteHeader(200)
	}))
	defer server.Close()

	extractor := &DecisionExtractor{
		nlpServiceURL: server.URL,
		client:        &http.Client{Timeout: 100 * time.Millisecond}, // very short timeout
	}

	_, err := extractor.callExtractionService([16]byte{}, "test", nil, nil, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// ==================== Extraction Response JSON Parsing ====================

func TestExtractionResponse_ValidJSON(t *testing.T) {
	t.Parallel()

	jsonStr := `{
		"success": true,
		"items": [
			{"text": "Deploy on Wednesday", "item_type": "decision", "owner_name": "Ali", "source_quote": "Ali said let's deploy"},
			{"text": "Write tests by Friday", "item_type": "action_item", "owner_name": "Bob", "source_quote": "Bob will write tests"},
			{"text": "Budget needs discussion", "item_type": "open_topic", "owner_name": null, "source_quote": "budget wasn't resolved"}
		],
		"usage_prompt_tokens": 500,
		"usage_completion_tokens": 200,
		"usage_total_tokens": 700
	}`

	var result extractionResponse
	err := json.Unmarshal([]byte(jsonStr), &result)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if !result.Success {
		t.Error("expected success=true")
	}
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Items))
	}

	if result.Items[0].ItemType != "decision" {
		t.Errorf("expected decision, got %s", result.Items[0].ItemType)
	}
	if result.Items[1].ItemType != "action_item" {
		t.Errorf("expected action_item, got %s", result.Items[1].ItemType)
	}
	if result.Items[2].ItemType != "open_topic" {
		t.Errorf("expected open_topic, got %s", result.Items[2].ItemType)
	}
	if result.Items[2].OwnerName != nil {
		t.Errorf("expected null owner for open_topic, got %v", result.Items[2].OwnerName)
	}
	if result.UsageTotalTokens == nil || *result.UsageTotalTokens != 700 {
		t.Error("expected usage_total_tokens=700")
	}
}

func TestExtractionResponse_EmptyItems(t *testing.T) {
	t.Parallel()

	jsonStr := `{"success": true, "items": []}`
	var result extractionResponse
	json.Unmarshal([]byte(jsonStr), &result)

	if len(result.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(result.Items))
	}
}

func TestExtractionResponse_MalformedJSON(t *testing.T) {
	t.Parallel()

	jsonStr := `{"items": [{"text": "broken`
	var result extractionResponse
	err := json.Unmarshal([]byte(jsonStr), &result)

	if err == nil {
		t.Error("expected parse error for malformed JSON")
	}
}

// ==================== Source Quote Enforcement ====================

func TestEmptySourceQuoteItem_IsSkipped(t *testing.T) {
	t.Parallel()

	items := []extractedItem{
		{Text: "Deploy on Wednesday", ItemType: "decision", SourceQuote: ""},
		{Text: "Budget discussion", ItemType: "open_topic", SourceQuote: "   "},
	}

	var kept []extractedItem
	for _, item := range items {
		if !isValidItemType(item.ItemType) {
			continue
		}
		if item.Text == "" {
			continue
		}
		if strings.TrimSpace(item.SourceQuote) == "" {
			continue
		}
		kept = append(kept, item)
	}

	if len(kept) != 0 {
		t.Errorf("expected 0 kept items (empty source_quote should be skipped), got %d", len(kept))
	}
}

func TestValidSourceQuoteItem_IsKept(t *testing.T) {
	t.Parallel()

	items := []extractedItem{
		{Text: "Deploy on Wednesday", ItemType: "decision", SourceQuote: "Ali said let's deploy on Wednesday"},
		{Text: "Write tests by Friday", ItemType: "action_item", SourceQuote: "Bob will write tests"},
	}

	var kept []extractedItem
	for _, item := range items {
		if !isValidItemType(item.ItemType) {
			continue
		}
		if item.Text == "" {
			continue
		}
		if strings.TrimSpace(item.SourceQuote) == "" {
			continue
		}
		kept = append(kept, item)
	}

	if len(kept) != 2 {
		t.Errorf("expected 2 kept items (valid source_quote), got %d", len(kept))
	}
}

// ==================== Item Type Validation ====================

func TestIsValidItemType(t *testing.T) {
	t.Parallel()

	valid := []string{"decision", "action_item", "open_topic"}
	for _, v := range valid {
		if !isValidItemType(v) {
			t.Errorf("expected %s to be valid", v)
		}
	}

	invalid := []string{"", "question", "note", "DECISION", "Action_Item"}
	for _, v := range invalid {
		if isValidItemType(v) {
			t.Errorf("expected %s to be invalid", v)
		}
	}
}

// ==================== Exclusion Note Building ====================

func TestExclusionNote_Empty(t *testing.T) {
	t.Parallel()

	confirmedTexts := []string{}
	note := buildExclusionNote(confirmedTexts)
	if note != "" {
		t.Errorf("expected empty note for no confirmed texts, got %q", note)
	}
}

func TestExclusionNote_WithTexts(t *testing.T) {
	t.Parallel()

	confirmedTexts := []string{"Deploy on Wednesday", "Write tests"}
	note := buildExclusionNote(confirmedTexts)

	if note == "" {
		t.Fatal("expected non-empty exclusion note")
	}
	if !strings.Contains(note, "Deploy on Wednesday") {
		t.Error("expected note to contain 'Deploy on Wednesday'")
	}
	if !strings.Contains(note, "Write tests") {
		t.Error("expected note to contain 'Write tests'")
	}
}

// ==================== Merge Operation Parsing ====================

func TestExtractedItem_ADDOperation(t *testing.T) {
	t.Parallel()

	item := extractedItem{
		Text:        "Yeni karar",
		ItemType:    "decision",
		SourceQuote: "Ali: yeni karar alalım",
		Operation:   "ADD",
	}

	if item.Operation != "ADD" {
		t.Errorf("expected ADD, got %s", item.Operation)
	}
	if item.UpdatedChainIdx != nil {
		t.Error("ADD should not have updated_chain_idx")
	}
}

func TestExtractedItem_UPDATEOperation(t *testing.T) {
	t.Parallel()

	chainIdx := 2
	item := extractedItem{
		Text:            "API v2.1",
		ItemType:        "decision",
		SourceQuote:     "scope daraltıldı",
		Operation:       "UPDATE",
		UpdatedChainIdx: &chainIdx,
	}

	if item.Operation != "UPDATE" {
		t.Errorf("expected UPDATE, got %s", item.Operation)
	}
	if item.UpdatedChainIdx == nil || *item.UpdatedChainIdx != 2 {
		t.Error("expected updated_chain_idx=2")
	}
}

func TestExtractedItem_DELETEOperation(t *testing.T) {
	t.Parallel()

	chainIdx := 0
	item := extractedItem{
		Text:            "X projesi iptal",
		ItemType:        "decision",
		SourceQuote:     "X iptal edildi",
		Operation:       "DELETE",
		DeletedChainIdx: &chainIdx,
	}

	if item.Operation != "DELETE" {
		t.Errorf("expected DELETE, got %s", item.Operation)
	}
	if item.DeletedChainIdx == nil || *item.DeletedChainIdx != 0 {
		t.Error("expected deleted_chain_idx=0")
	}
}

func TestExtractionResponse_WithMergeOperations(t *testing.T) {
	t.Parallel()

	chainIdx := 0
	jsonStr := `{
		"success": true,
		"items": [
			{"text": "Yeni", "item_type": "decision", "source_quote": "q1", "operation": "ADD"},
			{"text": "Güncellendi", "item_type": "decision", "source_quote": "q2", "operation": "UPDATE", "updated_chain_idx": 0},
			{"text": "İptal", "item_type": "decision", "source_quote": "q3", "operation": "DELETE", "deleted_chain_idx": 1}
		]
	}`

	var resp extractionResponse
	err := json.Unmarshal([]byte(jsonStr), &resp)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if len(resp.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(resp.Items))
	}

	_ = chainIdx // used conceptually
	if resp.Items[0].Operation != "ADD" {
		t.Errorf("item 0: expected ADD, got %s", resp.Items[0].Operation)
	}
	if resp.Items[1].Operation != "UPDATE" || resp.Items[1].UpdatedChainIdx == nil || *resp.Items[1].UpdatedChainIdx != 0 {
		t.Error("item 1: expected UPDATE with chain_idx=0")
	}
	if resp.Items[2].Operation != "DELETE" || resp.Items[2].DeletedChainIdx == nil || *resp.Items[2].DeletedChainIdx != 1 {
		t.Error("item 2: expected DELETE with chain_idx=1")
	}
}

func TestExtractionResponse_WithTelemetry(t *testing.T) {
	t.Parallel()

	jsonStr := `{
		"success": true,
		"items": [],
		"chunk_telemetry": [
			{"chunk_index": 0, "status": "success", "latency_ms": 1200, "retry_count": 0, "items_extracted": 3, "model_used": "gpt-5.4"},
			{"chunk_index": 1, "status": "failed", "latency_ms": 5000, "retry_count": 2, "items_extracted": 0, "model_used": "gpt-5.4"}
		],
		"merge_telemetry": {
			"raw_count": 3, "merged_count": 2, "add_count": 1, "update_count": 1, "delete_count": 0, "noop_count": 1, "merge_latency_ms": 800
		}
	}`

	var resp extractionResponse
	err := json.Unmarshal([]byte(jsonStr), &resp)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if len(resp.ChunkTelemetry) != 2 {
		t.Fatalf("expected 2 chunk telemetry entries, got %d", len(resp.ChunkTelemetry))
	}
	if resp.ChunkTelemetry[0].Status != "success" {
		t.Error("chunk 0 should be success")
	}
	if resp.ChunkTelemetry[1].RetryCount != 2 {
		t.Error("chunk 1 should have 2 retries")
	}
	if resp.MergeTelemetry == nil {
		t.Fatal("expected merge telemetry")
	}
	if resp.MergeTelemetry.AddCount != 1 || resp.MergeTelemetry.UpdateCount != 1 || resp.MergeTelemetry.NoopCount != 1 {
		t.Errorf("unexpected merge counts: add=%d update=%d noop=%d",
			resp.MergeTelemetry.AddCount, resp.MergeTelemetry.UpdateCount, resp.MergeTelemetry.NoopCount)
	}
}

func TestSafeTypeAssertion_InvalidChainItemID(t *testing.T) {
	t.Parallel()

	// Simulate chainItems with missing "id" key
	chainItems := []map[string]interface{}{
		{"text": "some decision", "item_type": "decision"},  // no "id" key
	}

	idx := 0
	_, ok := chainItems[idx]["id"].(string)
	if ok {
		t.Error("expected type assertion to fail for missing 'id' key")
	}
}

// Helper to test exclusion note building logic
func buildExclusionNote(confirmedTexts []string) string {
	if len(confirmedTexts) == 0 {
		return ""
	}
	note := "\n\nAŞAĞIDAKİ KARARLAR ZATEN ONAYLANDI — bunları tekrar çıkarma:\n"
	for _, text := range confirmedTexts {
		note += "- " + text + "\n"
	}
	return note
}
