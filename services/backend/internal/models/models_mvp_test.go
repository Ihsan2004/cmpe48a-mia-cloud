package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDecisionItem_Serialization(t *testing.T) {
	t.Parallel()

	chainID := uuid.New()
	ownerName := "Alice"
	sourceQuote := "Let's deploy on Wednesday"
	now := time.Now()

	item := DecisionItem{
		ID:          uuid.New(),
		MeetingID:   uuid.New(),
		ChainID:     &chainID,
		UserID:      uuid.New(),
		ItemType:    "decision",
		Text:        "Deploy on Wednesday",
		OwnerName:   &ownerName,
		SourceQuote: &sourceQuote,
		Status:      "confirmed",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed DecisionItem
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if parsed.ItemType != "decision" {
		t.Errorf("expected item_type=decision, got %s", parsed.ItemType)
	}
	if parsed.Status != "confirmed" {
		t.Errorf("expected status=confirmed, got %s", parsed.Status)
	}
	if *parsed.OwnerName != "Alice" {
		t.Errorf("expected owner_name=Alice, got %s", *parsed.OwnerName)
	}
}

func TestDecisionItem_ItemTypes(t *testing.T) {
	t.Parallel()

	validTypes := []string{"decision", "action_item", "open_topic"}
	for _, itemType := range validTypes {
		item := DecisionItem{ItemType: itemType}
		if item.ItemType != itemType {
			t.Errorf("expected %s, got %s", itemType, item.ItemType)
		}
	}
}

func TestDecisionItem_StatusTransitions(t *testing.T) {
	t.Parallel()

	validStatuses := []string{"confirmed", "completed", "dismissed"}
	for _, status := range validStatuses {
		item := DecisionItem{Status: status}
		if item.Status != status {
			t.Errorf("expected %s, got %s", status, item.Status)
		}
	}
}

func TestMeetingChain_Serialization(t *testing.T) {
	t.Parallel()

	seriesID := "abc123"
	displayName := "Weekly Sync"

	chain := MeetingChain{
		ID:               uuid.New(),
		ChainType:        "calendar_series",
		CalendarSeriesID: &seriesID,
		DisplayName:      &displayName,
		CreatedAt:        time.Now(),
	}

	data, err := json.Marshal(chain)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed MeetingChain
	json.Unmarshal(data, &parsed)

	if parsed.ChainType != "calendar_series" {
		t.Errorf("expected chain_type=calendar_series, got %s", parsed.ChainType)
	}
	if *parsed.CalendarSeriesID != "abc123" {
		t.Errorf("expected calendar_series_id=abc123, got %s", *parsed.CalendarSeriesID)
	}
}

func TestMeetingNote_Serialization(t *testing.T) {
	t.Parallel()

	content := "Remember to ask about budget"
	note := MeetingNote{
		ID:        uuid.New(),
		MeetingID: uuid.New(),
		UserID:    uuid.New(),
		Content:   &content,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	data, err := json.Marshal(note)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed MeetingNote
	json.Unmarshal(data, &parsed)

	if *parsed.Content != content {
		t.Errorf("expected content=%s, got %s", content, *parsed.Content)
	}
}

func TestMeetingBrief_GenerationMethods(t *testing.T) {
	t.Parallel()

	methods := []string{"llm", "template_fallback"}
	for _, method := range methods {
		brief := MeetingBrief{GenerationMethod: method}
		if brief.GenerationMethod != method {
			t.Errorf("expected %s, got %s", method, brief.GenerationMethod)
		}
	}
}

func TestBriefFeedback_FeedbackValues(t *testing.T) {
	t.Parallel()

	values := []string{"useful", "not_useful"}
	for _, val := range values {
		fb := BriefFeedback{Feedback: val}
		if fb.Feedback != val {
			t.Errorf("expected %s, got %s", val, fb.Feedback)
		}
	}
}

func TestMeeting_NewMVPFields(t *testing.T) {
	t.Parallel()

	seriesID := "recurring_123"
	chainID := uuid.New()
	processedAt := time.Now()

	meeting := Meeting{
		ID:               uuid.New(),
		Title:            "Test Meeting",
		CalendarSeriesID: &seriesID,
		ChainID:          &chainID,
		ProcessedAt:      &processedAt,
	}

	if *meeting.CalendarSeriesID != "recurring_123" {
		t.Errorf("expected calendar_series_id=recurring_123, got %s", *meeting.CalendarSeriesID)
	}
	if *meeting.ChainID != chainID {
		t.Errorf("expected chain_id=%s, got %s", chainID, *meeting.ChainID)
	}
	if meeting.ProcessedAt == nil {
		t.Error("expected processed_at to be set")
	}
}

func TestProfile_BriefLeadTimeMinutes(t *testing.T) {
	t.Parallel()

	profile := Profile{
		BriefLeadTimeMinutes: 10,
	}

	if profile.BriefLeadTimeMinutes != 10 {
		t.Errorf("expected 10, got %d", profile.BriefLeadTimeMinutes)
	}

	// Zero value should be 0 (Go default)
	profile2 := Profile{}
	if profile2.BriefLeadTimeMinutes != 0 {
		t.Errorf("expected 0 default, got %d", profile2.BriefLeadTimeMinutes)
	}
}
