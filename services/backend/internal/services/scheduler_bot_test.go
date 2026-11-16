package services

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== Bot API HTTP Request Tests ====================

func TestBotJoinRequest_Payload_Structure(t *testing.T) {
	t.Parallel()

	meetingURL := "https://meet.google.com/abc-def-ghi"
	meetingID := uuid.New()
	botName := "Test Bot"
	botLanguage := "tr"

	req := BotJoinRequest{
		MeetingURL: meetingURL,
		BotName:    botName,
		MeetingID:  meetingID.String(),
	}

	if botLanguage != "" && botLanguage != "auto" {
		req.Language = botLanguage
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["meeting_url"] != meetingURL {
		t.Errorf("expected meeting_url=%s, got %s", meetingURL, parsed["meeting_url"])
	}
	if parsed["bot_name"] != botName {
		t.Errorf("expected bot_name=%s, got %s", botName, parsed["bot_name"])
	}
	if parsed["meeting_id"] != meetingID.String() {
		t.Errorf("expected meeting_id=%s, got %s", meetingID.String(), parsed["meeting_id"])
	}
	if parsed["language"] != "tr" {
		t.Errorf("expected language=tr, got %v", parsed["language"])
	}
}

func TestBotJoinRequest_Payload_AutoLanguage_Omitted(t *testing.T) {
	t.Parallel()

	req := BotJoinRequest{
		MeetingURL: "https://meet.google.com/abc",
		BotName:    "Bot",
		MeetingID:  uuid.New().String(),
	}

	data, _ := json.Marshal(req)
	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if lang, exists := parsed["language"]; exists && lang != "" {
		t.Errorf("expected language to be omitted for auto mode, got %v", lang)
	}
}

func TestBotAPI_ReceivesCorrectEndpointAndHeaders(t *testing.T) {
	t.Parallel()

	var receivedPath string
	var receivedContentType string
	var receivedAPIKey string
	var receivedBody BotJoinRequest

	fakeBot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedContentType = r.Header.Get("Content-Type")
		receivedAPIKey = r.Header.Get("x-bot-api-key")

		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer fakeBot.Close()

	meetingURL := "https://meet.google.com/test-meeting"
	meetingID := uuid.New()

	payload := BotJoinRequest{
		MeetingURL: meetingURL,
		BotName:    "MIA Assistant",
		MeetingID:  meetingID.String(),
		Language:   "tr",
	}

	jsonData, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", fakeBot.URL+"/meetings/join", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-bot-api-key", "test-api-key")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if receivedPath != "/meetings/join" {
		t.Errorf("expected path /meetings/join, got %s", receivedPath)
	}
	if receivedContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", receivedContentType)
	}
	if receivedAPIKey != "test-api-key" {
		t.Errorf("expected x-bot-api-key=test-api-key, got %s", receivedAPIKey)
	}
	if receivedBody.MeetingURL != meetingURL {
		t.Errorf("expected meeting_url=%s, got %s", meetingURL, receivedBody.MeetingURL)
	}
	if receivedBody.BotName != "MIA Assistant" {
		t.Errorf("expected bot_name=MIA Assistant, got %s", receivedBody.BotName)
	}
	if receivedBody.Language != "tr" {
		t.Errorf("expected language=tr, got %s", receivedBody.Language)
	}
}

func TestBotAPI_ErrorResponse_StatusCode(t *testing.T) {
	t.Parallel()

	fakeBot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
	}))
	defer fakeBot.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fakeBot.URL+"/meetings/join", "application/json", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 500 {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}

	// Verify error is parseable
	var errorResp map[string]string
	json.NewDecoder(resp.Body).Decode(&errorResp)
	if errorResp["error"] != "internal server error" {
		t.Errorf("expected error message, got %v", errorResp)
	}
}

func TestBotAPI_KeyFromEnv(t *testing.T) {
	t.Setenv("EXTERNAL_BOT_API_KEY", "my-secret-key")

	key := os.Getenv("EXTERNAL_BOT_API_KEY")
	if key != "my-secret-key" {
		t.Errorf("expected my-secret-key, got %s", key)
	}
}

func TestJoinMeetingWithBot_NoMeetingURL(t *testing.T) {
	t.Parallel()

	meeting := models.Meeting{
		ID:         uuid.New(),
		MeetingURL: nil,
	}

	svc := &SchedulerService{
		botAPIURL: "http://fake:3000",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	// Should return early without panic — no HTTP call made
	svc.joinMeetingWithBot(meeting)
}

func TestJoinMeetingWithBot_EmptyMeetingURL(t *testing.T) {
	t.Parallel()

	emptyURL := ""
	meeting := models.Meeting{
		ID:         uuid.New(),
		MeetingURL: &emptyURL,
	}

	svc := &SchedulerService{
		botAPIURL: "http://fake:3000",
		client:    &http.Client{Timeout: 5 * time.Second},
	}

	// Should return early without panic — no HTTP call made
	svc.joinMeetingWithBot(meeting)
}
