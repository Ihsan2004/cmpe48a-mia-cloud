package briefnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CloudFunctionEmailTransport implements EmailTransport by forwarding rendered
// brief emails to the stateless `brief-notifier` Google Cloud Function.
type CloudFunctionEmailTransport struct {
	functionURL  string
	sharedSecret string
	httpClient   *http.Client
}

// NewCloudFunctionEmailTransport constructs an EmailTransport targeting brief-notifier.
func NewCloudFunctionEmailTransport(functionURL, sharedSecret string) (*CloudFunctionEmailTransport, error) {
	trimmed := strings.TrimSpace(functionURL)
	if trimmed == "" {
		return nil, fmt.Errorf("briefnotify: function URL is required")
	}
	return &CloudFunctionEmailTransport{
		functionURL:  trimmed,
		sharedSecret: sharedSecret,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Send POSTs the rendered email payload to the brief-notifier Cloud Function.
func (t *CloudFunctionEmailTransport) Send(ctx context.Context, payload EmailPayload) (string, error) {
	if len(payload.To) == 0 || strings.TrimSpace(payload.To[0]) == "" {
		return "", fmt.Errorf("briefnotify: recipient email is empty")
	}

	reqBody := map[string]interface{}{
		"channel":       "email",
		"recipient":     payload.To[0],
		"from":          payload.From,
		"subject":       payload.Subject,
		"body":          payload.Text,
		"html":          payload.HTML,
		"extra_headers": payload.ExtraHeaders,
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("briefnotify: marshal cloud function payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.functionURL, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("briefnotify: build cloud function request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if t.sharedSecret != "" {
		req.Header.Set("X-Internal-Service-Secret", t.sharedSecret)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", Retryable(fmt.Errorf("briefnotify: cloud function call failed: %w", err))
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", Retryable(fmt.Errorf("briefnotify: cloud function HTTP %d: %s", resp.StatusCode, string(respBody)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("briefnotify: cloud function rejected HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if payload.MessageID != "" {
		return payload.MessageID, nil
	}
	return fmt.Sprintf("cf-%d", time.Now().UnixNano()), nil
}
