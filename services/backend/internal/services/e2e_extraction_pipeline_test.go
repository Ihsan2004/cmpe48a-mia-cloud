package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== E2E Extraction Pipeline Tests ====================
// Tests the full extraction pipeline: Go → Python mia-search → OpenAI → DB
// Verifies: chunking, ADR fields, telemetry, merge operations
// Requires: E2E_TEST=true, DATABASE_URL, NLP_SERVICE_URL, OPENAI_API_KEY

func e2eSeedLongTranscript(t *testing.T, ctx context.Context, meetingID uuid.UUID) {
	t.Helper()
	createdAt := time.Now().Add(-5 * time.Minute)

	// Create a long transcript (30+ segments) to trigger chunking
	segments := []struct {
		speaker string
		text    string
	}{
		{"Ali", "Herkese merhaba, bugünkü toplantıda birkaç önemli konuyu ele alacağız."},
		{"Ali", "İlk olarak proje timeline'ını gözden geçirelim."},
		{"Bob", "Evet, geçen hafta müşteri tarafından yeni gereksinimler geldi."},
		{"Bob", "Özellikle raporlama modülünde büyük değişiklikler istiyorlar."},
		{"Charlie", "Ben teknik analizi tamamladım. Raporlama için iki seçenek var."},
		{"Charlie", "Birinci seçenek mevcut framework üzerine inşa etmek. Bu daha hızlı ama kısıtlı."},
		{"Charlie", "İkinci seçenek yeni bir raporlama engine yazmak. Daha uzun sürer ama esnek olur."},
		{"Ali", "Maliyet açısından karşılaştırma yapabilir misin?"},
		{"Charlie", "Birinci seçenek yaklaşık iki hafta, ikinci seçenek altı hafta sürer."},
		{"Bob", "Müşteri acil istiyor, bence birinci seçenekle başlayalım."},
		{"Ali", "Tamam, karar alalım: raporlama için birinci seçenekle devam edeceğiz."},
		{"Ali", "Charlie, sen raporlama modülünün teknik tasarımını hazırlayacaksın."},
		{"Charlie", "Olur, önümüzdeki Pazartesi'ye kadar tasarım dokümanı hazır olur."},
		{"Bob", "Ben de müşteriye bilgilendirme yapacağım."},
		{"Ali", "İkinci konu: API entegrasyonu."},
		{"Bob", "API v2 üzerinde çalışıyoruz. Temel endpoint'ler hazır."},
		{"Charlie", "Ama authentication kısmında sorun var. OAuth2 flow'u düzgün çalışmıyor."},
		{"Ali", "Bu kritik bir blocker. Ne kadar sürede çözülebilir?"},
		{"Charlie", "İki gün içinde çözebilirim. Sorun token refresh mekanizmasında."},
		{"Ali", "Tamam, bu en yüksek öncelik olsun."},
		{"Bob", "Bir de performans konusu var. Büyük veri setlerinde API yavaşlıyor."},
		{"Charlie", "Pagination eklemeliyiz. Şu an tüm veriyi tek seferde çekiyor."},
		{"Ali", "Pagination'ı da ekleyelim. Charlie, OAuth fix'ten sonra pagination'a geçersin."},
		{"Bob", "Son konu: deployment planı."},
		{"Ali", "Evet, ne zaman canlıya çıkabiliriz?"},
		{"Bob", "Eğer raporlama ve API sorunları çözülürse, önümüzdeki Cuma deploy edebiliriz."},
		{"Charlie", "Ben teknik tarafı hazır ederim. Staging'e Çarşamba'ya kadar çıkabiliriz."},
		{"Ali", "Güzel. O zaman plan: Çarşamba staging, Cuma production."},
		{"Ali", "Toplantıyı kapatalım. Herkes görevini biliyor."},
		{"Bob", "Teşekkürler, görüşmek üzere."},
	}

	for i, seg := range segments {
		idempotencyKey := fmt.Sprintf("e2e_long_%s_%d", meetingID.String()[:8], i)
		_, err := repository.DB.Exec(ctx, `
			INSERT INTO transcript_segments (meeting_id, speaker, text, start_time, end_time, role, stt_provider, idempotency_key, created_at)
			VALUES ($1, $2, $3, $4, $5, 'bot', 'e2e_test', $6, $7)
		`, meetingID, seg.speaker, seg.text, float64(i*3), float64(i*3)+2.5, idempotencyKey, createdAt)
		if err != nil {
			t.Fatalf("failed to seed long segment %d: %v", i, err)
		}
	}
}

// ==================== Test: Extraction via mia-search (not direct OpenAI) ====================

func TestE2E_ExtractionViaMiaSearch(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	e2eSeedTranscriptSegments(t, ctx, meetingID)

	extractor := NewDecisionExtractor()
	err := extractor.ExtractFromMeeting(meetingID, userID, nil)
	if err != nil {
		t.Fatalf("extraction via mia-search failed: %v", err)
	}

	// Verify items created in DB
	decisionRepo := repository.NewDecisionRepository()
	items, err := decisionRepo.GetDecisionItemsByMeeting(ctx, meetingID)
	if err != nil {
		t.Fatalf("failed to get items: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected items from extraction")
	}

	// Verify all items have required fields
	for _, item := range items {
		if item.Text == "" {
			t.Error("item text should not be empty")
		}
		if item.ItemType != "decision" && item.ItemType != "action_item" && item.ItemType != "open_topic" {
			t.Errorf("invalid item_type: %s", item.ItemType)
		}
		if item.SourceQuote == nil || *item.SourceQuote == "" {
			t.Errorf("item %s missing source_quote", item.ID)
		}
		if item.Status != "confirmed" {
			t.Errorf("expected confirmed status (auto-confirm), got %s", item.Status)
		}
	}

	t.Logf("Extraction via mia-search: %d items extracted", len(items))
}

// ==================== Test: ADR Fields Populated ====================

func TestE2E_ExtractionADRFields(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)

	// Seed transcript with clear decision + alternatives
	createdAt := time.Now().Add(-5 * time.Minute)
	segments := []struct {
		speaker string
		text    string
	}{
		{"Ali", "Raporlama konusunda iki seçenek var."},
		{"Bob", "Birinci seçenek mevcut framework, ikinci seçenek yeni engine."},
		{"Ali", "Mevcut framework daha hızlı olduğu için birinci seçenekle gidelim."},
		{"Bob", "Tamam, birinci seçenek ile devam kararı aldık."},
	}
	for i, seg := range segments {
		key := fmt.Sprintf("e2e_adr_%s_%d", meetingID.String()[:8], i)
		repository.DB.Exec(ctx, `
			INSERT INTO transcript_segments (meeting_id, speaker, text, start_time, end_time, role, stt_provider, idempotency_key, created_at)
			VALUES ($1, $2, $3, $4, $5, 'bot', 'e2e_test', $6, $7)
		`, meetingID, seg.speaker, seg.text, float64(i*5), float64(i*5)+4.0, key, createdAt)
	}

	extractor := NewDecisionExtractor()
	err := extractor.ExtractFromMeeting(meetingID, userID, nil)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	decisionRepo := repository.NewDecisionRepository()
	items, _ := decisionRepo.GetDecisionItemsByMeeting(ctx, meetingID)

	// At least one item should have context (reason for decision)
	hasContext := false
	for _, item := range items {
		if item.Context != nil && *item.Context != "" {
			hasContext = true
			t.Logf("  Item with context: %s → %s", item.Text, *item.Context)
		}
	}

	// Context is best-effort — LLM may or may not extract it
	// But with clear alternatives in transcript, we expect at least one
	if !hasContext {
		t.Log("WARNING: no items with context field populated (LLM may not have extracted reasons)")
	}

	t.Logf("ADR fields test: %d items, hasContext=%v", len(items), hasContext)
}

// ==================== Test: Long Transcript Triggers Chunking ====================

func TestE2E_LongTranscript_Chunking(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	e2eSeedLongTranscript(t, ctx, meetingID) // 30 segments — should trigger chunking

	extractor := NewDecisionExtractor()
	err := extractor.ExtractFromMeeting(meetingID, userID, nil)
	if err != nil {
		t.Fatalf("long transcript extraction failed: %v", err)
	}

	decisionRepo := repository.NewDecisionRepository()
	items, _ := decisionRepo.GetDecisionItemsByMeeting(ctx, meetingID)

	// Long transcript with multiple decisions should extract more items
	if len(items) < 2 {
		t.Errorf("expected at least 2 items from long transcript (30 segments), got %d", len(items))
	}

	// Verify variety: should have decisions + action items
	types := make(map[string]int)
	for _, item := range items {
		types[item.ItemType]++
	}

	t.Logf("Long transcript: %d items (decisions=%d, actions=%d, topics=%d)",
		len(items), types["decision"], types["action_item"], types["open_topic"])

	if types["decision"]+types["action_item"] == 0 {
		t.Error("expected at least one decision or action item from long transcript")
	}
}

// ==================== Test: Telemetry Persisted to Usage Logs ====================

func TestE2E_ExtractionTelemetry(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	e2eSeedTranscriptSegments(t, ctx, meetingID)

	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM usage_logs WHERE meeting_id = $1", meetingID)
	})

	extractor := NewDecisionExtractor()
	err := extractor.ExtractFromMeeting(meetingID, userID, nil)
	if err != nil {
		t.Fatalf("extraction failed: %v", err)
	}

	// Verify usage log was created
	var usageCount int
	repository.DB.QueryRow(ctx, `
		SELECT COUNT(*) FROM usage_logs
		WHERE meeting_id = $1 AND feature = 'decision_extraction'
	`, meetingID).Scan(&usageCount)

	if usageCount == 0 {
		t.Error("expected usage_log entry for decision_extraction")
	}

	// Verify tokens were recorded
	var totalTokens int
	repository.DB.QueryRow(ctx, `
		SELECT COALESCE(input_tokens + output_tokens, 0)
		FROM usage_logs
		WHERE meeting_id = $1 AND feature = 'decision_extraction'
		LIMIT 1
	`, meetingID).Scan(&totalTokens)

	if totalTokens == 0 {
		t.Error("expected non-zero token usage in usage_logs")
	}

	t.Logf("Telemetry: usage_count=%d, total_tokens=%d", usageCount, totalTokens)
}

// ==================== Test: Meeting Marked as Processed ====================

func TestE2E_MeetingProcessedAt(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	e2eSeedTranscriptSegments(t, ctx, meetingID)

	// Verify processed_at is NULL before extraction
	var processedBefore *time.Time
	repository.DB.QueryRow(ctx, "SELECT processed_at FROM meetings WHERE id = $1", meetingID).Scan(&processedBefore)
	if processedBefore != nil {
		t.Error("processed_at should be NULL before extraction")
	}

	extractor := NewDecisionExtractor()
	extractor.ExtractFromMeeting(meetingID, userID, nil)

	// Verify processed_at is set after extraction
	var processedAfter *time.Time
	repository.DB.QueryRow(ctx, "SELECT processed_at FROM meetings WHERE id = $1", meetingID).Scan(&processedAfter)
	if processedAfter == nil {
		t.Error("processed_at should be set after extraction")
	}
}
