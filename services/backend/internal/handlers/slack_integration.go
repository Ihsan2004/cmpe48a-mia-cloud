package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"mia-website/backend/internal/integrations/slackoauth"
	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// SlackIntegrationHandler owns the full install + disconnect flow:
//
//	GET  /api/integrations/slack/install        (JWT) → redirect to Slack consent
//	GET  /api/integrations/slack/callback       (public) → code exchange, store token
//	POST /api/integrations/slack/resolve-email  (JWT) → alternative email → open DM
//	POST /api/integrations/slack/disconnect     (JWT) → revoke integration
//	GET  /api/integrations/slack/status         (JWT) → connected + team_name
//
// ProfileEmailLookup is the narrow view of ProfileService we need — just
// "what is this user's Syncrun email?". Abstracted so tests don't need
// the full service.
type ProfileEmailLookup interface {
	EmailForUser(userID uuid.UUID) (string, error)
}

// SlackIntegrationHandler depends on the integration repo + Slack
// client + state signer + token cipher. Empty fields (nil handler) →
// routes aren't registered, so half-wired deploys don't expose the
// endpoints.
type SlackIntegrationHandler struct {
	repo             repository.SlackIntegrationRepository
	client           *slackoauth.Client
	state            *slackoauth.StateSigner
	cipher           *slackoauth.TokenCipher
	redirectURI      string
	frontendCallback string
	profileEmails    ProfileEmailLookup
}

// SlackIntegrationConfig bundles the inputs so main.go doesn't have to
// pass six arguments. Any missing dependency panics at New() rather
// than surfacing later during a callback.
type SlackIntegrationConfig struct {
	Repo             repository.SlackIntegrationRepository
	Client           *slackoauth.Client
	State            *slackoauth.StateSigner
	Cipher           *slackoauth.TokenCipher
	RedirectURI      string
	FrontendCallback string
	ProfileEmails    ProfileEmailLookup
}

func NewSlackIntegrationHandler(cfg SlackIntegrationConfig) (*SlackIntegrationHandler, error) {
	if cfg.Repo == nil || cfg.Client == nil || cfg.State == nil || cfg.Cipher == nil {
		return nil, errors.New("handlers: SlackIntegrationHandler missing required dependency")
	}
	if cfg.RedirectURI == "" || cfg.FrontendCallback == "" {
		return nil, errors.New("handlers: SlackIntegrationHandler requires redirect + frontend URLs")
	}
	if cfg.ProfileEmails == nil {
		return nil, errors.New("handlers: SlackIntegrationHandler requires ProfileEmails")
	}
	return &SlackIntegrationHandler{
		repo:             cfg.Repo,
		client:           cfg.Client,
		state:            cfg.State,
		cipher:           cfg.Cipher,
		redirectURI:      cfg.RedirectURI,
		frontendCallback: cfg.FrontendCallback,
		profileEmails:    cfg.ProfileEmails,
	}, nil
}

// Install returns the Slack consent URL as JSON so the SPA can navigate
// to it via window.location.assign. We used to 302-redirect here, but
// browsers don't forward the Authorization header on a full-page
// navigation — so the request landed on the JWT middleware without a
// token and got 401'd. Returning JSON lets the SPA fetch this endpoint
// with its bearer, then navigate to the embedded URL.
//
// State carries the userID so the public /callback can tie the install
// back to the right Syncrun account without trusting query params.
func (h *SlackIntegrationHandler) Install(c *fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	state := h.state.Generate(userID)
	if state == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to sign state"})
	}
	return c.JSON(fiber.Map{"url": h.client.AuthorizeURL(state, h.redirectURI)})
}

// Callback is Slack's OAuth landing. We validate state, exchange the
// code for a bot token, attempt to match the user by their Syncrun
// email, then redirect back to the SPA. The SPA picks up the result
// from the ?slack= query param (connected | needs_email | error).
func (h *SlackIntegrationHandler) Callback(c *fiber.Ctx) error {
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))

	// User rejected on the Slack consent screen — Slack returns an
	// error param instead of a code. Respect their choice silently.
	if slackErr := strings.TrimSpace(c.Query("error")); slackErr != "" {
		return c.Redirect(h.frontendURL("error", slackErr))
	}
	if state == "" || code == "" {
		return c.Redirect(h.frontendURL("error", "missing_state_or_code"))
	}

	userID, err := h.state.Validate(state)
	if err != nil {
		slog.Warn("slack oauth: state validation failed", "error", err)
		return c.Redirect(h.frontendURL("error", "invalid_state"))
	}

	access, err := h.client.ExchangeCode(c.Context(), code, h.redirectURI)
	if err != nil {
		slog.Warn("slack oauth: code exchange failed", "user_id", userID, "error", err)
		return c.Redirect(h.frontendURL("error", "code_exchange_failed"))
	}

	ciphertext, err := h.cipher.Encrypt(access.AccessToken)
	if err != nil {
		slog.Error("slack oauth: token encrypt failed", "user_id", userID, "error", err)
		return c.Redirect(h.frontendURL("error", "encrypt_failed"))
	}

	integration := &models.SlackIntegration{
		UserID:            userID,
		TeamID:            access.Team.ID,
		TeamName:          nullableString(access.Team.Name),
		BotUserID:         access.BotUserID,
		BotTokenEncrypted: ciphertext,
		Scope:             access.Scope,
	}

	// Try matching the user's Syncrun email to a Slack user in this
	// workspace; if it hits, cache the DM channel too so Phase 5 can
	// skip the call per-delivery.
	syncEmail, _ := h.profileEmails.EmailForUser(userID)
	if syncEmail != "" {
		lookup, lookupErr := h.client.LookupUserByEmail(c.Context(), access.AccessToken, syncEmail)
		if lookupErr == nil && lookup != nil && lookup.User.ID != "" {
			integration.MatchedEmail = &syncEmail
			if dmID, err := h.client.OpenDM(c.Context(), access.AccessToken, lookup.User.ID); err == nil {
				integration.DMChannelID = &dmID
			}
		}
	}

	if err := h.repo.Upsert(c.Context(), integration); err != nil {
		slog.Error("slack oauth: repo upsert failed", "user_id", userID, "error", err)
		return c.Redirect(h.frontendURL("error", "persist_failed"))
	}

	if integration.MatchedEmail == nil {
		// Installation succeeded but the user needs to provide a
		// workspace email we can resolve — SPA shows the resolve-email
		// form on return.
		return c.Redirect(h.frontendURL("needs_email", ""))
	}
	return c.Redirect(h.frontendURL("connected", ""))
}

// ResolveEmail handles the SPA "I use a different email on Slack"
// flow. Reads the integration for this user, tries lookupByEmail with
// the submitted value, caches DM channel on success.
func (h *SlackIntegrationHandler) ResolveEmail(c *fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || strings.TrimSpace(body.Email) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Email is required"})
	}
	altEmail := strings.TrimSpace(body.Email)

	integ, err := h.repo.GetActiveForUser(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load integration"})
	}
	if integ == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Slack integration not found — please reconnect"})
	}

	plaintext, err := h.cipher.Decrypt(integ.BotTokenEncrypted)
	if err != nil {
		// Encryption key mismatch or tampered row — safest path is to
		// force a reconnect. Revoking keeps /status honest.
		_ = h.repo.Revoke(c.Context(), integ.ID)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Integration token invalid — please reconnect"})
	}

	lookup, lookupErr := h.client.LookupUserByEmail(c.Context(), plaintext, altEmail)
	if errors.Is(lookupErr, slackoauth.ErrSlackUserNotFound) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Bu email workspace'inizde bulunamadı",
		})
	}
	if lookupErr != nil || lookup == nil || lookup.User.ID == "" {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "Slack lookup failed, try again"})
	}

	dmChannel, err := h.client.OpenDM(c.Context(), plaintext, lookup.User.ID)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "Could not open DM channel"})
	}
	if err := h.repo.UpdateDMChannel(c.Context(), integ.ID, dmChannel, altEmail); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to persist"})
	}
	return c.JSON(fiber.Map{"ok": true, "matched_email": altEmail})
}

// Disconnect revokes the active integration. We skip Slack's
// auth.revoke — not strictly required and would need a retry
// surface in case of 429. The user can uninstall the app from their
// workspace if they want the bot user removed; our DB-level revoke
// stops further deliveries regardless.
func (h *SlackIntegrationHandler) Disconnect(c *fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	integ, err := h.repo.GetActiveForUser(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load integration"})
	}
	if integ == nil {
		return c.JSON(fiber.Map{"ok": true, "message": "No active integration"})
	}
	if err := h.repo.Revoke(c.Context(), integ.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to revoke"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// Status feeds the SPA's /settings/notifications Slack card.
func (h *SlackIntegrationHandler) Status(c *fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	integ, err := h.repo.GetActiveForUser(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load integration"})
	}
	if integ == nil {
		return c.JSON(fiber.Map{"connected": false})
	}
	resp := fiber.Map{
		"connected":     true,
		"team_name":     integ.TeamName,
		"installed_at":  integ.InstalledAt,
		"matched_email": integ.MatchedEmail,
		"pending_email": integ.MatchedEmail == nil,
	}
	return c.JSON(resp)
}

// ==================== helpers ====================

func (h *SlackIntegrationHandler) userID(c *fiber.Ctx) (uuid.UUID, error) {
	raw, ok := c.Locals("userID").(string)
	if !ok {
		return uuid.Nil, c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user id"})
	}
	return id, nil
}

// frontendURL returns "<frontend>?slack=<status>[&error=<detail>]".
// We include errors as separate query params so the SPA can render
// a distinct toast per case rather than parsing a generic "error" blob.
func (h *SlackIntegrationHandler) frontendURL(status, errDetail string) string {
	u, err := url.Parse(h.frontendCallback)
	if err != nil {
		// Shouldn't happen — validated at construction — but fall
		// back to the raw string rather than panicking mid-callback.
		return h.frontendCallback
	}
	q := u.Query()
	q.Set("slack", status)
	if errDetail != "" {
		q.Set("error", errDetail)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ProfileEmailsFromContext is a small helper that wraps a raw lookup
// function into the ProfileEmailLookup interface — callers can
// implement this without needing to build a full service struct.
type ProfileEmailsFromContext func(ctx context.Context, userID uuid.UUID) (string, error)

// EmailForUser adapts the function to the interface. We provide a
// context.Background() fallback — the Slack callback path is short
// enough that a background context is fine.
func (f ProfileEmailsFromContext) EmailForUser(userID uuid.UUID) (string, error) {
	return f(context.Background(), userID)
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
