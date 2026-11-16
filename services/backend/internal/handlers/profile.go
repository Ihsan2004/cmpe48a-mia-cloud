package handlers

import (
	"mia-website/backend/internal/models"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ProfileHandler struct {
	profileService *services.ProfileService
	// Phase 2.4 B7: optional desktop hub for `invalidate` push when the
	// profile-level recording_method changes (every meeting that
	// resolves through this profile shifts its effective_method).
	desktopHub *services.DesktopHub
}

func NewProfileHandler() *ProfileHandler {
	return &ProfileHandler{
		profileService: services.NewProfileService(),
	}
}

// SetDesktopHub injects the WS hub used to publish `invalidate`
// messages after a recording_method change. Phase 2.4 B7.
func (h *ProfileHandler) SetDesktopHub(hub *services.DesktopHub) {
	h.desktopHub = hub
}

// buildProfileResponse assembles the JSON shape returned from GET /api/profile.
// Extracted from GetProfile so the response shape is unit-testable
// (handlers package, profile_test.go). v1.1 Phase 4 added teams_onboarding_status
// as a flat top-level field per CONTEXT.md D-12 (no nested wrapper).
func buildProfileResponse(profile *models.Profile) fiber.Map {
	// Check if calendar is connected (has valid refresh token)
	isConnected := profile.IsCalendarConnected
	if !isConnected && profile.GoogleRefreshToken != nil && *profile.GoogleRefreshToken != "" {
		isConnected = true
	}

	// Phase 5 fix 2026-04-20 — recording_method needs to make it to the
	// client or the /settings/profile page can't render (let alone change)
	// the default. Default to "bot" when the DB column is NULL so legacy
	// profiles look the same as fresh ones.
	recordingMethod := profile.RecordingMethod
	if recordingMethod == "" {
		recordingMethod = models.RecordingMethodBot
	}

	// Defensive: column is NOT NULL with default 'pending' and the service
	// applies COALESCE, so this should be unreachable. But if a future
	// refactor accidentally drops the COALESCE, returning "" to the
	// frontend would route the user into onboarding incorrectly. Guard
	// with the same default as the migration (T-04-11).
	teamsOnboardingStatus := profile.TeamsOnboardingStatus
	if teamsOnboardingStatus == "" {
		teamsOnboardingStatus = models.TeamsOnboardingStatusPending
	}

	return fiber.Map{
		"id":                       profile.ID,
		"email":                    profile.Email,
		"full_name":                profile.FullName,
		"job_title":                profile.JobTitle,
		"company":                  profile.Company,
		"timezone":                 profile.Timezone,
		"default_meeting_duration": profile.DefaultMeetingDuration,
		"work_start_hour":          profile.WorkStartHour,
		"work_end_hour":            profile.WorkEndHour,
		"bot_name":                 profile.BotName,
		"bot_language":             profile.BotLanguage,
		"is_calendar_connected":    isConnected,
		"is_google_connected":      isConnected, // Keep for backwards compatibility
		"google_email":             profile.GoogleEmail,
		"interface_language":       profile.InterfaceLanguage,
		"brief_lead_time_minutes":  profile.BriefLeadTimeMinutes,
		"recording_method":         recordingMethod,
		// Brief delivery (Phase 6): per-channel toggles + unsubscribe
		// state so the /settings/notifications page can render without
		// a second GET. Default false honours roadmap Karar #12 (opt-in).
		"brief_deliver_via_email": profile.BriefDeliverViaEmail,
		"brief_deliver_via_slack": profile.BriefDeliverViaSlack,
		"email_unsubscribed_at":   profile.EmailUnsubscribedAt,
		// v1.1 Phase 4 (ONBOARD-03): flat top-level onboarding state
		// surfaced for the frontend's post-OAuth router. Defaulted at
		// both the DB layer (COALESCE in service) and the handler
		// layer (above) so the wire response is always one of the four
		// allowed values, never empty.
		"teams_onboarding_status": teamsOnboardingStatus,
	}
}

// GetProfile handles GET /api/profile
func (h *ProfileHandler) GetProfile(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Oturum bilgisi bulunamadı. Lütfen tekrar giriş yapın.",
		})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz kullanıcı bilgisi.",
		})
	}

	profile, err := h.profileService.GetProfile(userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Kullanıcı profili bulunamadı.",
		})
	}

	return c.JSON(buildProfileResponse(profile))
}

// UpdateProfile handles PUT /api/profile
func (h *ProfileHandler) UpdateProfile(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Oturum bilgisi bulunamadı. Lütfen tekrar giriş yapın.",
		})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz kullanıcı bilgisi.",
		})
	}

	var body struct {
		FullName               string  `json:"full_name"`
		Email                  *string `json:"email,omitempty"` // read-only from UI; ignored on update
		JobTitle               *string `json:"job_title,omitempty"`
		Company                *string `json:"company,omitempty"`
		Timezone               *string `json:"timezone,omitempty"`
		DefaultMeetingDuration *int    `json:"default_meeting_duration,omitempty"`
		WorkStartHour          *string `json:"work_start_hour,omitempty"`
		WorkEndHour            *string `json:"work_end_hour,omitempty"`
		BotName                *string `json:"bot_name,omitempty"`
		BotLanguage            *string `json:"bot_language,omitempty"`
		InterfaceLanguage      *string `json:"interface_language,omitempty"`
		BriefLeadTimeMinutes   *int    `json:"brief_lead_time_minutes,omitempty"`
		RecordingMethod        *string `json:"recording_method,omitempty"` // batch pipeline: bot|desktop
	}

	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	if body.FullName == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Ad soyad alanı gereklidir.",
		})
	}

	// Defaults / fallbacks
	timezone := "UTC"
	if body.Timezone != nil && *body.Timezone != "" {
		timezone = *body.Timezone
	}
	defaultDuration := 30
	if body.DefaultMeetingDuration != nil && *body.DefaultMeetingDuration > 0 {
		defaultDuration = *body.DefaultMeetingDuration
	}
	botName := "MIA Assistant"
	if body.BotName != nil && *body.BotName != "" {
		botName = *body.BotName
	}
	botLanguage := "tr"
	if body.BotLanguage != nil && *body.BotLanguage != "" {
		botLanguage = *body.BotLanguage
	}
	interfaceLanguage := "tr"
	if body.InterfaceLanguage != nil && *body.InterfaceLanguage != "" {
		interfaceLanguage = *body.InterfaceLanguage
	}

	// recording_method: validated enum, default "bot" on first save.
	// When the client omits the field we pass empty — service layer
	// leaves the column untouched so repeat PUTs don't reset it.
	recordingMethod := ""
	if body.RecordingMethod != nil {
		candidate := *body.RecordingMethod
		if !models.IsValidRecordingMethod(candidate) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Kayıt yöntemi 'bot' veya 'desktop' olmalıdır.",
				"code":  "INVALID_RECORDING_METHOD",
			})
		}
		recordingMethod = candidate
	}

	// Capture the previous recording_method so we only fire the
	// `invalidate` push when it actually shifted. Avoids spurious
	// re-polls every time the user just touches their bot name or
	// timezone.
	var prevMethod string
	if existing, _ := h.profileService.GetProfile(userID); existing != nil {
		prevMethod = existing.RecordingMethod
	}

	if err := h.profileService.UpdateProfile(
		userID,
		body.FullName,
		body.JobTitle,
		body.Company,
		timezone,
		defaultDuration,
		body.WorkStartHour,
		body.WorkEndHour,
		botName,
		botLanguage,
		interfaceLanguage,
		body.BriefLeadTimeMinutes,
		recordingMethod,
	); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Profil güncellenirken bir hata oluştu.",
		})
	}

	// Phase 2.4 B7: only nudge the recorder when recording_method
	// actually changed — body.RecordingMethod is non-nil + differs
	// from the stored value.
	if body.RecordingMethod != nil && *body.RecordingMethod != "" &&
		*body.RecordingMethod != prevMethod {
		services.PublishCalendarInvalidate(h.desktopHub, userID)
	}

	return c.JSON(fiber.Map{
		"message": "Profile updated successfully",
	})
}

// UpdateNotificationPreferences handles PATCH /api/profile/notifications.
// Narrow-surface endpoint for the brief-delivery toggles so the
// /settings/notifications page can flip a single boolean without
// resending the whole profile form (no full_name revalidation, no
// timezone round-trip). Body fields are optional pointers — omitted
// fields leave the stored value untouched.
func (h *ProfileHandler) UpdateNotificationPreferences(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Oturum bilgisi bulunamadı. Lütfen tekrar giriş yapın.",
		})
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz kullanıcı bilgisi.",
		})
	}

	var body struct {
		BriefDeliverViaEmail *bool `json:"brief_deliver_via_email,omitempty"`
		BriefDeliverViaSlack *bool `json:"brief_deliver_via_slack,omitempty"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek gövdesi.",
		})
	}
	if body.BriefDeliverViaEmail == nil && body.BriefDeliverViaSlack == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "En az bir tercih güncellenmeli.",
		})
	}

	if err := h.profileService.UpdateNotificationPreferences(
		userID,
		body.BriefDeliverViaEmail,
		body.BriefDeliverViaSlack,
	); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Bildirim tercihleri güncellenirken bir hata oluştu.",
		})
	}
	return c.JSON(fiber.Map{"ok": true})
}

