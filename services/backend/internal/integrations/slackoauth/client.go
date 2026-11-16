package slackoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client carries the OAuth app credentials + an HTTP client + the
// base URL. Base URL is per-instance (not a package global) so tests
// running t.Parallel() can point different clients at different
// httptest servers without racing.
type Client struct {
	clientID     string
	clientSecret string
	baseURL      string
	http         *http.Client
}

// NewClient requires ID + secret — refusing to build with empty values
// means the handler doesn't have to re-check at every request. Base
// URL defaults to Slack's production endpoint.
func NewClient(clientID, clientSecret string) (*Client, error) {
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("slackoauth: clientID and clientSecret are required")
	}
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      "https://slack.com",
		http:         &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// WithBaseURL overrides the Slack API base — test-only hook.
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = u
	return c
}

// WithHTTPClient swaps the internal HTTP client. Test-only hook.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// AuthorizeURL builds the install URL the SPA redirects the user to.
// Scope list MUST mirror slack_app_manifest.yaml — a mismatch makes
// Slack reject the consent request with "invalid_scope".
func (c *Client) AuthorizeURL(state, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("scope", "chat:write,im:write,users:read,users:read.email")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return c.baseURL + "/oauth/v2/authorize?" + q.Encode()
}

// OAuthV2AccessResponse mirrors the fields we care about from Slack's
// oauth.v2.access response. Slack emits many more fields (app_id,
// incoming_webhook, enterprise, etc) — we drop them deliberately so
// unused payload doesn't creep into logs.
type OAuthV2AccessResponse struct {
	OK          bool     `json:"ok"`
	Error       string   `json:"error,omitempty"`
	AccessToken string   `json:"access_token"`         // bot token xoxb-...
	TokenType   string   `json:"token_type"`           // "bot"
	Scope       string   `json:"scope"`                // csv
	BotUserID   string   `json:"bot_user_id"`
	AppID       string   `json:"app_id"`
	Team        teamInfo `json:"team"`
}

type teamInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ExchangeCode posts the authorization code + redirect to Slack and
// returns the bot token on success. "ok=false" from Slack becomes a
// non-nil err carrying the Slack-reported reason ("code_already_used",
// "bad_redirect_uri", etc.) so the handler can surface it.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*OAuthV2AccessResponse, error) {
	body := url.Values{}
	body.Set("client_id", c.clientID)
	body.Set("client_secret", c.clientSecret)
	body.Set("code", code)
	body.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/oauth.v2.access", strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("slackoauth: build oauth.v2.access request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slackoauth: oauth.v2.access call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("slackoauth: oauth.v2.access read: %w", err)
	}

	var out OAuthV2AccessResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("slackoauth: oauth.v2.access decode: %w", err)
	}
	if !out.OK {
		reason := out.Error
		if reason == "" {
			reason = "unknown"
		}
		return nil, fmt.Errorf("slackoauth: oauth.v2.access refused: %s", reason)
	}
	if out.AccessToken == "" {
		return nil, errors.New("slackoauth: oauth.v2.access returned empty access_token")
	}
	return &out, nil
}

// UsersLookupByEmailResponse is the trimmed view of users.lookupByEmail
// we need — user id + email + a few optional display fields.
type UsersLookupByEmailResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	User  struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Profile struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"profile"`
	} `json:"user"`
}

// LookupUserByEmail returns the Slack user row whose profile email
// matches the argument, or a typed error when Slack says "users_not_found".
// Callers should treat NotFound separately from generic transport
// errors — it drives the Phase-4 "pending email" flow.
func (c *Client) LookupUserByEmail(ctx context.Context, botToken, email string) (*UsersLookupByEmailResponse, error) {
	q := url.Values{}
	q.Set("email", email)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/users.lookupByEmail?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("slackoauth: build lookupByEmail: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+botToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slackoauth: lookupByEmail call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("slackoauth: lookupByEmail read: %w", err)
	}
	var out UsersLookupByEmailResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("slackoauth: lookupByEmail decode: %w", err)
	}
	if !out.OK {
		if out.Error == "users_not_found" {
			return &out, ErrSlackUserNotFound
		}
		return &out, fmt.Errorf("slackoauth: lookupByEmail refused: %s", out.Error)
	}
	return &out, nil
}

// ErrSlackUserNotFound is the canonical sentinel for a users.lookupByEmail
// miss. Handler translates this into HTTP 404/UI "pending email" state.
var ErrSlackUserNotFound = errors.New("slackoauth: slack user not found by email")

// ConversationsOpenResponse — we only need the channel id.
type ConversationsOpenResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
}

// OpenDM calls conversations.open to get a DM channel id for a given
// Slack user. We cache the returned channel id on the slack_integrations
// row so subsequent deliveries skip this call.
func (c *Client) OpenDM(ctx context.Context, botToken, userID string) (string, error) {
	body := url.Values{}
	body.Set("users", userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/conversations.open", strings.NewReader(body.Encode()))
	if err != nil {
		return "", fmt.Errorf("slackoauth: build conversations.open: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("slackoauth: conversations.open call: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("slackoauth: conversations.open read: %w", err)
	}
	var out ConversationsOpenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("slackoauth: conversations.open decode: %w", err)
	}
	if !out.OK {
		return "", fmt.Errorf("slackoauth: conversations.open refused: %s", out.Error)
	}
	if out.Channel.ID == "" {
		return "", errors.New("slackoauth: conversations.open returned empty channel id")
	}
	return out.Channel.ID, nil
}
