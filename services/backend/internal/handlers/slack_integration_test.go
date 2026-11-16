package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/integrations/slackoauth"
	"mia-website/backend/internal/models"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ==================== Fakes ====================

type fakeSlackRepo struct {
	store      map[uuid.UUID]*models.SlackIntegration
	dmUpdates  []dmUpdate
	revokedIDs []uuid.UUID
}

type dmUpdate struct {
	id            uuid.UUID
	dmChannelID   string
	matchedEmail  string
}

func newFakeSlackRepo() *fakeSlackRepo {
	return &fakeSlackRepo{store: map[uuid.UUID]*models.SlackIntegration{}}
}

func (f *fakeSlackRepo) Upsert(ctx context.Context, integ *models.SlackIntegration) error {
	if integ.ID == uuid.Nil {
		integ.ID = uuid.New()
	}
	clone := *integ
	f.store[integ.ID] = &clone
	return nil
}

func (f *fakeSlackRepo) GetActiveForUser(ctx context.Context, userID uuid.UUID) (*models.SlackIntegration, error) {
	for _, v := range f.store {
		if v.UserID == userID && v.RevokedAt == nil {
			clone := *v
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeSlackRepo) GetByUserAndTeam(ctx context.Context, userID uuid.UUID, teamID string) (*models.SlackIntegration, error) {
	for _, v := range f.store {
		if v.UserID == userID && v.TeamID == teamID {
			clone := *v
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeSlackRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	f.revokedIDs = append(f.revokedIDs, id)
	if v, ok := f.store[id]; ok {
		now := time.Now().UTC()
		v.RevokedAt = &now
	}
	return nil
}

func (f *fakeSlackRepo) UpdateDMChannel(ctx context.Context, id uuid.UUID, dm, email string) error {
	f.dmUpdates = append(f.dmUpdates, dmUpdate{id: id, dmChannelID: dm, matchedEmail: email})
	if v, ok := f.store[id]; ok {
		d := dm
		e := email
		v.DMChannelID = &d
		v.MatchedEmail = &e
	}
	return nil
}

type fakeProfileEmails struct {
	email string
}

func (f *fakeProfileEmails) EmailForUser(userID uuid.UUID) (string, error) { return f.email, nil }

// ==================== Test harness ====================

const slackStateSecret = "slack-handler-state-secret-32-bytes-x!!"

// buildHandler creates a fully-wired SlackIntegrationHandler pointing at
// the given httptest server as Slack's API base. Per-instance baseURL
// (not a package global) means parallel tests don't race.
func buildHandler(t *testing.T, slackMockServer *httptest.Server, profileEmail string) (*SlackIntegrationHandler, *fakeSlackRepo) {
	t.Helper()

	client, err := slackoauth.NewClient("CID", "CSEC")
	if err != nil {
		t.Fatal(err)
	}
	client.WithBaseURL(slackMockServer.URL)

	// Use an all-zero 32-byte key for deterministic AES-GCM. Random
	// keys work too but make failures harder to debug across runs.
	encKey := "0000000000000000000000000000000000000000000000000000000000000000"
	cipher, _ := slackoauth.NewTokenCipher(encKey)
	state, _ := slackoauth.NewStateSigner(slackStateSecret)

	repo := newFakeSlackRepo()
	h, err := NewSlackIntegrationHandler(SlackIntegrationConfig{
		Repo:             repo,
		Client:           client,
		State:            state,
		Cipher:           cipher,
		RedirectURI:      "https://api.syncrun.net/api/integrations/slack/callback",
		FrontendCallback: "https://console.syncrun.net/settings/notifications",
		ProfileEmails:    &fakeProfileEmails{email: profileEmail},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, repo
}

func testAppWithUser(userID uuid.UUID, register func(app *fiber.App)) *fiber.App {
	app := fiber.New()
	// Inject userID into every request so the handler's c.Locals lookup works.
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("userID", userID.String())
		return c.Next()
	})
	register(app)
	return app
}

// ==================== Install ====================

func TestSlackInstall_ReturnsConsentURLAsJSON(t *testing.T) {
	t.Parallel()

	// Install returns JSON {url: ...} (not a 302) so the SPA can
	// fetch it with its JWT bearer and then full-page-navigate. A
	// full-page navigation to /install itself would drop the auth
	// header and land on the JWT middleware with 401.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer mock.Close()

	h, _ := buildHandler(t, mock, "")

	userID := uuid.New()
	app := testAppWithUser(userID, func(a *fiber.App) { a.Get("/install", h.Install) })

	req, _ := http.NewRequest("GET", "/install", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	url, _ := parsed["url"].(string)
	if !strings.Contains(url, "/oauth/v2/authorize") {
		t.Errorf("url missing oauth path: %s", url)
	}
	if !strings.Contains(url, "state=") || !strings.Contains(url, "client_id=CID") {
		t.Errorf("url missing state or client_id: %s", url)
	}
}

// ==================== Callback ====================

func TestSlackCallback_Happy_MatchedEmailCached(t *testing.T) {
	t.Parallel()

	// Mock Slack: oauth.v2.access returns a bot token, users.lookupByEmail
	// matches the profile email, conversations.open returns a DM channel.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			_, _ = w.Write([]byte(`{
				"ok": true, "access_token": "xoxb-real",
				"token_type": "bot", "scope": "chat:write",
				"bot_user_id": "U1", "app_id": "A1",
				"team": {"id": "T1", "name": "Test Team"}
			}`))
		case "/api/users.lookupByEmail":
			_, _ = w.Write([]byte(`{"ok": true, "user": {"id": "U42", "profile": {"email": "user@example.com"}}}`))
		case "/api/conversations.open":
			_, _ = w.Write([]byte(`{"ok": true, "channel": {"id": "D777"}}`))
		default:
			t.Errorf("unexpected slack path: %s", r.URL.Path)
		}
	}))
	defer mock.Close()

	h, repo := buildHandler(t, mock, "user@example.com")


	userID := uuid.New()
	app := testAppWithUser(userID, func(a *fiber.App) { a.Get("/callback", h.Callback) })

	// Generate a real state matching the userID so Validate passes.
	state := h.state.Generate(userID)

	req, _ := http.NewRequest("GET", "/callback?state="+state+"&code=abc", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode/100 != 3 {
		t.Errorf("expected redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "slack=connected") {
		t.Errorf("expected slack=connected in redirect, got %s", loc)
	}

	// DB side: integration persisted with matched email + DM channel.
	got, _ := repo.GetActiveForUser(context.Background(), userID)
	if got == nil {
		t.Fatal("integration not upserted")
	}
	if got.MatchedEmail == nil || *got.MatchedEmail != "user@example.com" {
		t.Errorf("matched_email wrong: %v", got.MatchedEmail)
	}
	if got.DMChannelID == nil || *got.DMChannelID != "D777" {
		t.Errorf("dm_channel_id wrong: %v", got.DMChannelID)
	}
	// Stored token must be ciphertext, not the raw bot token.
	if strings.Contains(got.BotTokenEncrypted, "xoxb-real") {
		t.Error("bot token stored as plaintext")
	}
}

func TestSlackCallback_LookupFails_RedirectsNeedsEmail(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			_, _ = w.Write([]byte(`{"ok": true, "access_token": "xoxb", "scope": "chat:write", "bot_user_id": "U1", "team": {"id": "T1"}}`))
		case "/api/users.lookupByEmail":
			_, _ = w.Write([]byte(`{"ok": false, "error": "users_not_found"}`))
		}
	}))
	defer mock.Close()

	h, repo := buildHandler(t, mock, "profile@example.com")


	userID := uuid.New()
	app := testAppWithUser(userID, func(a *fiber.App) { a.Get("/callback", h.Callback) })
	state := h.state.Generate(userID)

	req, _ := http.NewRequest("GET", "/callback?state="+state+"&code=abc", nil)
	resp, _ := app.Test(req, -1)
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "slack=needs_email") {
		t.Errorf("expected slack=needs_email, got %s", loc)
	}
	got, _ := repo.GetActiveForUser(context.Background(), userID)
	if got == nil || got.MatchedEmail != nil {
		t.Error("integration should be persisted with nil matched_email")
	}
}

func TestSlackCallback_InvalidState_Rejects(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("slack should not be called with invalid state, path=%s", r.URL.Path)
	}))
	defer mock.Close()

	h, _ := buildHandler(t, mock, "")


	app := testAppWithUser(uuid.New(), func(a *fiber.App) { a.Get("/callback", h.Callback) })
	req, _ := http.NewRequest("GET", "/callback?state=garbage&code=c", nil)
	resp, _ := app.Test(req, -1)
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "error=invalid_state") {
		t.Errorf("expected error=invalid_state redirect, got %s", loc)
	}
}

func TestSlackCallback_UserRejectedConsent_SilentRedirect(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("slack should NOT be called when user rejected consent, path=%s", r.URL.Path)
	}))
	defer mock.Close()

	h, _ := buildHandler(t, mock, "")


	app := testAppWithUser(uuid.New(), func(a *fiber.App) { a.Get("/callback", h.Callback) })
	req, _ := http.NewRequest("GET", "/callback?error=access_denied", nil)
	resp, _ := app.Test(req, -1)
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "slack=error") || !strings.Contains(loc, "error=access_denied") {
		t.Errorf("unexpected redirect: %s", loc)
	}
}

// ==================== ResolveEmail ====================

func TestSlackResolveEmail_Happy(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/users.lookupByEmail":
			_, _ = w.Write([]byte(`{"ok": true, "user": {"id": "U9"}}`))
		case "/api/conversations.open":
			_, _ = w.Write([]byte(`{"ok": true, "channel": {"id": "D9"}}`))
		}
	}))
	defer mock.Close()

	h, repo := buildHandler(t, mock, "")


	// Seed a pending-email integration.
	userID := uuid.New()
	ciphertext, _ := h.cipher.Encrypt("xoxb-real")
	_ = repo.Upsert(context.Background(), &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: ciphertext, Scope: "chat:write",
	})

	app := testAppWithUser(userID, func(a *fiber.App) { a.Post("/resolve", h.ResolveEmail) })
	req, _ := http.NewRequest("POST", "/resolve", strings.NewReader(`{"email":"alt@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}

	if len(repo.dmUpdates) != 1 || repo.dmUpdates[0].matchedEmail != "alt@example.com" {
		t.Errorf("dm update not recorded: %+v", repo.dmUpdates)
	}
}

func TestSlackResolveEmail_NotFound_404(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok": false, "error": "users_not_found"}`))
	}))
	defer mock.Close()

	h, repo := buildHandler(t, mock, "")


	userID := uuid.New()
	ciphertext, _ := h.cipher.Encrypt("xoxb-real")
	_ = repo.Upsert(context.Background(), &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: ciphertext, Scope: "chat:write",
	})

	app := testAppWithUser(userID, func(a *fiber.App) { a.Post("/resolve", h.ResolveEmail) })
	req, _ := http.NewRequest("POST", "/resolve", strings.NewReader(`{"email":"missing@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusNotFound {
		t.Errorf("expected 404 for missing email, got %d", resp.StatusCode)
	}
}

func TestSlackResolveEmail_NoIntegration_404(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer mock.Close()
	h, _ := buildHandler(t, mock, "")


	app := testAppWithUser(uuid.New(), func(a *fiber.App) { a.Post("/resolve", h.ResolveEmail) })
	req, _ := http.NewRequest("POST", "/resolve", strings.NewReader(`{"email":"x@y.z"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusNotFound {
		t.Errorf("expected 404 when no integration exists, got %d", resp.StatusCode)
	}
}

// ==================== Disconnect ====================

func TestSlackDisconnect_Revokes(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer mock.Close()
	h, repo := buildHandler(t, mock, "")


	userID := uuid.New()
	_ = repo.Upsert(context.Background(), &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: "x", Scope: "s",
	})

	app := testAppWithUser(userID, func(a *fiber.App) { a.Post("/disconnect", h.Disconnect) })
	req, _ := http.NewRequest("POST", "/disconnect", nil)
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(repo.revokedIDs) != 1 {
		t.Errorf("expected 1 revoke call, got %d", len(repo.revokedIDs))
	}
}

// ==================== Status ====================

func TestSlackStatus_NoIntegration(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer mock.Close()
	h, _ := buildHandler(t, mock, "")


	app := testAppWithUser(uuid.New(), func(a *fiber.App) { a.Get("/status", h.Status) })
	req, _ := http.NewRequest("GET", "/status", nil)
	resp, _ := app.Test(req, -1)

	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if connected, ok := parsed["connected"].(bool); !ok || connected {
		t.Errorf("expected connected=false, got %v", parsed)
	}
}

func TestSlackStatus_Connected(t *testing.T) {
	t.Parallel()

	mock := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer mock.Close()
	h, repo := buildHandler(t, mock, "")


	userID := uuid.New()
	email := "user@example.com"
	_ = repo.Upsert(context.Background(), &models.SlackIntegration{
		UserID: userID, TeamID: "T1", BotUserID: "U1",
		BotTokenEncrypted: "x", Scope: "s",
		TeamName:     strPtr("Test Team"),
		MatchedEmail: &email,
	})

	app := testAppWithUser(userID, func(a *fiber.App) { a.Get("/status", h.Status) })
	req, _ := http.NewRequest("GET", "/status", nil)
	resp, _ := app.Test(req, -1)

	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	if connected, _ := parsed["connected"].(bool); !connected {
		t.Errorf("expected connected=true, got %v", parsed)
	}
	if team, _ := parsed["team_name"].(string); team != "Test Team" {
		t.Errorf("team_name wrong: %v", parsed["team_name"])
	}
	if pending, _ := parsed["pending_email"].(bool); pending {
		t.Errorf("pending_email should be false when matched_email set, got %v", parsed)
	}
}

func strPtr(s string) *string { return &s }
