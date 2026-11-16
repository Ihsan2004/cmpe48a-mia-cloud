package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

// EmbeddingClient is the narrow embedding-only surface exposed to
// callers that don't need the rest of the post-meeting plumbing.
// Stage 3 of the chain matcher (decision-based binding) consumes
// this; in tests it's stubbed with a deterministic in-memory impl.
//
// EmbedTexts MUST return one [1536]-element float64 slice per input
// text in the same order. nil entries inside the result mark per-text
// failures; the caller filters them.
//
// userID + meetingID are issue #39 attribution hints. When userID is
// uuid.Nil the implementation MUST skip usage recording (e.g. boot-
// time or system-internal calls with no tenant context). meetingID
// is optional — chain-level centroid rebuilds aren't tied to a
// specific meeting and pass nil.
type EmbeddingClient interface {
	EmbedTexts(ctx context.Context, userID uuid.UUID, meetingID *uuid.UUID, texts []string) ([][]float64, error)
}

// httpEmbeddingClient is the production EmbeddingClient that calls
// the mia-search NLP service. The route + payload shape mirror what
// PostMeetingService.generateBatchEmbeddings already uses, kept here
// in a smaller surface so Stage 3 doesn't have to drag in the whole
// PostMeetingService.
type httpEmbeddingClient struct {
	baseURL       string
	client        *http.Client
	usageRecorder *UsageRecorder // issue #39 attribution; nil disables recording
}

// NewEmbeddingClient returns a production-ready EmbeddingClient. URL
// is read from NLP_SERVICE_URL with the same default
// PostMeetingService uses, so deployments that already work for
// summary embeddings work for Stage 3 with no extra config.
func NewEmbeddingClient() EmbeddingClient {
	url := os.Getenv("NLP_SERVICE_URL")
	if url == "" {
		url = "http://mia-search:8000"
	}
	return &httpEmbeddingClient{
		baseURL:       url,
		client:        &http.Client{Timeout: 30 * time.Second},
		usageRecorder: NewUsageRecorder(),
	}
}

func (c *httpEmbeddingClient) EmbedTexts(ctx context.Context, userID uuid.UUID, meetingID *uuid.UUID, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		return nil, fmt.Errorf("EmbedTexts: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/embeddings/generate-batch", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("EmbedTexts: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := SetInternalServiceAuthHeader(req); err != nil {
		return nil, fmt.Errorf("EmbedTexts: auth header: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("EmbedTexts: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("EmbedTexts: NLP service status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed BatchEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("EmbedTexts: decode response: %w", err)
	}

	// Issue #39 — chain v3 (Stage 3) embedding cost attribution. Skip
	// when caller is a system-internal context (userID==Nil) or the
	// server didn't ship usage (older mia-search). usageRecorder.Record
	// is async + atomic so this adds no latency to the matcher path.
	if c.usageRecorder != nil && userID != uuid.Nil && parsed.UsageTotalTokens > 0 {
		model := parsed.Model
		if model == "" {
			model = "text-embedding-3-small"
		}
		c.usageRecorder.Record(
			userID, nil, meetingID,
			"embedding", "openai", model,
			parsed.UsagePromptTokens, 0, 0,
			map[string]interface{}{
				"feature_origin": "chain_v3_stage3",
				"text_count":     len(texts),
			},
		)
	}

	return parsed.Embeddings, nil
}
