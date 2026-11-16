package briefnotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudFunctionEmailTransport_SendSuccess(t *testing.T) {
	var gotSecret string
	var gotPayload map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.Header.Get("X-Internal-Service-Secret")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer srv.Close()

	tr, err := NewCloudFunctionEmailTransport(srv.URL, "shared-secret")
	if err != nil {
		t.Fatalf("NewCloudFunctionEmailTransport: %v", err)
	}

	msgID, err := tr.Send(context.Background(), EmailPayload{
		To:        []string{"user@example.com"},
		Subject:   "Weekly Sync Brief",
		Text:      "Plain summary",
		HTML:      "<p>HTML summary</p>",
		MessageID: "<msg-123@mia.local>",
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if msgID != "<msg-123@mia.local>" {
		t.Fatalf("expected message ID to propagate, got %q", msgID)
	}
	if gotSecret != "shared-secret" {
		t.Fatalf("expected X-Internal-Service-Secret header, got %q", gotSecret)
	}
	if gotPayload["recipient"] != "user@example.com" || gotPayload["html"] != "<p>HTML summary</p>" {
		t.Fatalf("unexpected payload: %+v", gotPayload)
	}
}

func TestCloudFunctionEmailTransport_5xxIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	tr, err := NewCloudFunctionEmailTransport(srv.URL, "")
	if err != nil {
		t.Fatalf("NewCloudFunctionEmailTransport: %v", err)
	}
	_, err = tr.Send(context.Background(), EmailPayload{
		To:      []string{"user@example.com"},
		Subject: "Test",
		Text:    "Body",
	})
	if err == nil || !IsRetryable(err) {
		t.Fatalf("expected retryable error on 503, got %v", err)
	}
}
