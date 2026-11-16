package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/google/uuid"
)

// BotDispatcher triggers a bot execution for a meeting. Callers handle
// bot_status transitions and duplicate guards — this interface is only
// about starting the underlying worker.
type BotDispatcher interface {
	DispatchBot(ctx context.Context, req BotDispatchRequest) error
}

// BotDispatchRequest carries the env-var payload the bot container needs
// to join a specific meeting. Language may be empty when the profile is
// set to "auto" — the bot then falls back to detection.
type BotDispatchRequest struct {
	MeetingID  uuid.UUID
	MeetingURL string
	BotName    string
	Language   string
}

// NewBotDispatcher selects an implementation based on env. When
// AZURE_SUBSCRIPTION_ID is set (production) the ACA Jobs dispatcher is
// returned; otherwise the legacy HTTP dispatcher is used so local/dev
// deployments keep working against the old bot orchestrator.
//
// httpBotAPIURL is only consumed by the HTTP fallback.
func NewBotDispatcher(httpBotAPIURL string) (BotDispatcher, error) {
	if os.Getenv("AZURE_SUBSCRIPTION_ID") != "" {
		return NewACABotDispatcher()
	}
	return NewHTTPBotDispatcher(httpBotAPIURL), nil
}

// ---------- HTTP (legacy) ----------

// HTTPBotDispatcher posts to the pre-7.10 bot orchestrator
// (`POST {botAPIURL}/meetings/join`). Kept so environments without Azure
// credentials (CI, local compose) can still dispatch the bot.
type HTTPBotDispatcher struct {
	botAPIURL string
	client    *http.Client
	apiKey    string
}

// NewHTTPBotDispatcher returns the legacy HTTP dispatcher. apiKey comes
// from EXTERNAL_BOT_API_KEY (with fallback to MIA_BOT_API_KEY / BOT_API_KEY);
// if empty, the auth header is omitted.
func NewHTTPBotDispatcher(botAPIURL string) *HTTPBotDispatcher {
	apiKey := os.Getenv("EXTERNAL_BOT_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("MIA_BOT_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("BOT_API_KEY")
	}
	return &HTTPBotDispatcher{
		botAPIURL: botAPIURL,
		apiKey:    apiKey,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

// DispatchBot POSTs a JSON join request to the legacy bot orchestrator.
func (h *HTTPBotDispatcher) DispatchBot(ctx context.Context, req BotDispatchRequest) error {
	if h.botAPIURL == "" {
		return fmt.Errorf("http dispatcher: bot API URL is empty")
	}

	body := map[string]string{
		"meeting_url": req.MeetingURL,
		"bot_name":    req.BotName,
		"meeting_id":  req.MeetingID.String(),
	}
	if req.Language != "" {
		body["language"] = req.Language
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal bot request: %w", err)
	}

	endpoint := h.botAPIURL + "/meetings/join"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		httpReq.Header.Set("x-bot-api-key", h.apiKey)
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bot API returned %d: %s", resp.StatusCode, string(raw))
	}

	slog.Info("bot dispatched via HTTP",
		"meeting_id", req.MeetingID,
		"endpoint", endpoint,
		"status", resp.StatusCode,
	)
	return nil
}

// ---------- Azure Container Apps Jobs ----------

// acaJobsStarter is the narrow slice of armappcontainers.JobsClient the
// ACA dispatcher actually uses. Extracted so tests can stub Azure without
// hitting the real SDK. The poller returned by the real client is
// discarded — dispatching is fire-and-forget by design.
type acaJobsStarter interface {
	BeginStart(
		ctx context.Context,
		resourceGroupName string,
		jobName string,
		options *armappcontainers.JobsClientBeginStartOptions,
	) error
}

// ACABotDispatcher starts a new ACA Container Apps Job execution per
// meeting. Each execution is bound to a single meeting via MEETING_ID +
// MEETING_URL env overrides on the single container in the Job template;
// the Job resource itself is shared.
type ACABotDispatcher struct {
	starter        acaJobsStarter
	resourceGroup  string
	jobName        string
	subscriptionID string
}

// NewACABotDispatcher wires Azure SDK defaults. Reads config from env:
//
//	AZURE_SUBSCRIPTION_ID  — mandatory
//	AZURE_RESOURCE_GROUP   — default: syncrun-rg
//	AZURE_ACA_JOB_NAME     — default: mia-meeting-bot
//
// Authentication uses DefaultAzureCredential — Managed Identity in ACA,
// `az login` / env creds locally.
func NewACABotDispatcher() (*ACABotDispatcher, error) {
	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
	if subscriptionID == "" {
		return nil, fmt.Errorf("AZURE_SUBSCRIPTION_ID not set")
	}
	rg := os.Getenv("AZURE_RESOURCE_GROUP")
	if rg == "" {
		rg = "syncrun-rg"
	}
	jobName := os.Getenv("AZURE_ACA_JOB_NAME")
	if jobName == "" {
		jobName = "mia-meeting-bot"
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure credential: %w", err)
	}
	factory, err := armappcontainers.NewClientFactory(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("aca client factory: %w", err)
	}

	return &ACABotDispatcher{
		starter:        &sdkStarter{client: factory.NewJobsClient()},
		resourceGroup:  rg,
		jobName:        jobName,
		subscriptionID: subscriptionID,
	}, nil
}

// DispatchBot starts a new execution of the ACA Job with the meeting's
// env vars applied as overrides on the single container in the Job
// template. Returns once the start request is accepted — it does not
// wait for the bot to actually join.
func (a *ACABotDispatcher) DispatchBot(ctx context.Context, req BotDispatchRequest) error {
	envVars := []*armappcontainers.EnvironmentVar{
		{Name: azStrPtr("MEETING_ID"), Value: azStrPtr(req.MeetingID.String())},
		{Name: azStrPtr("MEETING_URL"), Value: azStrPtr(req.MeetingURL)},
		{Name: azStrPtr("BOT_NAME"), Value: azStrPtr(req.BotName)},
	}
	if req.Language != "" {
		envVars = append(envVars, &armappcontainers.EnvironmentVar{
			Name:  azStrPtr("MEETING_LANGUAGE"),
			Value: azStrPtr(req.Language),
		})
	}

	template := &armappcontainers.JobExecutionTemplate{
		Containers: []*armappcontainers.JobExecutionContainer{
			{Name: azStrPtr(a.jobName), Env: envVars},
		},
	}

	opts := &armappcontainers.JobsClientBeginStartOptions{
		Template: template,
	}

	if err := a.starter.BeginStart(ctx, a.resourceGroup, a.jobName, opts); err != nil {
		return fmt.Errorf("start aca job: %w", err)
	}

	slog.Info("bot dispatched via ACA Jobs",
		"meeting_id", req.MeetingID,
		"resource_group", a.resourceGroup,
		"job_name", a.jobName,
	)
	return nil
}

// ---------- Concurrent limit helpers ----------

// MaxConcurrentBotJobs returns the configured cap on parallel bot
// executions. Default: 5 (set in Phase 7.10).
func MaxConcurrentBotJobs() int {
	raw := os.Getenv("MAX_CONCURRENT_BOT_JOBS")
	if raw == "" {
		return 5
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 5
	}
	return n
}

// ---------- helpers ----------

func azStrPtr(s string) *string { return &s }

// sdkStarter adapts the real SDK client to our acaJobsStarter interface,
// discarding the returned poller.
type sdkStarter struct {
	client *armappcontainers.JobsClient
}

func (s *sdkStarter) BeginStart(
	ctx context.Context,
	rg string,
	jobName string,
	opts *armappcontainers.JobsClientBeginStartOptions,
) error {
	_, err := s.client.BeginStart(ctx, rg, jobName, opts)
	return err
}
