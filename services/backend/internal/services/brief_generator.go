package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// BriefGenerator creates pre-meeting briefs using LLM with template fallback
type BriefGenerator struct {
	decisionRepo repository.DecisionRepository
	briefRepo    repository.BriefRepository
	noteRepo     repository.NoteRepository
	usageRecorder *UsageRecorder
}

func NewBriefGenerator() *BriefGenerator {
	return &BriefGenerator{
		decisionRepo:  repository.NewDecisionRepository(),
		briefRepo:     repository.NewBriefRepository(),
		noteRepo:      repository.NewNoteRepository(),
		usageRecorder: NewUsageRecorder(),
	}
}

// BriefInput holds all data needed to generate a brief
type BriefInput struct {
	MeetingTitle     string
	MeetingTime      time.Time
	Participants     []string
	LastSummary      string
	ConfirmedItems   []models.DecisionItem
	StaleItems       []models.DecisionItem
	UpdatedItems     []models.DecisionItem // recently updated via Faz 5 merge
	PersonalNote     string
	ChainDisplayName string
}

// GenerateBrief creates a brief for a meeting. LLM first, template fallback on failure.
func (g *BriefGenerator) GenerateBrief(ctx context.Context, userID uuid.UUID, meeting *models.Meeting, chainID uuid.UUID) (*models.MeetingBrief, error) {
	// 1. Gather input data
	input, err := g.gatherBriefInput(ctx, userID, meeting, chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to gather brief input: %w", err)
	}

	// 2. Check if there's anything to brief about
	if input.LastSummary == "" && len(input.ConfirmedItems) == 0 && len(input.StaleItems) == 0 {
		return nil, fmt.Errorf("no content for brief: no summary, no confirmed items")
	}

	// 3. Try LLM generation
	content, method, err := g.generateWithLLM(input, userID, meeting.ID)
	if err != nil {
		slog.Warn("LLM brief generation failed, using template fallback", "meeting_id", meeting.ID, "error", err)
		content = g.generateTemplate(input)
		method = "template_fallback"
	}

	// 4. Persist brief
	brief := &models.MeetingBrief{
		ID:               uuid.New(),
		UserID:           userID,
		TargetMeetingID:  meeting.ID,
		ChainID:          &chainID,
		Content:          content,
		GenerationMethod: method,
	}

	if err := g.briefRepo.CreateBrief(ctx, brief); err != nil {
		return nil, fmt.Errorf("failed to persist brief: %w", err)
	}

	slog.Info("brief generated", "meeting_id", meeting.ID, "method", method, "content_length", len(content))
	return brief, nil
}

// gatherBriefInput collects all data needed for brief generation
func (g *BriefGenerator) gatherBriefInput(ctx context.Context, userID uuid.UUID, meeting *models.Meeting, chainID uuid.UUID) (*BriefInput, error) {
	input := &BriefInput{
		MeetingTitle: meeting.Title,
		MeetingTime:  meeting.StartTime,
	}

	// Participants
	for _, att := range meeting.Attendees {
		name := att.DisplayName
		if name == "" {
			name = att.Email
		}
		input.Participants = append(input.Participants, name)
	}

	// Last 3 summaries from chain (most recent first)
	rows, err := repository.DB.Query(ctx, `
		SELECT ms.content, m.start_time FROM meeting_summaries ms
		JOIN meetings m ON m.id = ms.meeting_id
		WHERE m.chain_id = $1 AND ms.summary_type = 'final_detailed'
		AND m.id != $2
		ORDER BY m.start_time DESC LIMIT 3
	`, chainID, meeting.ID)
	if err == nil {
		defer rows.Close()
		var summaryParts []string
		for rows.Next() {
			var content string
			var meetingTime time.Time
			if rows.Scan(&content, &meetingTime) == nil && content != "" {
				summaryParts = append(summaryParts, fmt.Sprintf("[%s] %s",
					meetingTime.Format("2 Jan"), content))
			}
		}
		input.LastSummary = strings.Join(summaryParts, "\n\n")
	}

	// Chain display name
	chain, err := repository.NewChainRepository().FindByID(ctx, chainID)
	if err == nil && chain.DisplayName != nil {
		input.ChainDisplayName = *chain.DisplayName
	}

	// Chain items: only confirmed (not completed/dismissed)
	items, err := g.decisionRepo.GetConfirmedItemsByChain(ctx, chainID)
	if err == nil {
		now := time.Now()
		for _, item := range items {
			// Skip completed items — they're done, not relevant for brief
			if item.Status == "completed" {
				continue
			}

			// Use updated_at for stale check (covers Faz 5 UPDATE scenario)
			lastActivity := item.UpdatedAt
			if lastActivity.IsZero() && item.ConfirmedAt != nil {
				lastActivity = *item.ConfirmedAt
			}

			isStale := item.Status == "confirmed" && !lastActivity.IsZero() && now.Sub(lastActivity) > 14*24*time.Hour

			// Recently updated items (updated_at > confirmed_at + 1min = was modified after initial confirm)
			isRecentlyUpdated := false
			if item.ConfirmedAt != nil && item.UpdatedAt.Sub(*item.ConfirmedAt) > time.Minute {
				isRecentlyUpdated = true
			}

			if isStale {
				input.StaleItems = append(input.StaleItems, item)
			} else if isRecentlyUpdated {
				input.UpdatedItems = append(input.UpdatedItems, item)
			} else {
				input.ConfirmedItems = append(input.ConfirmedItems, item)
			}
		}
	}

	// Personal notes: read from the most recent previous meeting in this chain
	// (notes are written during review of past meetings, not on the upcoming one)
	var noteContent string
	err = repository.DB.QueryRow(ctx, `
		SELECT mn.content FROM meeting_notes mn
		JOIN meetings m ON m.id = mn.meeting_id
		WHERE m.chain_id = $1 AND mn.user_id = $2 AND m.id != $3
		  AND mn.content IS NOT NULL AND mn.content != ''
		ORDER BY m.start_time DESC LIMIT 1
	`, chainID, userID, meeting.ID).Scan(&noteContent)
	if err == nil && noteContent != "" {
		input.PersonalNote = noteContent
	}

	return input, nil
}

// generateWithLLM calls OpenAI to create a natural language brief
func (g *BriefGenerator) generateWithLLM(input *BriefInput, userID, meetingID uuid.UUID) (string, string, error) {
	openAIKey := os.Getenv("OPENAI_API_KEY")
	if openAIKey == "" {
		return "", "", fmt.Errorf("OPENAI_API_KEY not set")
	}

	systemPrompt := `Bir sonraki toplantı için Türkçe kısa bir hazırlık kartı oluştur.

Kurallar:
- Sadece verilen bilgileri kullan, yeni bilgi üretme
- Kararların "sebep" bilgisi varsa kısaca belirt (1 cümle)
- Alternatif bilgisi varsa "Alternatifler: ..." olarak göster
- Kişisel notları kararlarla ilişkilendir (varsa)
- 30 saniyede okunabilir olmalı
- 14+ gün aksiyonsuz kararları "⚠️ Dikkat" olarak işaretle, kaç gün önce onaylandığını belirt
- Son toplantı özetleri kronolojik sırada verilmişse en güncelinden başla
- Spekülatif çıkarım yapma
- Tüm çıktı Türkçe`

	// Build user prompt with all input data
	var parts []string
	parts = append(parts, fmt.Sprintf("Toplantı: %s", input.MeetingTitle))
	parts = append(parts, fmt.Sprintf("Zaman: %s", input.MeetingTime.Format("2 January 2006 15:04")))

	if len(input.Participants) > 0 {
		parts = append(parts, fmt.Sprintf("Katılımcılar: %s", strings.Join(input.Participants, ", ")))
	}

	if input.LastSummary != "" {
		parts = append(parts, fmt.Sprintf("\nSon Toplantı Özeti:\n%s", input.LastSummary))
	}

	if len(input.UpdatedItems) > 0 {
		parts = append(parts, "\n📝 Son Toplantıda Güncellenen Kararlar:")
		for _, item := range input.UpdatedItems {
			line := formatItemForBrief(item)
			parts = append(parts, line)
		}
	}

	if len(input.ConfirmedItems) > 0 {
		parts = append(parts, "\nAçık Kararlar/Aksiyonlar:")
		for _, item := range input.ConfirmedItems {
			line := formatItemForBrief(item)
			parts = append(parts, line)
		}
	}

	if len(input.StaleItems) > 0 {
		parts = append(parts, "\n⚠️ 14+ Gün Aksiyonsuz:")
		for _, item := range input.StaleItems {
			lastActivity := item.UpdatedAt
			if lastActivity.IsZero() && item.ConfirmedAt != nil {
				lastActivity = *item.ConfirmedAt
			}
			daysAgo := ""
			if !lastActivity.IsZero() {
				days := int(time.Since(lastActivity).Hours() / 24)
				daysAgo = fmt.Sprintf(" (%d gün önce)", days)
			}
			owner := ""
			if item.OwnerName != nil {
				owner = " — " + *item.OwnerName
			}
			parts = append(parts, fmt.Sprintf("- %s%s%s", item.Text, owner, daysAgo))
		}
	}

	if input.PersonalNote != "" {
		parts = append(parts, fmt.Sprintf("\nKişisel Notlarım:\n%s", input.PersonalNote))
	}

	userPrompt := strings.Join(parts, "\n")

	reqBody := map[string]interface{}{
		"model": "gpt-5.4",
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens":  1000,
		"temperature": 0.4,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", strings.NewReader(string(jsonData)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+openAIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("OpenAI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("OpenAI returned status %d", resp.StatusCode)
	}

	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", "", err
	}

	if len(openAIResp.Choices) == 0 {
		return "", "", fmt.Errorf("no choices in response")
	}

	content := strings.TrimSpace(openAIResp.Choices[0].Message.Content)

	// Record usage
	if openAIResp.Usage.TotalTokens > 0 {
		g.usageRecorder.Record(userID, nil, &meetingID, "brief_generation", "openai", "gpt-5.4",
			openAIResp.Usage.PromptTokens, openAIResp.Usage.CompletionTokens, 0, nil)
	}

	return content, "llm", nil
}

// generateTemplate creates a brief using a static template (fallback)
func (g *BriefGenerator) generateTemplate(input *BriefInput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("📅 %s\n", input.MeetingTitle))
	sb.WriteString(fmt.Sprintf("🕐 %s\n", input.MeetingTime.Format("2 January 2006 15:04")))

	if len(input.Participants) > 0 {
		sb.WriteString(fmt.Sprintf("👥 %s\n", strings.Join(input.Participants, ", ")))
	}

	if input.LastSummary != "" {
		sb.WriteString(fmt.Sprintf("\n📝 Son Toplantı Özeti:\n%s\n", input.LastSummary))
	}

	if len(input.UpdatedItems) > 0 {
		sb.WriteString("\n📝 Son Toplantıda Güncellenen:\n")
		for _, item := range input.UpdatedItems {
			writeItemToTemplate(&sb, item)
		}
	}

	decisions := filterByType(input.ConfirmedItems, "decision")
	actions := filterByType(input.ConfirmedItems, "action_item")
	topics := filterByType(input.ConfirmedItems, "open_topic")

	if len(decisions) > 0 {
		sb.WriteString("\n✅ Açık Kararlar:\n")
		for _, item := range decisions {
			writeItemToTemplate(&sb, item)
		}
	}

	if len(actions) > 0 {
		sb.WriteString("\n📋 Yapılacaklar:\n")
		for _, item := range actions {
			writeItemToTemplate(&sb, item)
		}
	}

	if len(topics) > 0 {
		sb.WriteString("\n❓ Açık Konular:\n")
		for _, item := range topics {
			sb.WriteString(fmt.Sprintf("• %s\n", item.Text))
		}
	}

	if len(input.StaleItems) > 0 {
		sb.WriteString("\n⚠️ 14+ Gün Aksiyonsuz:\n")
		for _, item := range input.StaleItems {
			owner := ""
			if item.OwnerName != nil {
				owner = " — " + *item.OwnerName
			}
			lastActivity := item.UpdatedAt
			if lastActivity.IsZero() && item.ConfirmedAt != nil {
				lastActivity = *item.ConfirmedAt
			}
			daysAgo := ""
			if !lastActivity.IsZero() {
				days := int(time.Since(lastActivity).Hours() / 24)
				daysAgo = fmt.Sprintf(" (%d gün)", days)
			}
			sb.WriteString(fmt.Sprintf("• %s%s%s\n", item.Text, owner, daysAgo))
		}
	}

	if input.PersonalNote != "" {
		sb.WriteString(fmt.Sprintf("\n📌 Kişisel Notlarınız:\n%s\n", input.PersonalNote))
	}

	return sb.String()
}

// writeItemToTemplate writes a rich item line to a string builder (for template fallback)
func writeItemToTemplate(sb *strings.Builder, item models.DecisionItem) {
	owner := ""
	if item.OwnerName != nil {
		owner = " — " + *item.OwnerName
	}
	sb.WriteString(fmt.Sprintf("• %s%s\n", item.Text, owner))
	if item.Context != nil && *item.Context != "" {
		sb.WriteString(fmt.Sprintf("  Sebep: %s\n", *item.Context))
	}
	if item.AlternativesConsidered != nil && *item.AlternativesConsidered != "" {
		sb.WriteString(fmt.Sprintf("  Alternatifler: %s\n", *item.AlternativesConsidered))
	}
}

// formatItemForBrief builds a rich text line for a decision item including context + alternatives
func formatItemForBrief(item models.DecisionItem) string {
	typeLabel := map[string]string{"decision": "Karar", "action_item": "Aksiyon", "open_topic": "Açık Konu"}
	owner := ""
	if item.OwnerName != nil {
		owner = " — " + *item.OwnerName
	}
	line := fmt.Sprintf("- [%s] %s%s", typeLabel[item.ItemType], item.Text, owner)
	if item.Context != nil && *item.Context != "" {
		line += fmt.Sprintf("\n  Sebep: %s", *item.Context)
	}
	if item.AlternativesConsidered != nil && *item.AlternativesConsidered != "" {
		line += fmt.Sprintf("\n  Alternatifler: %s", *item.AlternativesConsidered)
	}
	return line
}

func filterByType(items []models.DecisionItem, itemType string) []models.DecisionItem {
	var result []models.DecisionItem
	for _, item := range items {
		if item.ItemType == itemType {
			result = append(result, item)
		}
	}
	return result
}

// IsStale returns true if a confirmed item has had no activity for 14+ days.
// Uses updated_at (covers Faz 5 updates), falls back to confirmed_at.
func IsStale(item models.DecisionItem) bool {
	if item.Status != "confirmed" {
		return false
	}
	lastActivity := item.UpdatedAt
	if lastActivity.IsZero() && item.ConfirmedAt != nil {
		lastActivity = *item.ConfirmedAt
	}
	if lastActivity.IsZero() {
		return false
	}
	return time.Since(lastActivity) > 14*24*time.Hour
}
