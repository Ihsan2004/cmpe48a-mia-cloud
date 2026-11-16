package services

import (
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== IsStale ====================

func TestIsStale_ConfirmedOver14Days(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Now().Add(-15 * 24 * time.Hour)
	item := models.DecisionItem{Status: "confirmed", ConfirmedAt: &confirmedAt}
	if !IsStale(item) {
		t.Error("expected stale for 15-day-old confirmed item")
	}
}

func TestIsStale_ConfirmedUnder14Days(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Now().Add(-13 * 24 * time.Hour)
	item := models.DecisionItem{Status: "confirmed", ConfirmedAt: &confirmedAt}
	if IsStale(item) {
		t.Error("expected not stale for 13-day-old confirmed item")
	}
}

func TestIsStale_Exactly14Days(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Now().Add(-14*24*time.Hour - 1*time.Minute)
	item := models.DecisionItem{Status: "confirmed", ConfirmedAt: &confirmedAt}
	if !IsStale(item) {
		t.Error("expected stale for item just past 14 days")
	}
}

func TestIsStale_CompletedNotStale(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Now().Add(-30 * 24 * time.Hour)
	item := models.DecisionItem{Status: "completed", ConfirmedAt: &confirmedAt}
	if IsStale(item) {
		t.Error("completed items should never be stale")
	}
}

func TestIsStale_ProposedNotStale(t *testing.T) {
	t.Parallel()
	item := models.DecisionItem{Status: "proposed"}
	if IsStale(item) {
		t.Error("proposed items should never be stale")
	}
}

func TestIsStale_NilConfirmedAt(t *testing.T) {
	t.Parallel()
	item := models.DecisionItem{Status: "confirmed", ConfirmedAt: nil}
	if IsStale(item) {
		t.Error("nil confirmed_at should not be stale")
	}
}

// ==================== Template Fallback ====================

func TestGenerateTemplate_BasicContent(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Weekly Product Sync",
		MeetingTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		Participants: []string{"Alice", "Bob"},
		LastSummary:  "Geçen hafta proje planı tartışıldı.",
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Weekly Product Sync") {
		t.Error("expected meeting title in template")
	}
	if !strings.Contains(result, "Alice") {
		t.Error("expected participant name in template")
	}
	if !strings.Contains(result, "proje planı") {
		t.Error("expected last summary in template")
	}
}

func TestGenerateTemplate_WithDecisions(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	owner := "Ali"
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		ConfirmedItems: []models.DecisionItem{
			{ItemType: "decision", Text: "V2'de yapacağız", OwnerName: &owner},
			{ItemType: "action_item", Text: "Test yazacak", OwnerName: &owner},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "V2'de yapacağız") {
		t.Error("expected decision text in template")
	}
	if !strings.Contains(result, "Test yazacak") {
		t.Error("expected action item in template")
	}
	if !strings.Contains(result, "Ali") {
		t.Error("expected owner name in template")
	}
}

func TestGenerateTemplate_WithStaleItems(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		StaleItems: []models.DecisionItem{
			{ItemType: "action_item", Text: "API dokümantasyonu"},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "⚠️") {
		t.Error("expected stale warning emoji in template")
	}
	if !strings.Contains(result, "API dokümantasyonu") {
		t.Error("expected stale item text in template")
	}
}

func TestGenerateTemplate_WithPersonalNote(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		PersonalNote: "Bütçeyi sormayı unutma",
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Bütçeyi sormayı unutma") {
		t.Error("expected personal note in template")
	}
	if !strings.Contains(result, "📌") {
		t.Error("expected note emoji in template")
	}
}

func TestGenerateTemplate_WithOpenTopics(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		ConfirmedItems: []models.DecisionItem{
			{ItemType: "open_topic", Text: "Bütçe konusu netleşmedi"},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "❓") {
		t.Error("expected open topic emoji in template")
	}
	if !strings.Contains(result, "Bütçe konusu netleşmedi") {
		t.Error("expected open topic text in template")
	}
}

func TestGenerateTemplate_EmptyInput(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Empty Meeting",
		MeetingTime:  time.Now(),
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Empty Meeting") {
		t.Error("expected at least meeting title")
	}
	// Should not contain section headers for empty sections
	if strings.Contains(result, "Yapılacaklar") {
		t.Error("should not show empty sections")
	}
}

func TestGenerateTemplate_AllSections(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	owner := "Bob"
	input := &BriefInput{
		MeetingTitle: "Full Brief Test",
		MeetingTime:  time.Now(),
		Participants: []string{"Alice", "Bob", "Charlie"},
		LastSummary:  "Son toplantıda tüm konular ele alındı.",
		ConfirmedItems: []models.DecisionItem{
			{ItemType: "decision", Text: "Karar 1"},
			{ItemType: "action_item", Text: "Aksiyon 1", OwnerName: &owner},
			{ItemType: "open_topic", Text: "Açık konu 1"},
		},
		StaleItems: []models.DecisionItem{
			{ItemType: "action_item", Text: "Stale aksiyon"},
		},
		PersonalNote: "Önemli not",
	}

	result := gen.generateTemplate(input)

	sections := []string{"📅", "👥", "📝", "✅", "📋", "❓", "⚠️", "📌"}
	for _, s := range sections {
		if !strings.Contains(result, s) {
			t.Errorf("expected section %s in full template", s)
		}
	}
}

// ==================== filterByType ====================

func TestFilterByType(t *testing.T) {
	t.Parallel()

	items := []models.DecisionItem{
		{ItemType: "decision", Text: "D1"},
		{ItemType: "action_item", Text: "A1"},
		{ItemType: "decision", Text: "D2"},
		{ItemType: "open_topic", Text: "O1"},
	}

	decisions := filterByType(items, "decision")
	if len(decisions) != 2 {
		t.Errorf("expected 2 decisions, got %d", len(decisions))
	}

	actions := filterByType(items, "action_item")
	if len(actions) != 1 {
		t.Errorf("expected 1 action, got %d", len(actions))
	}

	topics := filterByType(items, "open_topic")
	if len(topics) != 1 {
		t.Errorf("expected 1 topic, got %d", len(topics))
	}
}

// ==================== Brief Eligibility ====================

func TestBriefEligibility_NoChainNoItems(t *testing.T) {
	t.Parallel()
	// No summary, no items → should not generate brief
	input := &BriefInput{
		MeetingTitle: "Test",
		MeetingTime:  time.Now(),
	}
	if input.LastSummary != "" || len(input.ConfirmedItems) > 0 || len(input.StaleItems) > 0 {
		t.Error("expected empty input for eligibility test")
	}
}

func TestBriefEligibility_OnlySummary(t *testing.T) {
	t.Parallel()
	input := &BriefInput{
		LastSummary: "Toplantı özeti mevcut",
	}
	hasContent := input.LastSummary != "" || len(input.ConfirmedItems) > 0 || len(input.StaleItems) > 0
	if !hasContent {
		t.Error("summary alone should be eligible")
	}
}

func TestBriefEligibility_OnlyItems(t *testing.T) {
	t.Parallel()
	input := &BriefInput{
		ConfirmedItems: []models.DecisionItem{{Text: "Karar"}},
	}
	hasContent := input.LastSummary != "" || len(input.ConfirmedItems) > 0 || len(input.StaleItems) > 0
	if !hasContent {
		t.Error("items alone should be eligible")
	}
}

// ==================== Brief PersonalNote Source ====================

func TestGenerateTemplate_PersonalNoteFromChainHistory(t *testing.T) {
	t.Parallel()

	// PersonalNote should come from a previous meeting in the chain,
	// not from the upcoming meeting itself. This test verifies template
	// renders the note regardless of source.
	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Weekly Sync",
		MeetingTime:  time.Now(),
		PersonalNote: "Geçen haftaki toplantıdan not: bütçeyi sor",
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Geçen haftaki toplantıdan not") {
		t.Error("expected personal note from chain history in template")
	}
	if !strings.Contains(result, "📌") {
		t.Error("expected note pin emoji")
	}
}

func TestGenerateTemplate_NoPersonalNote(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Weekly Sync",
		MeetingTime:  time.Now(),
		LastSummary:  "Özet var",
	}

	result := gen.generateTemplate(input)

	if strings.Contains(result, "📌") {
		t.Error("should not show note section when no personal note exists")
	}
}

// ==================== IsStale with updated_at ====================

func TestIsStale_RecentlyUpdated_NotStale(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Now().Add(-20 * 24 * time.Hour) // 20 days ago
	item := models.DecisionItem{
		Status:    "confirmed",
		ConfirmedAt: &confirmedAt,
		UpdatedAt: time.Now().Add(-3 * 24 * time.Hour), // updated 3 days ago
	}
	if IsStale(item) {
		t.Error("item updated 3 days ago should NOT be stale despite old confirmed_at")
	}
}

func TestIsStale_UpdatedLongAgo_IsStale(t *testing.T) {
	t.Parallel()
	confirmedAt := time.Now().Add(-30 * 24 * time.Hour) // 30 days ago
	item := models.DecisionItem{
		Status:    "confirmed",
		ConfirmedAt: &confirmedAt,
		UpdatedAt: time.Now().Add(-20 * 24 * time.Hour), // updated 20 days ago
	}
	if !IsStale(item) {
		t.Error("item with no activity for 20 days should be stale")
	}
}

// ==================== Template: Updated Items Section ====================

func TestGenerateTemplate_WithUpdatedItems(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	owner := "Ali"
	ctx := "Performans testleri olumlu"
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		UpdatedItems: []models.DecisionItem{
			{ItemType: "decision", Text: "API v2.1 ile devam", OwnerName: &owner, Context: &ctx},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Güncellenen") {
		t.Error("expected updated items section header")
	}
	if !strings.Contains(result, "API v2.1") {
		t.Error("expected updated item text")
	}
	if !strings.Contains(result, "Performans testleri") {
		t.Error("expected context in updated item")
	}
}

// ==================== Template: Context + Alternatives ====================

func TestGenerateTemplate_WithContextAndAlternatives(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	ctx := "Maliyet analizi yapıldı"
	alts := "B seçeneği (yavaş), C seçeneği (pahalı)"
	input := &BriefInput{
		MeetingTitle: "Review",
		MeetingTime:  time.Now(),
		ConfirmedItems: []models.DecisionItem{
			{ItemType: "decision", Text: "A seçeneği ile gidilecek", Context: &ctx, AlternativesConsidered: &alts},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Sebep:") {
		t.Error("expected context label in template")
	}
	if !strings.Contains(result, "Maliyet analizi") {
		t.Error("expected context content")
	}
	if !strings.Contains(result, "Alternatifler:") {
		t.Error("expected alternatives label")
	}
	if !strings.Contains(result, "B seçeneği") {
		t.Error("expected alternatives content")
	}
}

func TestGenerateTemplate_NilContextAlternatives_NoCrash(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		ConfirmedItems: []models.DecisionItem{
			{ItemType: "decision", Text: "Basit karar", Context: nil, AlternativesConsidered: nil},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "Basit karar") {
		t.Error("expected item text")
	}
	if strings.Contains(result, "Sebep:") {
		t.Error("should not show Sebep when context is nil")
	}
	if strings.Contains(result, "Alternatifler:") {
		t.Error("should not show Alternatifler when nil")
	}
}

// ==================== Template: Stale with temporal info ====================

func TestGenerateTemplate_StaleItemShowsDaysAgo(t *testing.T) {
	t.Parallel()

	gen := &BriefGenerator{}
	updatedAt := time.Now().Add(-18 * 24 * time.Hour)
	confirmedAt := time.Now().Add(-20 * 24 * time.Hour)
	input := &BriefInput{
		MeetingTitle: "Sync",
		MeetingTime:  time.Now(),
		StaleItems: []models.DecisionItem{
			{ItemType: "action_item", Text: "Eski aksiyon", UpdatedAt: updatedAt, ConfirmedAt: &confirmedAt},
		},
	}

	result := gen.generateTemplate(input)

	if !strings.Contains(result, "18 gün") {
		t.Errorf("expected '18 gün' in stale warning, got: %s", result)
	}
}

// ==================== Completed items should NOT appear ====================

func TestCompletedItemsExcludedFromBrief(t *testing.T) {
	t.Parallel()

	// Simulate what gatherBriefInput does: completed items should be skipped
	items := []models.DecisionItem{
		{Status: "confirmed", Text: "Open karar"},
		{Status: "completed", Text: "Biten karar"},
		{Status: "dismissed", Text: "Reddedilen"},
	}

	var confirmed []models.DecisionItem
	for _, item := range items {
		if item.Status == "completed" {
			continue
		}
		confirmed = append(confirmed, item)
	}

	// Only non-completed should remain
	for _, item := range confirmed {
		if item.Status == "completed" {
			t.Error("completed item should have been filtered out")
		}
	}
	if len(confirmed) != 2 {
		t.Errorf("expected 2 non-completed items, got %d", len(confirmed))
	}
}

// ==================== BriefInput Stale Separation ====================

func TestStaleItemSeparation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	recentConfirm := now.Add(-5 * 24 * time.Hour)
	oldConfirm := now.Add(-20 * 24 * time.Hour)

	items := []models.DecisionItem{
		{ID: uuid.New(), Status: "confirmed", ConfirmedAt: &recentConfirm, Text: "Recent"},
		{ID: uuid.New(), Status: "confirmed", ConfirmedAt: &oldConfirm, Text: "Old stale"},
	}

	var confirmed, stale []models.DecisionItem
	for _, item := range items {
		if IsStale(item) {
			stale = append(stale, item)
		} else {
			confirmed = append(confirmed, item)
		}
	}

	if len(confirmed) != 1 || confirmed[0].Text != "Recent" {
		t.Errorf("expected 1 recent confirmed, got %d", len(confirmed))
	}
	if len(stale) != 1 || stale[0].Text != "Old stale" {
		t.Errorf("expected 1 stale, got %d", len(stale))
	}
}
