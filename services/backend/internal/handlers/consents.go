package handlers

import (
	"errors"
	"log/slog"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ConsentHandler exposes the consent management API used by:
//   - the post-OAuth flow (Google signup records consents here)
//   - the version-bump re-prompt UI
//   - the Settings page marketing toggle + audit list
//
// All routes require AuthMiddleware (registered under the protected
// group). The userID is read from c.Locals("userID").
type ConsentHandler struct {
	svc *services.ConsentService
}

func NewConsentHandler() *ConsentHandler {
	return &ConsentHandler{
		svc: services.NewConsentService(),
	}
}

func consentUserID(c *fiber.Ctx) (uuid.UUID, error) {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return uuid.Nil, errors.New("userID missing from context")
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

// RecordConsents handles POST /api/consents.
// Body: { "consents": [{ "type": "...", "version": "...", "granted": bool }, ...] }
//
// Records each provided consent for the authenticated user. Does NOT
// enforce that all mandatory types are present — frontends should
// follow up with GET /api/consents/status to verify satisfaction.
func (h *ConsentHandler) RecordConsents(c *fiber.Ctx) error {
	userID, err := consentUserID(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var body struct {
		Consents []models.SignupConsentInput `json:"consents"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek gövdesi.",
			"code":  "invalid_body",
		})
	}
	if len(body.Consents) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "En az bir onay gönderilmelidir.",
			"code":  "empty_consents",
		})
	}

	ipAddr := stringPtrIfNotEmpty(c.IP())
	userAgent := stringPtrIfNotEmpty(c.Get("User-Agent"))

	consents, err := h.svc.RecordConsents(c.Context(), userID, body.Consents, ipAddr, userAgent)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidConsentType):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error":  "Geçersiz onay tipi.",
				"detail": err.Error(),
				"code":   "invalid_consent_type",
			})
		case errors.Is(err, services.ErrConsentVersionMismatch):
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error":  "Onay belgesinin sürümü güncel değil. Sayfayı yenileyip tekrar deneyin.",
				"detail": err.Error(),
				"code":   "consent_version_mismatch",
			})
		}
		slog.Error("failed to record consents", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Onay kayıtları oluşturulamadı.",
			"code":  "consent_persist_failed",
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"consents": consents,
	})
}

// ListConsents handles GET /api/consents.
// Returns every consent row for the authenticated user, newest first.
// Powers the Settings page audit list.
func (h *ConsentHandler) ListConsents(c *fiber.Ctx) error {
	userID, err := consentUserID(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	consents, err := h.svc.ListUserConsents(c.Context(), userID)
	if err != nil {
		slog.Error("failed to list consents", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Onaylar listelenemedi.",
		})
	}

	return c.JSON(fiber.Map{
		"consents": consents,
	})
}

// GetStatus handles GET /api/consents/status.
// Returns { satisfied, missing, outdated, revoked } — used by the
// frontend to decide whether to redirect to the consents gate page.
func (h *ConsentHandler) GetStatus(c *fiber.Ctx) error {
	userID, err := consentUserID(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	status, err := h.svc.GetConsentStatus(c.Context(), userID)
	if err != nil {
		slog.Error("failed to compute consent status", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Onay durumu hesaplanamadı.",
		})
	}

	return c.JSON(status)
}

// RevokeConsent handles POST /api/consents/revoke.
// Body: { "type": "marketing", "reason": "optional" }
// Mandatory types cannot be revoked — frontend should route those
// users to account deletion instead.
func (h *ConsentHandler) RevokeConsent(c *fiber.Ctx) error {
	userID, err := consentUserID(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var body struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if err := c.BodyParser(&body); err != nil || body.Type == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek gövdesi. 'type' alanı gereklidir.",
			"code":  "invalid_body",
		})
	}

	consent, err := h.svc.RevokeConsent(c.Context(), userID, body.Type, body.Reason)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidConsentType):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Geçersiz onay tipi.",
				"code":  "invalid_consent_type",
			})
		case errors.Is(err, services.ErrCannotRevokeMandatory):
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Bu onay zorunludur ve geri çekilemez. Onay vermeyi sonlandırmak için hesabınızı silmeniz gerekir.",
				"code":  "consent_mandatory",
			})
		case errors.Is(err, repository.ErrConsentNotFound):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "Geri çekilecek aktif bir onay bulunamadı.",
				"code":  "consent_not_found",
			})
		}
		slog.Error("failed to revoke consent", "user_id", userID, "type", body.Type, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Onay geri çekilemedi.",
		})
	}

	return c.JSON(fiber.Map{
		"consent": consent,
	})
}
