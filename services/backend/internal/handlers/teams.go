package handlers

import (
	"context"
	"errors"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/services/teamdetection"
	"mia-website/backend/internal/services/teamssave"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// TeamsHandler owns the 5 Phase 7 endpoints. Construct once in
// routes.Setup with NewTeamsHandler — it is stateless.
//
// Endpoint contract (CONTEXT.md D-24):
//
//	POST   /api/teams/detect          → Detect      (runs Phase 6 Service.Detect under per-user advisory lock)
//	POST   /api/teams/save            → Save        (atomic insert + status flip; 409 on duplicate)
//	POST   /api/teams/skip            → Skip        (status flip only; body {empty_state: bool})
//	GET    /api/teams                 → List        (read-only; returns saved teams + members)
//	DELETE /api/teams/members/:id     → DeleteMember (KVKK right-to-erasure)
type TeamsHandler struct {
	repo      repository.TeamRepository
	saveSvc   *teamssave.Service
	detectSvc teamdetection.Detector
}

// NewTeamsHandler is the production constructor. detectSvc takes the
// teamdetection.Detector interface (not the concrete *Service) so tests
// can substitute a fake.
func NewTeamsHandler(repo repository.TeamRepository, detectSvc teamdetection.Detector) *TeamsHandler {
	return &TeamsHandler{
		repo:      repo,
		saveSvc:   teamssave.New(repo),
		detectSvc: detectSvc,
	}
}

// teamsUserIDFromCtx extracts the JWT-derived userID. Mirrors note.go:21-26.
func teamsUserIDFromCtx(c *fiber.Ctx) (uuid.UUID, error) {
	s, ok := c.Locals("userID").(string)
	if !ok {
		return uuid.Nil, errors.New("userID not in locals")
	}
	return uuid.Parse(s)
}

// Detect — POST /api/teams/detect.
//
// Per-user advisory lock prevents concurrent same-user detects (D-26).
// Returns the Phase 6 DetectionResult JSON verbatim (no wrapping per
// RESEARCH §"Endpoint Specifications 1").
func (h *TeamsHandler) Detect(c *fiber.Ctx) error {
	userID, err := teamsUserIDFromCtx(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userEmail, _ := c.Locals("userEmail").(string) // optional; teamdetection tolerates ""

	var result *teamdetection.DetectionResult
	lockErr := h.repo.WithDetectLock(c.Context(), userID, func(ctx context.Context) error {
		var dErr error
		result, dErr = h.detectSvc.Detect(ctx, userID, userEmail)
		return dErr
	})

	if errors.Is(lockErr, repository.ErrDetectAlreadyRunning) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": "Tarama zaten devam ediyor",
			"code":  "DETECT_RUNNING",
		})
	}

	// Translate Phase 5 BackfillError class → HTTP code. Class values are
	// the ErrorClass constants from services/calendar_backfill.go:
	//   oauth_expired    → 401 GOOGLE_AUTH_EXPIRED  (FETCH-04)
	//   quota_exhausted  → 503 FETCH_TRANSIENT      (retryable from caller's POV)
	//   network          → 503 FETCH_TRANSIENT
	//   server_5xx       → 503 FETCH_TRANSIENT
	//   permanent        → 502 FETCH_PERMANENT
	var be *services.BackfillError
	if errors.As(lockErr, &be) {
		switch be.Class {
		case services.ClassOAuthExpired:
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"code":  "GOOGLE_AUTH_EXPIRED",
				"error": "Google bağlantısı yenilenmeli",
			})
		case services.ClassQuotaExhausted, services.ClassNetwork, services.ClassServer5xx:
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"code":  "FETCH_TRANSIENT",
				"error": "Takvim alınamadı, yeniden dene",
			})
		case services.ClassPermanent:
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
				"code":  "FETCH_PERMANENT",
				"error": "Takvim alınamadı",
			})
		}
	}

	if lockErr != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Tarama başarısız oldu"})
	}
	return c.JSON(result)
}

// Save — POST /api/teams/save.
func (h *TeamsHandler) Save(c *fiber.Ctx) error {
	userID, err := teamsUserIDFromCtx(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	var body teamssave.SaveRequest
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Geçersiz istek gövdesi"})
	}

	resp, err := h.saveSvc.Save(c.Context(), userID, body)
	if err != nil {
		var ve *teamssave.ValidationError
		if errors.As(err, &ve) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": ve.Message,
				"code":  ve.Code,
				"field": ve.Field,
			})
		}
		if errors.Is(err, repository.ErrAlreadyCompleted) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "Başka sekmede zaten kaydedildi",
				"code":  "ALREADY_COMPLETED",
			})
		}
		if repository.IsUniqueViolation(err, "teams_user_id_name_unique") ||
			repository.IsUniqueViolation(err, "team_members_team_id_email_unique") {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "Yinelenen takım veya üye kaydı",
				"code":  "DUPLICATE",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Kaydetme başarısız"})
	}
	return c.JSON(resp)
}

// Skip — POST /api/teams/skip.
//
// body: { "empty_state": bool } — true ⇒ empty_state_skipped; false ⇒ skipped.
func (h *TeamsHandler) Skip(c *fiber.Ctx) error {
	userID, err := teamsUserIDFromCtx(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	var body struct {
		EmptyState bool `json:"empty_state"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Geçersiz istek gövdesi"})
	}

	newStatus := "skipped"
	if body.EmptyState {
		newStatus = "empty_state_skipped"
	}

	if err := h.repo.UpdateOnboardingStatus(c.Context(), userID, newStatus); err != nil {
		if errors.Is(err, repository.ErrAlreadyCompleted) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": "Onboarding zaten tamamlanmış",
				"code":  "ALREADY_COMPLETED",
			})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Atlama başarısız"})
	}
	return c.JSON(fiber.Map{"status": newStatus})
}

// List — GET /api/teams.
func (h *TeamsHandler) List(c *fiber.Ctx) error {
	userID, err := teamsUserIDFromCtx(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	teams, err := h.repo.ListTeamsByUserWithMembers(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Listeleme başarısız"})
	}
	// Defensive: ensure JSON-encoded shape is [] not null when zero teams.
	if teams == nil {
		teams = []repository.TeamWithMembers{}
	}
	return c.JSON(fiber.Map{"teams": teams})
}

// DeleteMember — DELETE /api/teams/members/:id.
//
// IDOR mitigation: both "not found" and "not owned by user" collapse to
// 404 (RESEARCH Threat T-07-02 / T-07-12).
func (h *TeamsHandler) DeleteMember(c *fiber.Ctx) error {
	userID, err := teamsUserIDFromCtx(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	memberID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Geçersiz üye kimliği"})
	}
	deleted, err := h.repo.DeleteMemberScoped(c.Context(), userID, memberID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Silme başarısız"})
	}
	if !deleted {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Üye bulunamadı"})
	}
	return c.SendStatus(fiber.StatusNoContent)
}
