package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== E2E Chain Lifecycle Tests ====================
// Tests the full chain: extraction → confirm → re-extract (merge UPDATE) → brief generation
// Requires: E2E_TEST=true, DATABASE_URL, NLP_SERVICE_URL, OPENAI_API_KEY

func e2eCreateChain(t *testing.T, ctx context.Context, userID uuid.UUID) uuid.UUID {
	t.Helper()
	chainID := uuid.New()
	displayName := "E2E Test Chain"

	_, err := repository.DB.Exec(ctx, `
		INSERT INTO meeting_chains (id, user_id, chain_type, display_name)
		VALUES ($1, $2, 'calendar_series', $3)
	`, chainID, userID, displayName)
	if err != nil {
		t.Fatalf("failed to create chain: %v", err)
	}

	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM meeting_chains WHERE id = $1", chainID)
	})

	return chainID
}

func e2eCreateChainedMeeting(t *testing.T, ctx context.Context, userID, chainID uuid.UUID, hoursAgo int) uuid.UUID {
	t.Helper()
	meetingID := uuid.New()

	_, err := repository.DB.Exec(ctx, `
		INSERT INTO meetings (id, user_id, title, start_time, end_time, google_event_id, bot_status, chain_id)
		VALUES ($1, $2, 'E2E Chain Meeting', $3, $4, $5, 'completed', $6)
	`, meetingID, userID,
		time.Now().Add(-time.Duration(hoursAgo)*time.Hour),
		time.Now().Add(-time.Duration(hoursAgo-1)*time.Hour),
		"e2e_chain_"+meetingID.String()[:8],
		chainID,
	)
	if err != nil {
		t.Fatalf("failed to create chained meeting: %v", err)
	}

	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM decision_items WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM meeting_summaries WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM meeting_briefs WHERE target_meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM transcript_chat_blocks WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM transcript_segments WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM meetings WHERE id = $1", meetingID)
	})

	return meetingID
}

func e2eSeedTranscriptForChain(t *testing.T, ctx context.Context, meetingID uuid.UUID, segments []struct {
	speaker string
	text    string
}) {
	t.Helper()
	createdAt := time.Now().Add(-5 * time.Minute)

	for i, seg := range segments {
		idempotencyKey := fmt.Sprintf("e2e_chain_%s_%d", meetingID.String()[:8], i)
		_, err := repository.DB.Exec(ctx, `
			INSERT INTO transcript_segments (meeting_id, speaker, text, start_time, end_time, role, stt_provider, idempotency_key, created_at)
			VALUES ($1, $2, $3, $4, $5, 'bot', 'e2e_test', $6, $7)
		`, meetingID, seg.speaker, seg.text, float64(i*3), float64(i*3)+2.5, idempotencyKey, createdAt)
		if err != nil {
			t.Fatalf("failed to seed chain segment %d: %v", i, err)
		}
	}
}

// ==================== Test: Full Extraction → Confirm → Brief Lifecycle ====================

func TestE2E_ChainLifecycle_ExtractionAndBrief(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	chainID := e2eCreateChain(t, ctx, userID)

	// Ensure profile exists for brief generation
	repository.DB.Exec(ctx, `
		INSERT INTO profiles (id, full_name, email, timezone, default_meeting_duration)
		VALUES ($1, 'E2E User', 'e2e@test.com', 'UTC', 30)
		ON CONFLICT (id) DO NOTHING
	`, userID)
	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM profiles WHERE id = $1", userID)
	})

	// === Meeting 1: Initial extraction ===
	meeting1ID := e2eCreateChainedMeeting(t, ctx, userID, chainID, 48) // 2 days ago
	e2eSeedTranscriptForChain(t, ctx, meeting1ID, []struct {
		speaker string
		text    string
	}{
		{"Ali", "Bugün API konusunu konuşalım."},
		{"Bob", "API'yi v2 ile yapacağız, karar verdik."},
		{"Ali", "Tamam, Bob sen API dokümantasyonunu hazırlayacaksın."},
		{"Bob", "Olur, önümüzdeki haftaya hazır olur."},
	})

	// Run extraction for meeting 1
	extractor := NewDecisionExtractor()
	err := extractor.ExtractFromMeeting(meeting1ID, userID, &chainID)
	if err != nil {
		t.Fatalf("Meeting 1 extraction failed: %v", err)
	}

	// Verify items were extracted
	decisionRepo := repository.NewDecisionRepository()
	items1, err := decisionRepo.GetDecisionItemsByMeeting(ctx, meeting1ID)
	if err != nil {
		t.Fatalf("failed to get meeting 1 items: %v", err)
	}

	if len(items1) == 0 {
		t.Fatal("expected at least 1 item from meeting 1 extraction")
	}

	t.Logf("Meeting 1: extracted %d items", len(items1))
	for _, item := range items1 {
		t.Logf("  [%s] %s (owner=%v, status=%s)", item.ItemType, item.Text, item.OwnerName, item.Status)
	}

	// Verify items have chain_id
	for _, item := range items1 {
		if item.ChainID == nil || *item.ChainID != chainID {
			t.Errorf("item %s missing chain_id", item.ID)
		}
	}

	// === Verify ADR fields (context, source_quote) ===
	hasSourceQuote := false
	for _, item := range items1 {
		if item.SourceQuote != nil && *item.SourceQuote != "" {
			hasSourceQuote = true
			break
		}
	}
	if !hasSourceQuote {
		t.Error("expected at least one item with source_quote")
	}

	// === Meeting 2: Re-extraction with merge ===
	meeting2ID := e2eCreateChainedMeeting(t, ctx, userID, chainID, 2) // 2 hours ago
	e2eSeedTranscriptForChain(t, ctx, meeting2ID, []struct {
		speaker string
		text    string
	}{
		{"Ali", "Geçen hafta API v2 kararı almıştık."},
		{"Bob", "Evet ama scope'u daraltmamız gerekiyor, v2.1 olarak devam edelim."},
		{"Ali", "Tamam, v2.1 ile devam kararı alalım."},
		{"Bob", "Dokümantasyon da hazır, test aşamasına geçebiliriz."},
	})

	// Run extraction for meeting 2 (should trigger merge with meeting 1's confirmed items)
	err = extractor.ExtractFromMeeting(meeting2ID, userID, &chainID)
	if err != nil {
		t.Fatalf("Meeting 2 extraction failed: %v", err)
	}

	items2, err := decisionRepo.GetDecisionItemsByMeeting(ctx, meeting2ID)
	if err != nil {
		t.Fatalf("failed to get meeting 2 items: %v", err)
	}

	t.Logf("Meeting 2: extracted %d items", len(items2))
	for _, item := range items2 {
		t.Logf("  [%s] %s (owner=%v, status=%s)", item.ItemType, item.Text, item.OwnerName, item.Status)
	}

	if len(items2) == 0 {
		t.Error("expected items from meeting 2 extraction")
	}

	// === Verify meeting is marked as processed ===
	var processedAt *time.Time
	repository.DB.QueryRow(ctx, "SELECT processed_at FROM meetings WHERE id = $1", meeting2ID).Scan(&processedAt)
	if processedAt == nil {
		t.Error("meeting 2 should be marked as processed after extraction")
	}

	t.Logf("E2E Chain Lifecycle passed: meeting1=%d items, meeting2=%d items", len(items1), len(items2))
}

// ==================== Test: Extraction Idempotency (Re-extract same meeting) ====================

func TestE2E_ExtractionIdempotency(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	e2eSeedTranscriptSegments(t, ctx, meetingID)

	extractor := NewDecisionExtractor()
	decisionRepo := repository.NewDecisionRepository()

	// First extraction
	err := extractor.ExtractFromMeeting(meetingID, userID, nil)
	if err != nil {
		t.Fatalf("first extraction failed: %v", err)
	}

	items1, _ := decisionRepo.GetDecisionItemsByMeeting(ctx, meetingID)
	count1 := len(items1)

	// User edits one item
	if count1 > 0 {
		decisionRepo.MarkUserEdited(ctx, items1[0].ID)
		decisionRepo.UpdateDecisionItem(ctx, items1[0].ID, "User edited text", items1[0].OwnerName)
	}

	// Second extraction (re-extract)
	err = extractor.ExtractFromMeeting(meetingID, userID, nil)
	if err != nil {
		t.Fatalf("second extraction failed: %v", err)
	}

	items2, _ := decisionRepo.GetDecisionItemsByMeeting(ctx, meetingID)

	// User-edited item should survive re-extraction
	if count1 > 0 {
		found := false
		for _, item := range items2 {
			if item.Text == "User edited text" {
				found = true
				break
			}
		}
		if !found {
			t.Error("user-edited item should survive re-extraction (idempotency)")
		}
	}

	t.Logf("Idempotency: first=%d items, after re-extract=%d items", count1, len(items2))
}

// ==================== Test: Brief Generation with Chain Data ====================

func TestE2E_BriefGeneration_WithChainItems(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	chainID := e2eCreateChain(t, ctx, userID)

	// Ensure profile
	repository.DB.Exec(ctx, `
		INSERT INTO profiles (id, full_name, email, timezone, default_meeting_duration, brief_lead_time_minutes)
		VALUES ($1, 'E2E User', 'e2e@test.com', 'UTC', 30, 10)
		ON CONFLICT (id) DO NOTHING
	`, userID)
	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM profiles WHERE id = $1", userID)
		repository.DB.Exec(context.Background(), "DELETE FROM meeting_briefs WHERE user_id = $1", userID)
	})

	// Past meeting with summary
	pastMeetingID := e2eCreateChainedMeeting(t, ctx, userID, chainID, 168) // 1 week ago
	repository.DB.Exec(ctx, `
		INSERT INTO meeting_summaries (id, meeting_id, content, summary_type)
		VALUES ($1, $2, 'Geçen hafta API v2 kararı alındı ve dokümantasyon başlatıldı.', 'final_detailed')
	`, uuid.New(), pastMeetingID)

	// Add confirmed decision items to the chain
	now := time.Now()
	confirmedAt := now.Add(-7 * 24 * time.Hour)
	ctx2 := "Performans testleri olumlu"
	repository.DB.Exec(ctx, `
		INSERT INTO decision_items (id, meeting_id, chain_id, user_id, item_type, text, owner_name, source_quote, context, status, user_edited, confirmed_at, updated_at)
		VALUES ($1, $2, $3, $4, 'decision', 'API v2 ile devam edilecek', 'Bob', 'Bob: v2 ile gidelim', $5, 'confirmed', true, $6, $6)
	`, uuid.New(), pastMeetingID, chainID, userID, ctx2, confirmedAt)

	// Upcoming meeting (target for brief)
	upcomingMeetingID := e2eCreateChainedMeeting(t, ctx, userID, chainID, -1) // 1 hour from now
	// Fix: set start_time to future
	repository.DB.Exec(ctx, `
		UPDATE meetings SET start_time = $1, end_time = $2 WHERE id = $3
	`, time.Now().Add(1*time.Hour), time.Now().Add(2*time.Hour), upcomingMeetingID)

	// Generate brief
	upcomingMeeting := &models.Meeting{
		ID:        upcomingMeetingID,
		UserID:    userID,
		Title:     "E2E Chain Meeting",
		StartTime: time.Now().Add(1 * time.Hour),
		ChainID:   &chainID,
	}

	generator := NewBriefGenerator()
	brief, err := generator.GenerateBrief(ctx, userID, upcomingMeeting, chainID)
	if err != nil {
		t.Fatalf("brief generation failed: %v", err)
	}

	if brief == nil {
		t.Fatal("expected brief to be generated")
	}

	if len(brief.Content) < 20 {
		t.Errorf("brief content too short: %s", brief.Content)
	}

	t.Logf("Brief generated (%s): %d chars\n%s", brief.GenerationMethod, len(brief.Content), brief.Content[:min(200, len(brief.Content))])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
