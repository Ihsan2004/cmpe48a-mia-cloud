package handlers

import (
	"errors"
	"log/slog"
	"strings"

	"mia-website/backend/internal/services/briefnotify"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// UnsubscribeStore is the narrow persistence contract the handler
// needs. Declared here so unit tests can stub it; services.ProfileService
// already satisfies the shape so wiring in main.go stays a one-liner.
type UnsubscribeStore interface {
	SetEmailUnsubscribed(userID uuid.UUID) (changed bool, err error)
	ClearEmailUnsubscribe(userID uuid.UUID) error
}

// UnsubscribeHandler owns the public GET /api/unsubscribe endpoint and
// the authenticated POST /api/unsubscribe/restore companion. The signer
// MAY be nil — if UNSUBSCRIBE_SIGNING_SECRET wasn't configured, every
// request fails closed with 503 so the deploy owner notices the gap
// rather than silently accepting malformed tokens.
type UnsubscribeHandler struct {
	signer *briefnotify.UnsubscribeSigner
	store  UnsubscribeStore
}

func NewUnsubscribeHandler(signer *briefnotify.UnsubscribeSigner, store UnsubscribeStore) *UnsubscribeHandler {
	return &UnsubscribeHandler{signer: signer, store: store}
}

// Unsubscribe is the public handler (no JWT) invoked when a user clicks
// the footer link in a brief email. Returns HTML (not JSON) because the
// response goes straight to a browser. We render three outcomes:
//
//	200 — success OR idempotent re-click (no state change this time)
//	400 — token malformed or signature invalid
//	410 — token expired (>30 days since the mail arrived)
//	503 — signer not configured (ops misconfiguration)
//
// Turkish copy throughout; mail recipients see Turkish everywhere else.
func (h *UnsubscribeHandler) Unsubscribe(c *fiber.Ctx) error {
	if h.signer == nil {
		return h.renderError(c, fiber.StatusServiceUnavailable,
			"Abonelik iptali geçici olarak kullanılamıyor",
			"Sunucu yapılandırması eksik. Lütfen tekrar deneyin.")
	}

	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		return h.renderError(c, fiber.StatusBadRequest,
			"Bağlantı geçersiz",
			"İptal bağlantınızda token eksik. Lütfen mail içindeki bağlantıyı tekrar açın.")
	}

	userID, err := h.signer.ValidateToken(token)
	switch {
	case err == nil:
		// ok
	case errors.Is(err, briefnotify.ErrUnsubscribeTokenExpired):
		return h.renderError(c, fiber.StatusGone,
			"Bağlantının süresi dolmuş",
			"Bu iptal bağlantısı 30 günden eski. Dashboard üzerinden ayarlara girip mail bildirimlerini kapatabilirsiniz.")
	case errors.Is(err, briefnotify.ErrUnsubscribeTokenInvalid):
		return h.renderError(c, fiber.StatusBadRequest,
			"Bağlantı geçersiz",
			"Bu iptal bağlantısı doğrulanamadı. Lütfen mail içindeki orijinal bağlantıyı kullanın.")
	default:
		slog.Error("unsubscribe token validation failed", "error", err)
		return h.renderError(c, fiber.StatusInternalServerError,
			"Beklenmeyen bir hata oluştu",
			"Lütfen birazdan tekrar deneyin.")
	}

	changed, err := h.store.SetEmailUnsubscribed(userID)
	if err != nil {
		slog.Error("failed to persist unsubscribe", "user_id", userID, "error", err)
		return h.renderError(c, fiber.StatusInternalServerError,
			"Beklenmeyen bir hata oluştu",
			"Aboneliğiniz iptal edilemedi, lütfen birazdan tekrar deneyin.")
	}

	subtitle := "Artık toplantı öncesi brief mailleri almayacaksınız."
	if !changed {
		subtitle = "Zaten iptal edilmişti. Yine de mail almaya devam ediyorsanız lütfen destek ile iletişime geçin."
	}
	return h.renderSuccess(c, "Mail aboneliğiniz iptal edildi", subtitle)
}

// Restore is the authenticated companion — a JWT-bearing user can turn
// the toggle back on from the dashboard. Returns JSON because the caller
// is the SPA, not a browser link.
func (h *UnsubscribeHandler) Restore(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user id"})
	}
	if err := h.store.ClearEmailUnsubscribe(userID); err != nil {
		slog.Error("failed to clear unsubscribe", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to restore subscription"})
	}
	// Note: brief_deliver_via_email stays off — user must re-enable
	// via /settings/notifications (roadmap Karar #12: opt-in only).
	return c.JSON(fiber.Map{"ok": true, "message": "Abonelik geri alındı. Mailleri almak için ayarlardan toggle'ı açın."})
}

// ==================== HTML helpers ====================

// renderSuccess + renderError share the same skeleton so the page
// layout stays consistent across outcomes (same banner, same footer).
// Inline styles only — a one-page response doesn't need a CSS file.

func (h *UnsubscribeHandler) renderSuccess(c *fiber.Ctx, title, subtitle string) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.Status(fiber.StatusOK).SendString(unsubscribePage(title, subtitle, "#059669"))
}

func (h *UnsubscribeHandler) renderError(c *fiber.Ctx, status int, title, subtitle string) error {
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.Status(status).SendString(unsubscribePage(title, subtitle, "#dc2626"))
}

func unsubscribePage(title, subtitle, accent string) string {
	return `<!DOCTYPE html>
<html lang="tr"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>` + htmlEscape(title) + ` — Syncrun</title></head>
<body style="margin:0;background:#f4f5f7;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#1f2937;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;">
<div style="max-width:480px;width:100%;background:#ffffff;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,0.08);overflow:hidden;">
<div style="padding:24px 28px;background:` + accent + `;color:#ffffff;">
<div style="font-size:13px;letter-spacing:0.4px;text-transform:uppercase;opacity:0.85;">Syncrun</div>
<div style="font-size:22px;font-weight:600;margin-top:4px;">` + htmlEscape(title) + `</div>
</div>
<div style="padding:28px;font-size:15px;line-height:1.6;color:#374151;">` + htmlEscape(subtitle) + `</div>
</div></body></html>`
}

// htmlEscape is a minimal replacement for the template package's auto-
// escape — we build the page manually because the Turkish strings need
// to survive verbatim (they already contain no user-controlled input).
// Still, paranoid replacement of the five XML characters keeps us safe
// if a future caller slips a user-supplied value into title/subtitle.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
