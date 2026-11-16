package slackoauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ==================== Constructor ====================

func TestNewClient_RequiresCredentials(t *testing.T) {
	t.Parallel()
	cases := map[string][2]string{
		"empty client id":     {"", "secret"},
		"empty client secret": {"id", ""},
		"both empty":          {"", ""},
	}
	for name, v := range cases {
		v := v
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(v[0], v[1]); err == nil {
				t.Errorf("%s: expected error", name)
			}
		})
	}
	if _, err := NewClient("id", "secret"); err != nil {
		t.Fatalf("valid creds failed: %v", err)
	}
}

// ==================== AuthorizeURL ====================

func TestClient_AuthorizeURL_ContainsRequiredParams(t *testing.T) {
	t.Parallel()
	c, _ := NewClient("CID", "CSEC")
	got := c.AuthorizeURL("state123", "https://api.syncrun.net/api/integrations/slack/callback")

	for _, want := range []string{
		"client_id=CID",
		"state=state123",
		"redirect_uri=https%3A%2F%2Fapi.syncrun.net",
		// Scopes from the manifest (comma, but URL-encoded).
		"scope=chat%3Awrite%2Cim%3Awrite%2Cusers%3Aread%2Cusers%3Aread.email",
		"/oauth/v2/authorize",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("AuthorizeURL missing %q in %s", want, got)
		}
	}
}

// ==================== ExchangeCode ====================

func TestClient_ExchangeCode_HappyPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth.v2.access" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != "CID" {
			t.Errorf("client_id not forwarded: %s", r.PostForm.Get("client_id"))
		}
		if r.PostForm.Get("code") != "code-123" {
			t.Errorf("code not forwarded: %s", r.PostForm.Get("code"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"ok": true,
			"access_token": "xoxb-test-token",
			"token_type": "bot",
			"scope": "chat:write,im:write",
			"bot_user_id": "U111",
			"app_id": "A111",
			"team": {"id": "T123", "name": "Syncrun Test"}
		}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	resp, err := c.ExchangeCode(context.Background(), "code-123", "https://cb")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if resp.AccessToken != "xoxb-test-token" {
		t.Errorf("access_token = %q", resp.AccessToken)
	}
	if resp.Team.ID != "T123" || resp.Team.Name != "Syncrun Test" {
		t.Errorf("team parsing off: %+v", resp.Team)
	}
}

func TestClient_ExchangeCode_SlackOKFalse_SurfacesError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": false, "error": "code_already_used"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	_, err := c.ExchangeCode(context.Background(), "code", "cb")
	if err == nil || !strings.Contains(err.Error(), "code_already_used") {
		t.Errorf("expected error carrying Slack reason, got %v", err)
	}
}

func TestClient_ExchangeCode_EmptyAccessToken_Rejected(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": true, "access_token": "", "team": {"id": "T1"}}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	_, err := c.ExchangeCode(context.Background(), "code", "cb")
	if err == nil || !strings.Contains(err.Error(), "empty access_token") {
		t.Errorf("expected empty-token rejection, got %v", err)
	}
}

// ==================== LookupUserByEmail ====================

func TestClient_LookupUserByEmail_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users.lookupByEmail" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("email"); got != "user@example.com" {
			t.Errorf("email not forwarded: %s", got)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("Bearer auth header missing")
		}
		_, _ = w.Write([]byte(`{
			"ok": true,
			"user": {"id": "U888", "name": "alice", "profile": {"email": "user@example.com", "display_name": "Alice"}}
		}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	resp, err := c.LookupUserByEmail(context.Background(), "xoxb-test", "user@example.com")
	if err != nil {
		t.Fatalf("LookupUserByEmail: %v", err)
	}
	if resp.User.ID != "U888" {
		t.Errorf("user.id = %q", resp.User.ID)
	}
}

func TestClient_LookupUserByEmail_NotFound_Sentinel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": false, "error": "users_not_found"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	_, err := c.LookupUserByEmail(context.Background(), "xoxb", "missing@example.com")
	if !errors.Is(err, ErrSlackUserNotFound) {
		t.Errorf("expected ErrSlackUserNotFound, got %v", err)
	}
}

// ==================== OpenDM ====================

func TestClient_OpenDM_ReturnsChannelID(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("users") != "U123" {
			t.Errorf("users param not forwarded: %s", r.PostForm.Get("users"))
		}
		_, _ = w.Write([]byte(`{"ok": true, "channel": {"id": "D999"}}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	id, err := c.OpenDM(context.Background(), "xoxb", "U123")
	if err != nil {
		t.Fatalf("OpenDM: %v", err)
	}
	if id != "D999" {
		t.Errorf("channel id = %q, want D999", id)
	}
}

func TestClient_OpenDM_SlackErrorPropagated(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": false, "error": "cannot_dm_bot"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("CID", "CSEC")
	c.WithBaseURL(srv.URL)
	_, err := c.OpenDM(context.Background(), "xoxb", "U123")
	if err == nil || !strings.Contains(err.Error(), "cannot_dm_bot") {
		t.Errorf("expected Slack error propagated, got %v", err)
	}
}
