// Package services — elevenlabs_batch.go is the thin client for ElevenLabs
// Scribe v2 batch speech-to-text. It submits a job (audio URL + webhook
// config) and returns the provider's request_id; the transcript itself
// arrives later via the webhook handled by handlers/webhook_elevenlabs.go.
//
// One submit call per audio file. No polling loop here — the webhook is the
// completion signal. The TranscribeJob worker (background_job_worker.go)
// bumps attempt_count + retries with exponential backoff if the submit
// fails synchronously.
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mia-website/backend/internal/pkg/metrics"
)

const (
	elevenLabsSTTEndpoint = "https://api.elevenlabs.io/v1/speech-to-text"
	elevenLabsReqTimeout  = 60 * time.Second
)

// ElevenLabsBatchConfig carries the runtime knobs for submission.
// Populated from config.Config at startup so tests can inject fakes.
type ElevenLabsBatchConfig struct {
	APIKey        string
	WebhookID     string // dashboard-configured webhook target
	ModelID       string // default "scribe_v2"
	LanguageCode  string // default "tr"; pass "auto" to let ElevenLabs detect
	BaseURL       string // override for tests; defaults to production endpoint
	RequestClient *http.Client
}

// ElevenLabsBatchService submits audio to the batch STT API.
type ElevenLabsBatchService struct {
	cfg ElevenLabsBatchConfig
}

// NewElevenLabsBatchService returns a ready-to-use client. Returns nil if
// the API key is missing — callers should treat that as "transcription
// disabled" and fall back to the old pipeline (or error).
func NewElevenLabsBatchService(cfg ElevenLabsBatchConfig) *ElevenLabsBatchService {
	if cfg.APIKey == "" {
		return nil
	}
	if cfg.ModelID == "" {
		cfg.ModelID = "scribe_v2"
	}
	if cfg.LanguageCode == "" {
		cfg.LanguageCode = "tr"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = elevenLabsSTTEndpoint
	}
	if cfg.RequestClient == nil {
		cfg.RequestClient = &http.Client{Timeout: elevenLabsReqTimeout}
	}
	return &ElevenLabsBatchService{cfg: cfg}
}

// SubmitOptions configures a single batch STT submission.
type SubmitOptions struct {
	// AudioURL is a short-lived HTTPS URL fetched by ElevenLabs. Generated
	// via Supabase signed URL with 15-30 min expiry. Required.
	AudioURL string
	// Language overrides config default. Pass "" to use the default.
	Language string
	// Diarize toggles speaker diarisation. We pass false — our product
	// removed speaker detection (roadmap karar #4).
	Diarize bool
}

// SubmitResponse is the decoded initial response from ElevenLabs.
// The real transcript arrives later via webhook.
type SubmitResponse struct {
	RequestID string // opaque id for correlation — persisted on meeting row
}

// Submit posts the multipart request to ElevenLabs and returns the assigned
// request_id. Any non-2xx response surfaces as a concrete error including
// status + response body (truncated) so the worker can decide whether to
// retry.
func (s *ElevenLabsBatchService) Submit(ctx context.Context, opts SubmitOptions) (*SubmitResponse, error) {
	if opts.AudioURL == "" {
		return nil, fmt.Errorf("elevenlabs submit: audio URL is required")
	}
	if s.cfg.WebhookID == "" {
		return nil, fmt.Errorf("elevenlabs submit: webhook id not configured")
	}

	lang := opts.Language
	if lang == "" {
		lang = s.cfg.LanguageCode
	}

	body, contentType, err := buildSubmitBody(buildSubmitFields{
		ModelID:              s.cfg.ModelID,
		CloudStorageURL:      opts.AudioURL,
		WebhookID:            s.cfg.WebhookID,
		LanguageCode:         lang,
		Diarize:              opts.Diarize,
		TagAudioEvents:       true,    // exposes silence/noise events to us
		TimestampsGranularity: "word", // we need word-level for segment mapping
	})
	if err != nil {
		return nil, fmt.Errorf("elevenlabs submit: build body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL, body)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs submit: build request: %w", err)
	}
	req.Header.Set("xi-api-key", s.cfg.APIKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := s.cfg.RequestClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs submit: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &ElevenLabsAPIError{
			Status: resp.StatusCode,
			Body:   strings.TrimSpace(string(msg)),
		}
	}

	var out struct {
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("elevenlabs submit: decode: %w", err)
	}
	if out.RequestID == "" {
		return nil, fmt.Errorf("elevenlabs submit: empty request_id in response")
	}
	metrics.BatchTranscriptionSubmitted.Inc()
	return &SubmitResponse{RequestID: out.RequestID}, nil
}

// ElevenLabsAPIError is returned for non-2xx responses so the worker can
// distinguish HTTP errors (retry candidate) from malformed-input errors.
type ElevenLabsAPIError struct {
	Status int
	Body   string
}

func (e *ElevenLabsAPIError) Error() string {
	return fmt.Sprintf("elevenlabs: status %d: %s", e.Status, e.Body)
}

// IsRetryable returns true for 5xx / 429 / network-level errors.
// 4xx (other than 429) is a bad request — retrying won't help.
func (e *ElevenLabsAPIError) IsRetryable() bool {
	return e.Status == http.StatusTooManyRequests || (e.Status >= 500 && e.Status < 600)
}

// --- internals ---

type buildSubmitFields struct {
	ModelID              string
	CloudStorageURL      string
	WebhookID            string
	LanguageCode         string
	Diarize              bool
	TagAudioEvents       bool
	TimestampsGranularity string
}

func buildSubmitBody(f buildSubmitFields) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	add := func(name, value string) error {
		if value == "" {
			return nil
		}
		return mw.WriteField(name, value)
	}

	if err := add("model_id", f.ModelID); err != nil {
		return nil, "", err
	}
	if err := add("cloud_storage_url", f.CloudStorageURL); err != nil {
		return nil, "", err
	}
	if err := add("webhook", "true"); err != nil {
		return nil, "", err
	}
	if err := add("webhook_id", f.WebhookID); err != nil {
		return nil, "", err
	}
	if err := add("language_code", f.LanguageCode); err != nil {
		return nil, "", err
	}
	if err := add("diarize", strconv.FormatBool(f.Diarize)); err != nil {
		return nil, "", err
	}
	if err := add("tag_audio_events", strconv.FormatBool(f.TagAudioEvents)); err != nil {
		return nil, "", err
	}
	if err := add("timestamps_granularity", f.TimestampsGranularity); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}
