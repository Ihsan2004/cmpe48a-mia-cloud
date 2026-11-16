package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os"
	"strings"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// AdminUserQuotaHandler exposes ops tooling for the issue #39 soft cap:
// view a user's full quota state (dollar fields included) and mutate
// tier / override / notes. Gated by ADMIN_API_KEY bearer auth — same
// pattern as AdminBriefDeliveryHandler, no JWT.
//
// Mutations write a user_quota_events audit row atomically with the
// UPDATE; the repo handles that in one tx. The handler is intentionally
// thin — validation + auth + JSON shaping only.
type AdminUserQuotaHandler struct {
	repo     repository.UserQuotaRepository
	svc      quota.Service
	adminKey string
}

func NewAdminUserQuotaHandler(repo repository.UserQuotaRepository, svc quota.Service, adminKey string) *AdminUserQuotaHandler {
	return &AdminUserQuotaHandler{repo: repo, svc: svc, adminKey: adminKey}
}

// NewAdminUserQuotaHandlerFromEnv returns nil when ADMIN_API_KEY is not
// set so routes.Setup can branch off a single nil check (mirrors the
// brief-delivery handler).
func NewAdminUserQuotaHandlerFromEnv(repo repository.UserQuotaRepository, svc quota.Service) *AdminUserQuotaHandler {
	key := strings.TrimSpace(os.Getenv("ADMIN_API_KEY"))
	if key == "" {
		return nil
	}
	return NewAdminUserQuotaHandler(repo, svc, key)
}

// adminQuotaPatch decodes the PATCH body. Tier / Notes / Reason use
// pointer fields with standard PATCH semantics (omitted → no change).
//
// monthly_usd_cap_override is tri-state — caller may "leave alone",
// "set to a value", or "drop entirely" — and JSON pointer can't model
// "explicit null" without `json.RawMessage`. We accept BOTH idioms:
//
//	{ "monthly_usd_cap_override": null }    // explicit clear (REST-idiomatic)
//	{ "clear_override": true }              // boolean flag (for clients that
//	                                        // can't send JSON null)
//	{ "monthly_usd_cap_override": 50.0 }    // set to value
//	(field absent)                          // no change
type adminQuotaPatch struct {
	Tier        *string         `json:"tier"`
	OverrideRaw json.RawMessage `json:"monthly_usd_cap_override"`
	Notes       *string         `json:"notes"`
	Reason      *string         `json:"reason"`
	// ClearOverride is an alternative to JSON null for clients that
	// can't easily emit it (some YAML→JSON pipes, query-string PATCH
	// builders, etc.). When true, override is set to NULL.
	ClearOverride bool `json:"clear_override"`
}

// decodeOverride returns (clear, value, ok). Three outcomes:
//
//	clear=false, value=nil, ok=true  → field absent OR ClearOverride=true with no field
//	clear=true,  value=nil, ok=true  → explicit clear (null or clear_override:true)
//	clear=false, value=*float64,ok=true → set to value
//	ok=false                         → invalid JSON or negative number
//
// The caller maps these to repository.AdminQuotaUpdate.
func decodeOverride(raw json.RawMessage, clearFlag bool) (clear bool, value *float64, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		// Field absent. clear_override:true on its own still triggers
		// the clear path; otherwise no change.
		return clearFlag, nil, true
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return true, nil, true
	}
	var v float64
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return false, nil, false
	}
	if v < 0 {
		return false, nil, false
	}
	return false, &v, true
}

// Get handles GET /api/admin/users/:id/quota.
//
// Returns the FULL dollar view (cap_usd, used_usd are exposed) so ops
// can size new overrides. The user-facing /api/usage/me stays
// dollar-free; that distinction is the whole point of having a
// separate admin endpoint.
func (h *AdminUserQuotaHandler) Get(c *fiber.Ctx) error {
	if !h.checkAdminAuth(c) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	userID, ok := h.parseUserID(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user id"})
	}

	snap, err := h.repo.GetSnapshot(c.Context(), userID, time.Now())
	if err != nil {
		if errors.Is(err, repository.ErrUserQuotaNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user quota row not found"})
		}
		slog.Error("admin quota: snapshot read failed", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Snapshot read failed"})
	}

	return c.JSON(h.buildAdminView(snap))
}

// Patch handles PATCH /api/admin/users/:id/quota. Atomic UPDATE + audit
// event in the same DB tx. Returns the post-mutation snapshot so the
// admin UI doesn't need a follow-up GET.
func (h *AdminUserQuotaHandler) Patch(c *fiber.Ctx) error {
	if !h.checkAdminAuth(c) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}

	userID, ok := h.parseUserID(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user id"})
	}

	var body adminQuotaPatch
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid JSON body"})
	}

	if body.Tier != nil {
		// Postel: accept any case, normalize to the canonical lowercase
		// form before validation / persistence. The registry keys are
		// all lowercase so this is also the single source of truth.
		normalized := strings.ToLower(strings.TrimSpace(*body.Tier))
		canonical, ok := validTier(normalized)
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "invalid tier",
				"hint":  "must be one of: free, poc, internal",
			})
		}
		body.Tier = &canonical
	}
	clearOverride, overrideValue, overrideOK := decodeOverride(body.OverrideRaw, body.ClearOverride)
	if !overrideOK {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "monthly_usd_cap_override must be null or a number >= 0",
		})
	}

	update := repository.AdminQuotaUpdate{
		Tier:          body.Tier,
		Override:      overrideValue,
		ClearOverride: clearOverride,
		Notes:         body.Notes,
		Actor:         "admin:" + adminKeyLabel(h.adminKey),
	}
	if body.Reason != nil {
		update.Reason = strings.TrimSpace(*body.Reason)
	}

	snap, err := h.repo.ApplyAdminUpdate(c.Context(), userID, update)
	if err != nil {
		if errors.Is(err, repository.ErrUserQuotaNotFound) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "user quota row not found"})
		}
		slog.Error("admin quota: patch failed", "user_id", userID, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Patch failed"})
	}

	slog.Info("admin quota: patched",
		"user_id", userID,
		"actor", update.Actor,
		"tier", snap.Tier,
		"override", snap.MonthlyUSDCapOverride)

	return c.JSON(h.buildAdminView(snap))
}

func (h *AdminUserQuotaHandler) buildAdminView(snap repository.UserQuotaSnapshot) fiber.Map {
	cap := quota.ResolveCap(snap.Tier, snap.MonthlyUSDCapOverride)

	resp := fiber.Map{
		"user_id":                  snap.UserID.String(),
		"tier":                     snap.Tier,
		"monthly_usd_cap_override": snap.MonthlyUSDCapOverride,
		"used_usd":                 snap.UsedUSD,
		"cycle_anchor_day":         snap.CycleAnchorDay,
		"notes":                    snap.Notes,
	}

	if cap != nil {
		resp["cap_usd"] = *cap
		// Mirror the service's used_pct math so the admin UI matches
		// what the user-facing /api/usage/me + middleware report. Both
		// must use math.Ceil — a user at $1.99/$2.00 (99.5%) should
		// show 100% to surface "you're at the cap" consistently.
		used := snap.UsedUSD
		pct := 0
		if *cap > 0 {
			pct = int(math.Ceil((used / *cap) * 100))
			if pct > 100 {
				pct = 100
			}
			if pct < 0 {
				pct = 0
			}
		} else {
			pct = 100
		}
		resp["used_pct"] = pct
	} else {
		resp["cap_usd"] = nil
		resp["used_pct"] = 0
	}

	if !snap.CycleStartAt.IsZero() {
		resp["cycle_starts_at"] = snap.CycleStartAt.UTC().Format(time.RFC3339)
	}
	if !snap.CycleEndAt.IsZero() {
		resp["cycle_resets_at"] = snap.CycleEndAt.UTC().Format(time.RFC3339)
	}
	return resp
}

func (h *AdminUserQuotaHandler) parseUserID(c *fiber.Ctx) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.Params("id")))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (h *AdminUserQuotaHandler) checkAdminAuth(c *fiber.Ctx) bool {
	if h.adminKey == "" {
		return false
	}
	got := strings.TrimSpace(c.Get("Authorization"))
	got = strings.TrimPrefix(got, "Bearer ")
	got = strings.TrimSpace(got)
	return constantTimeEquals(got, h.adminKey)
}

// validTier echoes the plan registry's allowlist. Kept local so this
// file doesn't reach into quota internals beyond ResolveCap.
func validTier(t string) (string, bool) {
	switch t {
	case quota.TierFree, quota.TierPOC, quota.TierInternal:
		return t, true
	}
	return "", false
}

// adminKeyLabel returns a short SHA-256 fingerprint of the admin key so
// the audit trail (user_quota_events.actor) and slog lines can
// distinguish keys without leaking any of the key itself. 6 hex chars
// = 24 bits of entropy — collision-resistant enough for the handful of
// keys an org will ever rotate through, while keeping log lines short.
//
// Empty key → "anon" so the label is grep-friendly even when the
// handler is misconfigured (shouldn't happen — NewAdminUserQuotaHandlerFromEnv
// returns nil for empty keys — but guards against future misuse).
func adminKeyLabel(key string) string {
	if key == "" {
		return "anon"
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:6]
}
