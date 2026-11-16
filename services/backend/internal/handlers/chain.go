package handlers

import (
	"errors"
	"strconv"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ChainHandler struct {
	assignmentSvc *services.ChainAssignmentService
	usageRecorder *services.UsageRecorder
	chainRepo     repository.ChainRepository
}

func NewChainHandler() *ChainHandler {
	return &ChainHandler{
		assignmentSvc: services.NewChainAssignmentService(),
		usageRecorder: services.NewUsageRecorder(),
		chainRepo:     repository.NewChainRepository(),
	}
}

// updateMeetingChainBody is the wire shape for PATCH /api/meetings/:id/chain.
//
// Three operations on a single endpoint, picked by inspecting which fields
// are present:
//
//	{action:"retry_auto_detect"} → clears manual lock, keeps chain_id
//	{chain_id:"<uuid>"}          → manual move (writes manual_user lock)
//	{chain_id:null}              → manual remove (writes manual_removed lock)
//	{}                           → manual remove (legacy shape, same as null)
//
// Action takes precedence when both fields are sent so a typo can't
// silently fall through to a destructive default. ChainID is only read
// when no action is supplied.
type updateMeetingChainBody struct {
	Action  string  `json:"action,omitempty"`
	ChainID *string `json:"chain_id"`
}

// UpdateMeetingChain handles PATCH /api/meetings/:id/chain.
//
// Phase 3 rewrite: every code path now goes through ChainAssignmentService
// so provenance (chain_assignment_method + locked_at) is written
// consistently and the cascade to decision_items happens inside the
// service rather than in the handler. The endpoint stays
// backwards-compatible — clients sending `{chain_id:"..."}` still get
// the same outcome they always did, plus the manual_user lock the
// roadmap mandates.
func (h *ChainHandler) UpdateMeetingChain(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	meetingID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	var body updateMeetingChainBody
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	switch body.Action {
	case "retry_auto_detect":
		if err := h.assignmentSvc.RetryAutoDetect(c.Context(), userID, meetingID); err != nil {
			return mapAssignmentError(c, err)
		}
		h.usageRecorder.Record(userID, nil, &meetingID, "chain_override", "system", "retry_auto_detect", 0, 0, 0, nil)
		return c.JSON(fiber.Map{"message": "Auto-detect lock cleared"})
	case "":
		// fall through to chain_id semantics
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Unknown action",
			"code":  "INVALID_ACTION",
		})
	}

	// Manual move / remove path.
	if body.ChainID == nil || *body.ChainID == "" {
		if err := h.assignmentSvc.RemoveFromChain(c.Context(), userID, meetingID); err != nil {
			return mapAssignmentError(c, err)
		}
		h.usageRecorder.Record(userID, nil, &meetingID, "chain_override", "system", "manual_remove", 0, 0, 0, nil)
		return c.JSON(fiber.Map{"message": "Meeting removed from chain"})
	}

	chainID, err := uuid.Parse(*body.ChainID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid chain ID"})
	}
	if err := h.assignmentSvc.SetManualChain(c.Context(), userID, meetingID, chainID); err != nil {
		return mapAssignmentError(c, err)
	}
	h.usageRecorder.Record(userID, nil, &meetingID, "chain_override", "system", "manual_assign", 0, 0, 0, nil)
	return c.JSON(fiber.Map{
		"message":  "Meeting chain updated",
		"chain_id": chainID,
	})
}

// ListChains handles GET /api/chains.
//
// Returns the user's chains ordered by most-recent meeting first. Backs
// the move-to-chain picker; the chain that owns the meeting being moved
// is filtered client-side (the handler stays generic so the same
// endpoint can power other pickers — chain inbox sidebar, brief target
// selector — without per-caller arguments).
func (h *ChainHandler) ListChains(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	limit := 100
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	chains, err := h.chainRepo.ListUserChains(c.Context(), userID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":  "Failed to list chains",
			"detail": err.Error(),
		})
	}

	out := make([]fiber.Map, 0, len(chains))
	for _, ch := range chains {
		out = append(out, fiber.Map{
			"id":              ch.ID,
			"display_name":    ch.DisplayName,
			"last_meeting_at": ch.LastMeetingAt,
			"meeting_count":   ch.MeetingCount,
		})
	}
	return c.JSON(fiber.Map{"chains": out})
}

// ListChainSuggestions handles GET /api/chain-suggestions.
//
// Returns the user's pending chain-assignment suggestions, most recent
// first. Empty array is the normal state until Phase 4 starts writing
// `mode='suggestion' status='pending'` rows from the metadata LLM
// fallback band.
func (h *ChainHandler) ListChainSuggestions(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	limit := 20
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	out, err := h.assignmentSvc.ListPendingSuggestions(c.Context(), userID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":  "Failed to load chain suggestions",
			"detail": err.Error(),
		})
	}
	if out == nil {
		// Ensure JSON serialises an empty list as `[]`, never `null`.
		out = []services.PendingSuggestion{}
	}
	return c.JSON(fiber.Map{"suggestions": out})
}

// AcceptChainSuggestion handles POST /api/chain-suggestions/:id/accept.
func (h *ChainHandler) AcceptChainSuggestion(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	decisionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid suggestion ID"})
	}
	if err := h.assignmentSvc.AcceptSuggestion(c.Context(), userID, decisionID); err != nil {
		return mapAssignmentError(c, err)
	}
	h.usageRecorder.Record(userID, nil, nil, "chain_suggestion", "system", "accept", 0, 0, 0, nil)
	return c.JSON(fiber.Map{"message": "Suggestion accepted"})
}

// DeclineChainSuggestion handles POST /api/chain-suggestions/:id/decline.
func (h *ChainHandler) DeclineChainSuggestion(c *fiber.Ctx) error {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	decisionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid suggestion ID"})
	}
	if err := h.assignmentSvc.DeclineSuggestion(c.Context(), userID, decisionID); err != nil {
		return mapAssignmentError(c, err)
	}
	h.usageRecorder.Record(userID, nil, nil, "chain_suggestion", "system", "decline", 0, 0, 0, nil)
	return c.JSON(fiber.Map{"message": "Suggestion declined"})
}

// mapAssignmentError centralises the service-error → HTTP-status mapping
// so all four endpoints stay consistent. Sentinel errors map to 4xx;
// everything else collapses to 500 with the underlying error in `detail`
// (caller-visible because these are user-initiated actions and a clear
// message helps debugging).
func mapAssignmentError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, services.ErrChainNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Chain not found"})
	case errors.Is(err, services.ErrChainAcrossUsers):
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "Chain belongs to another user"})
	case errors.Is(err, services.ErrMeetingNotOwned):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Meeting not found"})
	case errors.Is(err, services.ErrSuggestionNotPending):
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "Suggestion already resolved"})
	case errors.Is(err, services.ErrSuggestionMissingChain):
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "Suggestion has no candidate chain"})
	case errors.Is(err, repository.ErrChainDecisionNotFound):
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Suggestion not found"})
	default:
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":  "Operation failed",
			"detail": err.Error(),
		})
	}
}
