package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/google/uuid"
)

// ==================== HTTP dispatcher ====================

func TestHTTPBotDispatcher_HappyPath(t *testing.T) {
	// Cannot t.Parallel — uses t.Setenv

	var (
		gotMethod      string
		gotURL         string
		gotContentType string
		gotAPIKey      string
		gotBody        map[string]string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURL = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotAPIKey = r.Header.Get("x-bot-api-key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	t.Setenv("EXTERNAL_BOT_API_KEY", "secret-key")

	d := NewHTTPBotDispatcher(server.URL)
	meetingID := uuid.New()
	err := d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  meetingID,
		MeetingURL: "https://meet.google.com/abc-defg-hij",
		BotName:    "Syncrun",
		Language:   "tr",
	})

	if err != nil {
		t.Fatalf("DispatchBot returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotURL != "/meetings/join" {
		t.Errorf("path = %q, want /meetings/join", gotURL)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotAPIKey != "secret-key" {
		t.Errorf("api key = %q, want secret-key", gotAPIKey)
	}
	if gotBody["meeting_id"] != meetingID.String() {
		t.Errorf("body.meeting_id = %q, want %q", gotBody["meeting_id"], meetingID)
	}
	if gotBody["meeting_url"] != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("body.meeting_url = %q", gotBody["meeting_url"])
	}
	if gotBody["bot_name"] != "Syncrun" {
		t.Errorf("body.bot_name = %q", gotBody["bot_name"])
	}
	if gotBody["language"] != "tr" {
		t.Errorf("body.language = %q", gotBody["language"])
	}
}

func TestHTTPBotDispatcher_OmitsLanguageWhenEmpty(t *testing.T) {
	t.Parallel()

	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := NewHTTPBotDispatcher(server.URL)
	err := d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  uuid.New(),
		MeetingURL: "https://meet.google.com/x",
		BotName:    "B",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := gotBody["language"]; ok {
		t.Error("language key should be omitted when empty")
	}
}

func TestHTTPBotDispatcher_5xxReturnsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer server.Close()

	d := NewHTTPBotDispatcher(server.URL)
	err := d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  uuid.New(),
		MeetingURL: "u",
		BotName:    "B",
	})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error missing status/body: %v", err)
	}
}

func TestHTTPBotDispatcher_EmptyURL(t *testing.T) {
	t.Parallel()
	d := NewHTTPBotDispatcher("")
	err := d.DispatchBot(context.Background(), BotDispatchRequest{MeetingID: uuid.New()})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' error, got %v", err)
	}
}

func TestHTTPBotDispatcher_OmitsAuthHeaderWhenKeyUnset(t *testing.T) {
	// Cannot t.Parallel — uses t.Setenv

	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-bot-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t.Setenv("EXTERNAL_BOT_API_KEY", "")
	d := NewHTTPBotDispatcher(server.URL)
	_ = d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  uuid.New(),
		MeetingURL: "u",
		BotName:    "B",
	})
	if gotKey != "" {
		t.Errorf("expected no api key header, got %q", gotKey)
	}
}

// ==================== ACA dispatcher ====================

// fakeStarter records BeginStart calls and returns a canned error.
type fakeStarter struct {
	mu    sync.Mutex
	calls []fakeStartCall
	err   error
}

type fakeStartCall struct {
	ResourceGroup string
	JobName       string
	Env           map[string]string
}

func (f *fakeStarter) BeginStart(
	_ context.Context,
	rg string,
	jobName string,
	opts *armappcontainers.JobsClientBeginStartOptions,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	env := map[string]string{}
	if opts != nil && opts.Template != nil {
		for _, c := range opts.Template.Containers {
			for _, e := range c.Env {
				if e.Name != nil && e.Value != nil {
					env[*e.Name] = *e.Value
				}
			}
		}
	}
	f.calls = append(f.calls, fakeStartCall{
		ResourceGroup: rg,
		JobName:       jobName,
		Env:           env,
	})
	return f.err
}

func TestACABotDispatcher_SetsEnvOverrides(t *testing.T) {
	t.Parallel()

	f := &fakeStarter{}
	d := &ACABotDispatcher{
		starter:        f,
		resourceGroup:  "syncrun-rg",
		jobName:        "mia-meeting-bot",
		subscriptionID: "sub-xyz",
	}

	meetingID := uuid.New()
	err := d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  meetingID,
		MeetingURL: "https://meet.google.com/abc-defg-hij",
		BotName:    "Syncrun",
		Language:   "tr",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 start call, got %d", len(f.calls))
	}

	call := f.calls[0]
	if call.ResourceGroup != "syncrun-rg" {
		t.Errorf("rg = %q", call.ResourceGroup)
	}
	if call.JobName != "mia-meeting-bot" {
		t.Errorf("job = %q", call.JobName)
	}
	if call.Env["MEETING_ID"] != meetingID.String() {
		t.Errorf("MEETING_ID = %q", call.Env["MEETING_ID"])
	}
	if call.Env["MEETING_URL"] != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("MEETING_URL = %q", call.Env["MEETING_URL"])
	}
	if call.Env["BOT_NAME"] != "Syncrun" {
		t.Errorf("BOT_NAME = %q", call.Env["BOT_NAME"])
	}
	if call.Env["MEETING_LANGUAGE"] != "tr" {
		t.Errorf("MEETING_LANGUAGE = %q", call.Env["MEETING_LANGUAGE"])
	}
}

func TestACABotDispatcher_OmitsLanguageWhenEmpty(t *testing.T) {
	t.Parallel()
	f := &fakeStarter{}
	d := &ACABotDispatcher{starter: f, resourceGroup: "rg", jobName: "job"}
	_ = d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  uuid.New(),
		MeetingURL: "u",
		BotName:    "B",
	})
	if _, ok := f.calls[0].Env["MEETING_LANGUAGE"]; ok {
		t.Error("MEETING_LANGUAGE should be absent when Language empty")
	}
}

func TestACABotDispatcher_PropagatesStartError(t *testing.T) {
	t.Parallel()
	f := &fakeStarter{err: errors.New("azure 403")}
	d := &ACABotDispatcher{starter: f, resourceGroup: "rg", jobName: "job"}
	err := d.DispatchBot(context.Background(), BotDispatchRequest{
		MeetingID:  uuid.New(),
		MeetingURL: "u",
		BotName:    "B",
	})
	if err == nil || !strings.Contains(err.Error(), "azure 403") {
		t.Errorf("expected wrapped azure error, got %v", err)
	}
}

// ==================== Factory + env ====================

func TestNewBotDispatcher_PicksACAWhenSubscriptionSet(t *testing.T) {
	// Cannot run parallel — mutates env
	originalSub := os.Getenv("AZURE_SUBSCRIPTION_ID")
	t.Cleanup(func() { _ = os.Setenv("AZURE_SUBSCRIPTION_ID", originalSub) })

	// Use a fake sub; azidentity.NewDefaultAzureCredential does not
	// actually probe until a request is made, so construction is safe.
	_ = os.Setenv("AZURE_SUBSCRIPTION_ID", "00000000-0000-0000-0000-000000000000")
	d, err := NewBotDispatcher("http://unused")
	if err != nil {
		t.Fatalf("NewBotDispatcher failed: %v", err)
	}
	if _, ok := d.(*ACABotDispatcher); !ok {
		t.Errorf("expected *ACABotDispatcher, got %T", d)
	}
}

func TestNewBotDispatcher_FallsBackToHTTPWhenSubscriptionUnset(t *testing.T) {
	originalSub := os.Getenv("AZURE_SUBSCRIPTION_ID")
	t.Cleanup(func() { _ = os.Setenv("AZURE_SUBSCRIPTION_ID", originalSub) })

	_ = os.Unsetenv("AZURE_SUBSCRIPTION_ID")
	d, err := NewBotDispatcher("http://bot.local")
	if err != nil {
		t.Fatalf("NewBotDispatcher failed: %v", err)
	}
	if _, ok := d.(*HTTPBotDispatcher); !ok {
		t.Errorf("expected *HTTPBotDispatcher, got %T", d)
	}
}

func TestMaxConcurrentBotJobs_Default(t *testing.T) {
	// Cannot t.Parallel — uses t.Setenv
	t.Setenv("MAX_CONCURRENT_BOT_JOBS", "")
	if got := MaxConcurrentBotJobs(); got != 5 {
		t.Errorf("default = %d, want 5", got)
	}
}

func TestMaxConcurrentBotJobs_Explicit(t *testing.T) {
	// Cannot t.Parallel — uses t.Setenv
	t.Setenv("MAX_CONCURRENT_BOT_JOBS", "12")
	if got := MaxConcurrentBotJobs(); got != 12 {
		t.Errorf("= %d, want 12", got)
	}
}

func TestMaxConcurrentBotJobs_InvalidFallsBack(t *testing.T) {
	// Cannot t.Parallel — uses t.Setenv
	t.Setenv("MAX_CONCURRENT_BOT_JOBS", "abc")
	if got := MaxConcurrentBotJobs(); got != 5 {
		t.Errorf("invalid -> %d, want 5", got)
	}
	t.Setenv("MAX_CONCURRENT_BOT_JOBS", "0")
	if got := MaxConcurrentBotJobs(); got != 5 {
		t.Errorf("zero -> %d, want 5", got)
	}
}
