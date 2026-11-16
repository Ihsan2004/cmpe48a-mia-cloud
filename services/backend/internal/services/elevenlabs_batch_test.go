package services

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeElevenLabs returns a client pointing at an httptest server.
// The handler mimics the /v1/speech-to-text endpoint behaviour.
func newFakeElevenLabs(t *testing.T, handler http.HandlerFunc) *ElevenLabsBatchService {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewElevenLabsBatchService(ElevenLabsBatchConfig{
		APIKey:       "test-key",
		WebhookID:    "wh-123",
		ModelID:      "scribe_v2",
		LanguageCode: "tr",
		BaseURL:      srv.URL,
	})
}

func TestNewElevenLabs_NilWithoutAPIKey(t *testing.T) {
	svc := NewElevenLabsBatchService(ElevenLabsBatchConfig{}) // no API key
	if svc != nil {
		t.Error("expected nil when APIKey is empty")
	}
}

func TestSubmit_SendsMultipartWithExpectedFields(t *testing.T) {
	var capturedCT string
	var capturedBody []byte
	var capturedAPIKey string

	svc := newFakeElevenLabs(t, func(w http.ResponseWriter, r *http.Request) {
		capturedCT = r.Header.Get("Content-Type")
		capturedAPIKey = r.Header.Get("xi-api-key")
		capturedBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"request_id":"req_abc"}`))
	})

	resp, err := svc.Submit(context.Background(), SubmitOptions{
		AudioURL: "https://example.com/audio.webm",
		Diarize:  false,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if resp.RequestID != "req_abc" {
		t.Errorf("RequestID = %q", resp.RequestID)
	}
	if !strings.HasPrefix(capturedCT, "multipart/form-data") {
		t.Errorf("Content-Type should be multipart, got %q", capturedCT)
	}
	if capturedAPIKey != "test-key" {
		t.Errorf("xi-api-key header = %q", capturedAPIKey)
	}
	body := string(capturedBody)
	for _, want := range []string{
		"model_id", "scribe_v2",
		"cloud_storage_url", "https://example.com/audio.webm",
		"webhook", "true",
		"webhook_id", "wh-123",
		"language_code", "tr",
		"diarize", "false",
		"tag_audio_events", "true",
		"timestamps_granularity", "word",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
}

func TestSubmit_LanguageOverride(t *testing.T) {
	var capturedBody []byte
	svc := newFakeElevenLabs(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"request_id":"x"}`))
	})
	_, err := svc.Submit(context.Background(), SubmitOptions{
		AudioURL: "https://x/a", Language: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(capturedBody), "language_code") ||
		!strings.Contains(string(capturedBody), "\r\nen\r\n") {
		t.Errorf("per-call language override should appear in body, got: %s", capturedBody)
	}
}

func TestSubmit_RequiresAudioURL(t *testing.T) {
	svc := NewElevenLabsBatchService(ElevenLabsBatchConfig{APIKey: "k", WebhookID: "wh"})
	if _, err := svc.Submit(context.Background(), SubmitOptions{}); err == nil {
		t.Error("expected error for empty AudioURL")
	}
}

func TestSubmit_RequiresWebhookID(t *testing.T) {
	svc := NewElevenLabsBatchService(ElevenLabsBatchConfig{APIKey: "k"}) // no WebhookID
	_, err := svc.Submit(context.Background(), SubmitOptions{AudioURL: "https://x"})
	if err == nil || !strings.Contains(err.Error(), "webhook id") {
		t.Errorf("expected webhook id error, got %v", err)
	}
}

func TestSubmit_SurfacesAPIErrorWithRetryability(t *testing.T) {
	// 429 should be retryable, 400 should not.
	cases := []struct {
		status    int
		retryable bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
	}
	for _, tc := range cases {
		svc := newFakeElevenLabs(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"detail":"boom"}`))
		})
		_, err := svc.Submit(context.Background(), SubmitOptions{AudioURL: "https://x"})
		if err == nil {
			t.Errorf("status %d: expected error", tc.status)
			continue
		}
		var apiErr *ElevenLabsAPIError
		if !errors.As(err, &apiErr) {
			t.Errorf("status %d: error should be *ElevenLabsAPIError, got %T", tc.status, err)
			continue
		}
		if apiErr.Status != tc.status {
			t.Errorf("status = %d, want %d", apiErr.Status, tc.status)
		}
		if apiErr.IsRetryable() != tc.retryable {
			t.Errorf("status %d: IsRetryable = %v, want %v", tc.status, apiErr.IsRetryable(), tc.retryable)
		}
	}
}

func TestSubmit_EmptyRequestIDIsError(t *testing.T) {
	svc := newFakeElevenLabs(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"request_id":""}`))
	})
	_, err := svc.Submit(context.Background(), SubmitOptions{AudioURL: "https://x"})
	if err == nil || !strings.Contains(err.Error(), "empty request_id") {
		t.Errorf("expected empty request_id error, got %v", err)
	}
}
