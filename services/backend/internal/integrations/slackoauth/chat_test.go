package slackoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ==================== PostMessage — happy path ====================

func TestClient_PostMessage_HappyPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat.postMessage" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer xoxb-test" {
			t.Errorf("bearer auth wrong: %s", auth)
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["channel"] != "D1" {
			t.Errorf("channel not forwarded: %v", body["channel"])
		}
		if body["text"] != "fallback text" {
			t.Errorf("text not forwarded: %v", body["text"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1234.5678","channel":"D1"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)

	ts, err := c.PostMessage(context.Background(), "xoxb-test", "D1", "fallback text", json.RawMessage(`[{"type":"section"}]`))
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if ts != "1234.5678" {
		t.Errorf("ts = %q, want 1234.5678", ts)
	}
}

// ==================== Invalid auth sentinel ====================

func TestClient_PostMessage_InvalidAuth_Sentinel(t *testing.T) {
	t.Parallel()

	// All four invalid_auth-class error codes map to ErrSlackInvalidAuth.
	codes := []string{"invalid_auth", "not_authed", "token_revoked", "account_inactive"}
	for _, code := range codes {
		code := code
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"ok":false,"error":"` + code + `"}`))
			}))
			defer srv.Close()

			c, _ := NewClient("CID", "CSEC")
			c.WithBaseURL(srv.URL)
			_, err := c.PostMessage(context.Background(), "xoxb", "D1", "t", nil)
			if !errors.Is(err, ErrSlackInvalidAuth) {
				t.Errorf("%s: expected ErrSlackInvalidAuth, got %v", code, err)
			}
		})
	}
}

// ==================== Rate limit (429) ====================

func TestClient_PostMessage_429_ReturnsRateLimitedSentinel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	_, err := c.PostMessage(context.Background(), "xoxb", "D1", "t", nil)
	if !errors.Is(err, ErrSlackRateLimited) {
		t.Errorf("expected ErrSlackRateLimited, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry-after=3") {
		t.Errorf("error message should carry retry-after: %v", err)
	}
}

// ==================== Permanent slack error ====================

func TestClient_PostMessage_OtherSlackError_Permanent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	_, err := c.PostMessage(context.Background(), "xoxb", "D1", "t", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrSlackInvalidAuth) || errors.Is(err, ErrSlackRateLimited) || errors.Is(err, ErrSlackTransport) {
		t.Errorf("permanent error must not match transient sentinels, got %v", err)
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("error must carry Slack's reason: %v", err)
	}
}

// ==================== Transport failures ====================

func TestClient_PostMessage_NetworkError_TransportSentinel(t *testing.T) {
	t.Parallel()

	// Start then close the server so the client dial fails.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	addr := srv.URL
	srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(addr)
	_, err := c.PostMessage(context.Background(), "xoxb", "D1", "t", nil)
	if !errors.Is(err, ErrSlackTransport) {
		t.Errorf("expected ErrSlackTransport, got %v", err)
	}
}

// ==================== Validation ====================

func TestClient_PostMessage_ValidatesArgs(t *testing.T) {
	t.Parallel()
	c, _ := NewClient("CID", "CSEC")
	if _, err := c.PostMessage(context.Background(), "", "D1", "t", nil); err == nil {
		t.Error("expected error on empty botToken")
	}
	if _, err := c.PostMessage(context.Background(), "xoxb", "", "t", nil); err == nil {
		t.Error("expected error on empty channelID")
	}
}

// ==================== parseRetryAfter ====================

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	cases := map[string]time.Duration{
		"":        1 * time.Second,
		"0":       1 * time.Second,
		"-5":      1 * time.Second,
		"abc":     1 * time.Second,
		"3":       3 * time.Second,
		"60":      60 * time.Second,
		"  10  ":  10 * time.Second,
	}
	for in, want := range cases {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			if got := parseRetryAfter(in); got != want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

// ==================== WorkspaceRateLimiter ====================

func TestWorkspaceRateLimiter_SerialisesPerWorkspace(t *testing.T) {
	t.Parallel()

	// Two concurrent calls against the same teamID should take at
	// least `gap` wall-clock time in total; different teams run in
	// parallel with no enforced gap.
	gap := 50 * time.Millisecond
	l := NewWorkspaceRateLimiter().WithGap(gap)

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Wait(context.Background(), "T1")
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 3 calls × 50ms gap — first is immediate, next two serialised.
	// Lower bound: 2*gap = 100ms (minus scheduler slack).
	if elapsed < 2*gap-10*time.Millisecond {
		t.Errorf("expected >= %v for 3 serial calls, got %v", 2*gap, elapsed)
	}
}

func TestWorkspaceRateLimiter_DifferentTeamsParallel(t *testing.T) {
	t.Parallel()

	gap := 200 * time.Millisecond
	l := NewWorkspaceRateLimiter().WithGap(gap)

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine has its own team → no gap enforced
			// between different teams.
			_ = l.Wait(context.Background(), "T"+string(rune('A'+i)))
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if elapsed >= gap {
		t.Errorf("different teams should run in parallel, took %v (gap=%v)", elapsed, gap)
	}
}

func TestWorkspaceRateLimiter_ContextCanceled(t *testing.T) {
	t.Parallel()

	l := NewWorkspaceRateLimiter().WithGap(1 * time.Second)
	// Warm the limiter so the next call needs to wait.
	_ = l.Wait(context.Background(), "T1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := l.Wait(ctx, "T1")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestWorkspaceRateLimiter_EmptyTeamID_NoOp(t *testing.T) {
	t.Parallel()
	l := NewWorkspaceRateLimiter().WithGap(1 * time.Second)
	start := time.Now()
	_ = l.Wait(context.Background(), "")
	if time.Since(start) > 50*time.Millisecond {
		t.Error("empty team id should not wait")
	}
}
