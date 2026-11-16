package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/apperror"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"google.golang.org/api/calendar/v3"
)

type MeetingHandler struct {
	meetingService  *services.MeetingService
	calendarService *services.CalendarService
	profileService  *services.ProfileService
	transcriptRepo  repository.TranscriptRepository
	// Phase 2.4 B7: optional desktop hub for `invalidate` push after a
	// recording-method override change. Nil-safe — when the WS subsystem
	// isn't wired the helper is a no-op.
	desktopHub *services.DesktopHub
}

func NewMeetingHandler() *MeetingHandler {
	return &MeetingHandler{
		meetingService:  services.NewMeetingService(),
		calendarService: services.NewCalendarService(),
		profileService:  services.NewProfileService(),
		transcriptRepo:  repository.NewTranscriptRepository(),
	}
}

// SetDesktopHub injects the WebSocket hub used to publish `invalidate`
// messages. Called from routes.Setup after the hub is constructed.
func (h *MeetingHandler) SetDesktopHub(hub *services.DesktopHub) {
	h.desktopHub = hub
}

func isGoogleNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "not found") || strings.Contains(lower, "404")
}

// GetMeetings handles GET /api/meetings
func (h *MeetingHandler) GetMeetings(c *fiber.Ctx) error {
	// Get user ID from context
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

	userProfile, err := h.profileService.GetProfile(userID)
	if err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendServiceError(c, err, "Kullanıcı profili yüklenemedi.", fiber.StatusInternalServerError, "fetch_user_profile", requestID)
	}
	userEmail := ""
	if userProfile.Email != nil {
		userEmail = *userProfile.Email
	}

	meetings, err := h.meetingService.GetMeetingsForUser(c.Context(), userID, userEmail)
	if err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Toplantılar yüklenemedi.", "fetch_meetings", requestID)
	}

	// Phase 5 fix 2026-04-20 — expose recording_method_override and the
	// resolved effective_method so the dashboard can render the per-meeting
	// override dropdown. Profile default is cached once per request to
	// keep this zero extra DB hits per row.
	profileDefault := userProfile.RecordingMethod
	if profileDefault == "" {
		profileDefault = models.RecordingMethodBot
	}

	result := []fiber.Map{}
	for _, m := range meetings {
		effective := profileDefault
		if m.RecordingMethodOverride != nil && *m.RecordingMethodOverride != "" {
			effective = *m.RecordingMethodOverride
		}
		meetingMap := fiber.Map{
			"id":                        m.ID,
			"user_id":                   m.UserID,
			"title":                     m.Title,
			"start_time":                m.StartTime,
			"end_time":                  m.EndTime,
			"meeting_url":               m.MeetingURL,
			"description":               m.Description,
			"location":                  m.Location,
			"attendees":                 m.Attendees,
			"organizer_email":           m.OrganizerEmail,
			"google_event_id":           m.GoogleEventID,
			"summary":                   m.Summary,
			"action_items":              m.ActionItems,
			"key_topics":                m.KeyTopics,
			"auto_bot_enabled":          m.AutoBotEnabled,
			"bot_status":                m.BotStatus,
			"bot_joined_at":             m.BotJoinedAt,
			"created_at":                m.CreatedAt,
			"recording_method_override": m.RecordingMethodOverride,
			"effective_method":          effective,
		}
		result = append(result, meetingMap)
	}

	return c.JSON(fiber.Map{
		"meetings": result,
	})
}

// GetMeetingDetail handles GET /api/meetings/:id
// Returns meeting details with structured action items and key topics (FIX 1.2)
func (h *MeetingHandler) GetMeetingDetail(c *fiber.Ctx) error {
	// Get user ID from context
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

	// Get meeting ID from params
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz toplantı bilgisi.",
		})
	}

	// Fetch meeting with structured data from repository
	meeting, err := h.meetingService.GetMeetingByID(userID, meetingID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Toplantı bulunamadı.",
		})
	}

	// Lookup chain type if meeting has a chain
	var chainType *string
	if meeting.ChainID != nil {
		var ct string
		err := repository.DB.QueryRow(c.Context(), `SELECT chain_type FROM meeting_chains WHERE id = $1`, *meeting.ChainID).Scan(&ct)
		if err == nil {
			chainType = &ct
		}
	}

	// Phase 5 fix 2026-04-20 — compute effective_method once for the detail
	// card so the override dropdown knows what to pre-select.
	profile, _ := h.profileService.GetProfile(userID)
	profileDefault := models.RecordingMethodBot
	if profile != nil && profile.RecordingMethod != "" {
		profileDefault = profile.RecordingMethod
	}
	effectiveMethod := profileDefault
	if meeting.RecordingMethodOverride != nil && *meeting.RecordingMethodOverride != "" {
		effectiveMethod = *meeting.RecordingMethodOverride
	}

	// Return meeting with all fields including structured action items and key topics
	return c.JSON(fiber.Map{
		"id":                        meeting.ID,
		"user_id":                   meeting.UserID,
		"title":                     meeting.Title,
		"start_time":                meeting.StartTime,
		"end_time":                  meeting.EndTime,
		"meeting_url":               meeting.MeetingURL,
		"description":               meeting.Description,
		"location":                  meeting.Location,
		"attendees":                 meeting.Attendees,
		"organizer_email":           meeting.OrganizerEmail,
		"google_event_id":           meeting.GoogleEventID,
		"summary":                   meeting.Summary,
		"executive_summary":         meeting.ExecutiveSummary,
		"action_items":              meeting.ActionItems,
		"action_items_structured":   meeting.ActionItemsStructured, // FIX 1.2: Structured with metadata
		"key_topics":                meeting.KeyTopics,
		"key_topics_structured":     meeting.KeyTopicsStructured, // Structured with stable topic order
		"auto_bot_enabled":          meeting.AutoBotEnabled,
		"bot_status":                meeting.BotStatus,
		"bot_joined_at":             meeting.BotJoinedAt,
		"last_heartbeat_at":         meeting.LastHeartbeatAt,
		"created_at":                meeting.CreatedAt,
		"agenda_items":              meeting.AgendaItems,
		"expected_outcomes":         meeting.ExpectedOutcomes,
		"chain_id":                  meeting.ChainID,
		"chain_type":                chainType,
		"chain_assignment_method":   meeting.ChainAssignmentMethod,
		"chain_assignment_score":    meeting.ChainAssignmentScore,
		"chain_assignment_locked_at": meeting.ChainAssignmentLockedAt,
		"calendar_series_id":        meeting.CalendarSeriesID,
		"recording_method_override": meeting.RecordingMethodOverride,
		"effective_method":          effectiveMethod,
		"transcription_status":      meeting.TranscriptionStatus,
		"transcription_last_error":  meeting.TranscriptionLastError,
	})
}

// CreateMeeting handles POST /api/meetings
func (h *MeetingHandler) CreateMeeting(c *fiber.Ctx) error {
	var req models.CreateMeetingRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	// Access token is now optional - we prefer refresh token from DB
	// But validate that we have either refresh token in DB or access token in request

	if req.Title == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Toplantı başlığı gereklidir.",
		})
	}

	if req.StartTime.IsZero() || req.EndTime.IsZero() {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Başlangıç ve bitiş zamanı gereklidir.",
		})
	}

	if req.EndTime.Before(req.StartTime) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Bitiş zamanı başlangıç zamanından sonra olmalıdır.",
		})
	}

	// Get user ID from context
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

	// Create event in Google Calendar
	// Use refresh token from DB (offline access) if available, otherwise fallback to access token
	googleEvent, err := h.calendarService.CreateEvent(
		&userID,
		req.AccessToken,
		req.Title,
		req.StartTime,
		req.EndTime,
		req.Description,
		req.Location,
		req.Attendees,
		req.CreateGoogleMeet,
	)

	// Handle calendar creation error
	if err != nil {
		// If this is an ad-hoc meeting (has meeting_url), we can proceed even if calendar sync fails
		if req.MeetingURL != nil && *req.MeetingURL != "" {
			slog.Warn("failed to sync ad-hoc meeting to calendar", "error", err, "request_id", c.Locals("requestID"))

			// Create a dummy google event to satisfy downstream logic
			googleEvent = &calendar.Event{
				Id:      fmt.Sprintf("local-%s", uuid.New().String()),
				Summary: req.Title,
				Start: &calendar.EventDateTime{
					DateTime: req.StartTime.Format(time.RFC3339),
				},
				End: &calendar.EventDateTime{
					DateTime: req.EndTime.Format(time.RFC3339),
				},
			}
			if req.Description != nil {
				googleEvent.Description = *req.Description
			}
			if req.Location != nil {
				googleEvent.Location = *req.Location
			}
			if req.Attendees != nil {
				for _, email := range req.Attendees {
					googleEvent.Attendees = append(googleEvent.Attendees, &calendar.EventAttendee{Email: email})
				}
			}
		} else {
			// For normal scheduled meetings, failure to create calendar event is a hard error
			requestID, _ := c.Locals("requestID").(string)
			return apperror.Send(c, apperror.BadGateway("Takvim etkinliği oluşturulamadı.", err), "create_calendar_event", requestID)
		}
	}

	// Extract meeting URL from created event, or use the one provided in request
	var meetingURL *string
	if req.MeetingURL != nil && *req.MeetingURL != "" {
		meetingURL = req.MeetingURL
	} else {
		meetingURL = h.calendarService.ExtractMeetingURL(googleEvent)
	}

	// Extract attendees from Google event
	var attendees []models.Attendee
	if googleEvent.Attendees != nil {
		for _, att := range googleEvent.Attendees {
			attendee := models.Attendee{
				Email: att.Email,
			}
			if att.DisplayName != "" {
				attendee.DisplayName = att.DisplayName
			} else {
				attendee.DisplayName = att.Email
			}
			if att.ResponseStatus != "" {
				attendee.ResponseStatus = att.ResponseStatus
			}
			attendees = append(attendees, attendee)
		}
	}

	// Extract organizer info
	var organizerEmail *string
	if googleEvent.Organizer != nil {
		if googleEvent.Organizer.Email != "" {
			organizerEmail = &googleEvent.Organizer.Email
		}
	}

	// Parse start and end times from Google event
	var startTime, endTime time.Time
	if googleEvent.Start != nil && googleEvent.Start.DateTime != "" {
		startTime, err = time.Parse(time.RFC3339, googleEvent.Start.DateTime)
		if err != nil {
			// Use fallback from request if parsing fails (should only happen for fallback events)
			startTime = req.StartTime
		}
	} else {
		startTime = req.StartTime
	}

	if googleEvent.End != nil && googleEvent.End.DateTime != "" {
		endTime, err = time.Parse(time.RFC3339, googleEvent.End.DateTime)
		if err != nil {
			endTime = req.EndTime
		}
	} else {
		endTime = req.EndTime
	}

	// Extract description and location from Google event
	var description *string
	if googleEvent.Description != "" {
		description = &googleEvent.Description
	} else if req.Description != nil {
		description = req.Description
	}

	var location *string
	if googleEvent.Location != "" {
		location = &googleEvent.Location
	} else if req.Location != nil {
		location = req.Location
	}

	// Set default auto_bot_enabled to true if not specified
	autoBotEnabled := true
	if req.AutoBotEnabled != nil {
		autoBotEnabled = *req.AutoBotEnabled
	}

	// Create meeting in database
	meeting := &models.Meeting{
		ID:             uuid.New(),
		UserID:         userID,
		Title:          googleEvent.Summary,
		StartTime:      startTime,
		EndTime:        endTime,
		MeetingURL:     meetingURL,
		Description:    description,
		Location:       location,
		Attendees:      attendees,
		OrganizerEmail: organizerEmail,
		GoogleEventID:  googleEvent.Id,
		AutoBotEnabled: &autoBotEnabled,
		CreatedAt:      time.Now(),
	}

	if err := h.meetingService.UpsertMeeting(meeting); err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Toplantı kaydedilemedi.", "save_meeting", requestID)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"message": "Meeting created successfully",
		"meeting": meeting,
	})
}

// CreateDesktopMeeting handles POST /api/meetings/desktop.
//
// Desktop recorder (Tauri app) registers an ad-hoc recording session here.
// Unlike CreateMeeting, this endpoint does NOT touch Google Calendar — no
// event create, no attendee invite, no Meet link. The `meetings` row exists
// purely to anchor the upload + transcription pipeline (transcript_segments,
// summaries, decisions all FK to meetings.id).
//
// Idempotency contract:
//
//   - The recorder owns a `local_session_id` (ULID) generated at Start.
//   - The synthetic `google_event_id` is derived deterministically as
//     "desktop_<local_session_id>".
//   - Calling this endpoint twice with the same (user, local_session_id)
//     returns the SAME meeting.id. First call returns 201 Created; later
//     calls return 200 OK.
//
// This guarantees that retries from network failures, orphan recovery, and
// upload-time fallback creation never produce duplicate meeting rows.
//
// Open-session create:
//
//   - `ended_at` is OPTIONAL on the create call. Recorder posts at Start
//     (recording in progress, end unknown) and later sets the real end via
//     PATCH /api/meetings/:id/desktop-end.
//   - When `ended_at` is absent, the server stores a 4-hour placeholder so
//     the existing NOT NULL `end_time` constraint is satisfied. The PATCH
//     endpoint overwrites this with the real value.
//
// Server forces:
//
//   - audio_source = "desktop"
//   - auto_bot_enabled = false (bot won't try to join)
//   - transcription_status = "pending"
//   - google_event_id = "desktop_<local_session_id>"  (synthetic)
//
// Caller passes timing + optional title + optional detected_app. Recorder
// uses the returned meeting_id for subsequent /upload-manifest,
// /upload-audio-chunk, /upload-complete.
func (h *MeetingHandler) CreateDesktopMeeting(c *fiber.Ctx) error {
	var req models.CreateDesktopMeetingRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	// local_session_id is the idempotency anchor. Without it, retries would
	// silently create duplicate meetings.
	if !isValidLocalSessionID(req.LocalSessionID) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "local_session_id gereklidir (URL-safe, whitespace içermeyen, 6–64 karakter).",
		})
	}

	if req.StartedAt.IsZero() {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Başlangıç zamanı gereklidir.",
		})
	}

	if req.EndedAt != nil && !req.EndedAt.After(req.StartedAt) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Bitiş zamanı başlangıç zamanından sonra olmalıdır.",
		})
	}

	// Phase 2.5 B8 — validate detected_app against the allowlist so a
	// typo / forward-compat unknown value can't smuggle into the
	// column. Empty pointer (no detected_app on the request) is the
	// normal calendar-armed / bot path and stays nil.
	if req.DetectedApp != nil && *req.DetectedApp != "" &&
		!models.IsValidDetectedApp(*req.DetectedApp) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Bilinmeyen detected_app değeri.",
			"code":  "INVALID_DETECTED_APP",
		})
	}

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

	title := "Desktop Recording " + req.StartedAt.Format("2006-01-02 15:04")
	if req.Title != nil && *req.Title != "" {
		title = *req.Title
	}

	// Open-session: provisional end_time = started_at + 4h. PATCH
	// /api/meetings/:id/desktop-end overwrites this with the real value at
	// Stop. The placeholder satisfies the NOT NULL DB constraint without
	// requiring a schema migration.
	endTime := req.StartedAt.Add(4 * time.Hour)
	if req.EndedAt != nil {
		endTime = *req.EndedAt
	}

	// Deterministic synthetic event ID for idempotency. Same local_session_id
	// from the same user always resolves to the same meeting row.
	syntheticEventID := "desktop_" + req.LocalSessionID

	audioSource := string(services.UploadSourceDesktop)
	autoBotEnabled := false

	meeting := &models.Meeting{
		ID:                  uuid.New(),
		UserID:              userID,
		Title:               title,
		StartTime:           req.StartedAt,
		EndTime:             endTime,
		GoogleEventID:       syntheticEventID,
		AutoBotEnabled:      &autoBotEnabled,
		AudioSource:         &audioSource,
		TranscriptionStatus: "pending",
		CreatedAt:           time.Now(),
		// Phase 2.5 B8 — only populated when the recorder's ad-hoc
		// fusion engine triggered the session (request_confirm path
		// from /ws/desktop start_recording carries None here).
		DetectedApp: req.DetectedApp,
	}

	upserted, isNew, err := h.meetingService.UpsertDesktopMeeting(meeting)
	if err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Toplantı kaydedilemedi.", "save_desktop_meeting", requestID)
	}

	status := fiber.StatusOK
	message := "Desktop meeting already exists"
	if isNew {
		status = fiber.StatusCreated
		message = "Desktop meeting created successfully"
	}

	return c.Status(status).JSON(fiber.Map{
		"message": message,
		"meeting": upserted,
	})
}

// UpdateDesktopEndTime handles PATCH /api/meetings/:id/desktop-end.
//
// Dedicated endpoint for the desktop recorder to set `end_time` once the
// session stops. Kept separate from the general PATCH /api/meetings/:id
// (EditMeeting) for two reasons:
//
//   - EditMeeting calls Google Calendar (UpdateEvent) on every change. We
//     never want that for desktop synthetic meetings.
//   - Recorder Stop should be a fire-and-forget call without worrying
//     whether the generic edit handler will ever evolve to do something
//     calendar-affecting.
//
// Validation: only meetings whose google_event_id begins with "desktop_"
// are accepted. ended_at must be > start_time. Idempotent on repeated
// submission with the same or a later valid timestamp.
func (h *MeetingHandler) UpdateDesktopEndTime(c *fiber.Ctx) error {
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz toplantı kimliği.",
		})
	}

	var req models.UpdateDesktopEndTimeRequest
	if err := c.BodyParser(&req); err != nil || req.EndedAt.IsZero() {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "ended_at gereklidir (RFC3339 formatında).",
		})
	}

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

	if err := h.meetingService.SetDesktopEndTime(c.Context(), userID, meetingID, req.EndedAt); err != nil {
		switch {
		case errors.Is(err, services.ErrMeetingNotOwned):
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "Toplantı bulunamadı.",
			})
		case errors.Is(err, services.ErrMeetingNotDesktop):
			return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
				"error": "Bu uç nokta yalnızca masaüstü oturumları için kullanılabilir.",
			})
		default:
			// Validation errors (ended_at <= start_time) and DB errors collapse
			// to 400 here — the message is descriptive enough.
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": err.Error(),
			})
		}
	}

	slog.Info("desktop meeting end_time updated",
		"meeting_id", meetingID,
		"user_id", userID,
		"ended_at", req.EndedAt,
	)

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":    "Bitiş zamanı güncellendi.",
		"meeting_id": meetingID,
		"ended_at":   req.EndedAt.UTC().Format(time.RFC3339),
	})
}

// isValidLocalSessionID enforces a conservative URL-safe shape for the
// recorder-owned idempotency key. We accept ULID (26 chars), UUID with or
// without dashes, and any opaque alphanumeric/hyphen/underscore string in a
// reasonable length range — without locking the recorder into one specific
// id format.
func isValidLocalSessionID(s string) bool {
	if len(s) < 6 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// RespondMeeting handles PATCH /api/meetings/:id/respond with response: accepted|tentative|declined
func (h *MeetingHandler) RespondMeeting(c *fiber.Ctx) error {
	// Get user context
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

	userEmail, _ := c.Locals("userEmail").(string)
	if userEmail == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "E-posta bilginiz bulunamadı. Lütfen tekrar giriş yapın.",
		})
	}

	var body struct {
		Response string `json:"response"` // accepted|tentative|declined
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}
	resp := strings.ToLower(body.Response)
	if resp == "" {
		resp = "accepted"
	}
	if resp != "accepted" && resp != "tentative" && resp != "declined" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Yanıt kabul, belirsiz veya ret olmalıdır.",
		})
	}

	// Parse meeting ID
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz toplantı bilgisi.",
		})
	}

	// Fetch meeting
	meeting, err := h.meetingService.GetMeetingByID(userID, meetingID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Toplantı bulunamadı.",
		})
	}

	// Get calendar service with refresh token (auto refresh)
	requestID, _ := c.Locals("requestID").(string)
	service, err := h.calendarService.GetServiceWithRefreshToken(userID)
	if err != nil {
		return apperror.Send(c, apperror.Internal("Takvim istemcisi başlatılamadı.", err), "init_calendar_client", requestID)
	}

	// Get event from Google Calendar
	event, err := service.Events.Get("primary", meeting.GoogleEventID).Do()
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Google takvim etkinliği alınamadı.", err), "fetch_google_event", requestID)
	}

	// Update attendee response
	found := false
	for _, att := range event.Attendees {
		if strings.EqualFold(att.Email, userEmail) {
			att.ResponseStatus = resp
			found = true
			break
		}
	}
	if !found {
		// Add attendee entry if missing
		event.Attendees = append(event.Attendees, &calendar.EventAttendee{
			Email:          userEmail,
			ResponseStatus: resp,
		})
	}

	// Patch event
	_, err = service.Events.Patch("primary", meeting.GoogleEventID, event).SendUpdates("all").Do()
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Google takvim etkinliği güncellenemedi.", err), "patch_google_event", requestID)
	}

	// Update local DB attendee status for quicker UI reflection
	_ = h.meetingService.UpdateAttendeeResponse(userID, meetingID, userEmail, resp)

	return c.JSON(fiber.Map{
		"message": fmt.Sprintf("Meeting response set to %s", resp),
	})
}

// CancelMeeting handles DELETE /api/meetings/:id
func (h *MeetingHandler) CancelMeeting(c *fiber.Ctx) error {
	// Get user context
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
	userEmail, _ := c.Locals("userEmail").(string)

	// Parse meeting ID
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz toplantı bilgisi.",
		})
	}

	// Fetch meeting
	meeting, err := h.meetingService.GetMeetingByID(userID, meetingID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Toplantı bulunamadı.",
		})
	}

	calendarWarning := ""

	// Desktop synthetic meetings have no Google Calendar event. Skip the
	// entire calendar interaction — including the calendar client init —
	// so a desktop delete never touches Google credentials, refresh
	// tokens, or remote APIs. The synthetic "desktop_<local_session_id>"
	// ID has no remote counterpart; calling Get/Patch/Delete on it would
	// either 404 or, worse, hit an unrelated event whose opaque ID
	// happens to collide.
	isDesktopSynthetic := strings.HasPrefix(meeting.GoogleEventID, "desktop_")

	if isDesktopSynthetic {
		slog.Info("skipping Google Calendar cleanup for desktop synthetic meeting",
			"meeting_id", meetingID, "google_event_id", meeting.GoogleEventID)
	} else {
		// Get calendar service with refresh token
		service, err := h.calendarService.GetServiceWithRefreshToken(userID)
		if err != nil {
			// Proceed with local delete even if remote fails to init
			slog.Warn("failed to init calendar client", "error", err, "request_id", c.Locals("requestID"))
			if meeting.GoogleEventID != "" {
				calendarWarning = "Toplantı dashboard'dan silindi ancak Google Takvim tarafı doğrulanamadı."
			}
		} else {
			// Attempt to fetch event
			event, getErr := service.Events.Get("primary", meeting.GoogleEventID).Do()
			if getErr != nil {
				// If already gone on Google, proceed to local delete.
				if isGoogleNotFoundError(getErr) {
					slog.Warn("event not found on Google", "error", getErr, "request_id", c.Locals("requestID"))
				} else {
					slog.Warn("failed to fetch event from Google", "error", getErr, "request_id", c.Locals("requestID"))
					calendarWarning = "Toplantı dashboard'dan silindi ancak Google Takvim'den silme işlemi tamamlanamadı."
				}
			} else {
				isOrganizer := meeting.OrganizerEmail != nil && userEmail != "" && strings.EqualFold(*meeting.OrganizerEmail, userEmail)

				if isOrganizer {
					// Organizer cancels for everyone
					if delErr := service.Events.Delete("primary", meeting.GoogleEventID).SendUpdates("all").Do(); delErr != nil {
						slog.Warn("failed to delete event on Google", "error", delErr, "request_id", c.Locals("requestID"))
						calendarWarning = "Toplantı dashboard'dan silindi ancak Google Takvim'den silme işlemi başarısız oldu."
					}
				} else {
					// Attendee declines: update responseStatus to declined
					declined := false
					if event.Attendees != nil {
						for _, att := range event.Attendees {
							if strings.EqualFold(att.Email, userEmail) {
								att.ResponseStatus = "declined"
								declined = true
								break
							}
						}
					}

					if declined {
						if _, patchErr := service.Events.Patch("primary", meeting.GoogleEventID, event).SendUpdates("all").Do(); patchErr != nil {
							slog.Warn("failed to patch event decline", "error", patchErr, "request_id", c.Locals("requestID"))
							calendarWarning = "Toplantı dashboard'dan silindi ancak Google Takvim'de davet reddi güncellenemedi."
						}
					} else {
						// As a fallback, remove from user's primary calendar (this may decline)
						if delErr := service.Events.Delete("primary", meeting.GoogleEventID).Do(); delErr != nil {
							slog.Warn("failed to delete event from primary", "error", delErr, "request_id", c.Locals("requestID"))
							calendarWarning = "Toplantı dashboard'dan silindi ancak Google Takvim'den kaldırma işlemi başarısız oldu."
						}
					}
				}
			}
		}
	}

	// Remove from local database
	if err := h.meetingService.DeleteMeeting(userID, meetingID); err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Toplantı silinemedi.", "delete_meeting", requestID)
	}

	response := fiber.Map{
		"message": "Meeting cancelled",
	}
	if calendarWarning != "" {
		response["calendar_warning"] = calendarWarning
	}

	return c.JSON(response)
}

// JoinMeetingWithAgent handles POST /api/meetings/:id/join-agent
// Makes HTTP request to separate mia-agent API service
func (h *MeetingHandler) JoinMeetingWithAgent(c *fiber.Ctx) error {
	// Get user ID from context
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

	// Get meeting ID from params
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz toplantı bilgisi.",
		})
	}

	// Fetch meeting from database
	meeting, err := h.meetingService.GetMeetingByID(userID, meetingID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Toplantı bulunamadı.",
		})
	}

	// Check if meeting has URL
	if meeting.MeetingURL == nil || *meeting.MeetingURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Bu toplantının bağlantısı bulunmuyor.",
		})
	}

	// Check if bot is already active
	if meeting.BotStatus != nil {
		status := strings.ToLower(*meeting.BotStatus)
		if status == "joining" || status == "joined" {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error": fmt.Sprintf("Bot is already %s this meeting", status),
			})
		}
	}

	// Fetch profile to get custom bot settings
	requestID, _ := c.Locals("requestID").(string)
	profile, err := h.profileService.GetProfile(userID)
	if err != nil {
		return apperror.SendServiceError(c, err, "Kullanıcı bilgileri yüklenemedi.", fiber.StatusInternalServerError, "fetch_user_profile", requestID)
	}

	// Get external bot API URL from environment
	agentAPIURL := os.Getenv("EXTERNAL_BOT_API_URL")
	if agentAPIURL == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Yapay zeka asistanı şu anda kullanılamıyor.",
		})
	}

	// Get user's custom bot name and language from profile (already fetched above)
	botName := "MIA Assistant" // Default
	if profile.BotName != "" {
		botName = profile.BotName
	}
	botLanguage := profile.BotLanguage

	// Prepare request body for mia-agent API
	requestBody := map[string]interface{}{
		"meeting_id":  meeting.ID.String(),
		"meeting_url": *meeting.MeetingURL,
		"bot_name":    botName,
	}
	// Only send language if explicitly set (not "auto") — ElevenLabs auto-detects when omitted
	if botLanguage != "" && botLanguage != "auto" {
		requestBody["language"] = botLanguage
	}

	// Marshal request body
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "İstek hazırlanamadı. Lütfen tekrar deneyin.",
		})
	}

	// Make HTTP POST request to separate mia-agent API
	resp, err := http.Post(
		fmt.Sprintf("%s/meetings/join", agentAPIURL),
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Yapay zeka asistanına bağlanılamadı.", err), "connect_agent_api", requestID)
	}
	defer resp.Body.Close()

	// Parse response from mia-agent API
	var agentResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&agentResponse); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Asistan yanıtı işlenemedi.",
		})
	}

	// Check if request was successful
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.Status(resp.StatusCode).JSON(agentResponse)
	}

	return c.JSON(fiber.Map{
		"message":      "Agent joined meeting successfully",
		"meeting_id":   agentResponse["meeting_id"],
		"status":       agentResponse["status"],
		"container_id": agentResponse["container_id"],
	})
}

// EditMeeting handles PATCH /api/meetings/:id
func (h *MeetingHandler) EditMeeting(c *fiber.Ctx) error {
	// 1. Parse user ID
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

	// 2. Parse meeting ID
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz toplantı bilgisi.",
		})
	}

	// 3. Parse request body
	var req models.EditMeetingRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	// 4. Validate time constraints (including partial updates against stored values)
	if req.StartTime != nil || req.EndTime != nil {
		effectiveStart := req.StartTime
		effectiveEnd := req.EndTime

		// For partial time edits, fetch existing meeting to compare
		if effectiveStart == nil || effectiveEnd == nil {
			existing, err := h.meetingService.GetMeetingByID(userID, meetingID)
			if err != nil {
				return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
					"error": "Toplantı bulunamadı.",
				})
			}
			if effectiveStart == nil {
				effectiveStart = &existing.StartTime
			}
			if effectiveEnd == nil {
				effectiveEnd = &existing.EndTime
			}
		}

		if effectiveEnd.Before(*effectiveStart) || effectiveEnd.Equal(*effectiveStart) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Bitiş zamanı başlangıç zamanından sonra olmalıdır.",
			})
		}
	}

	// 5. Build field maps: dbFields (all) and calendarFields (sync to Google Calendar)
	dbFields := map[string]interface{}{}
	calendarFields := map[string]interface{}{}

	if req.Title != nil {
		dbFields["title"] = *req.Title
		calendarFields["title"] = *req.Title
	}
	if req.StartTime != nil {
		dbFields["start_time"] = *req.StartTime
		calendarFields["start_time"] = *req.StartTime
	}
	if req.EndTime != nil {
		dbFields["end_time"] = *req.EndTime
		calendarFields["end_time"] = *req.EndTime
	}
	if req.Description != nil {
		dbFields["description"] = *req.Description
		calendarFields["description"] = *req.Description
	}
	if req.Location != nil {
		dbFields["location"] = *req.Location
		calendarFields["location"] = *req.Location
	}
	if req.Attendees != nil {
		attendees := make([]models.Attendee, 0, len(req.Attendees))
		for _, email := range req.Attendees {
			attendees = append(attendees, models.Attendee{
				Email:       email,
				DisplayName: email,
			})
		}
		dbFields["attendees"] = attendees
		calendarFields["attendees"] = req.Attendees // raw emails for Google API
	}

	// Mia-only fields — NEVER synced to Google Calendar
	if req.AgendaItems != nil {
		dbFields["agenda_items"] = *req.AgendaItems
	}
	if req.ExpectedOutcomes != nil {
		dbFields["expected_outcomes"] = *req.ExpectedOutcomes
	}

	if len(dbFields) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Güncellenecek alan bulunamadı.",
		})
	}

	// 6. Update DB (includes ownership + future-only checks)
	updatedMeeting, err := h.meetingService.EditMeeting(c.Context(), userID, meetingID, dbFields)
	if err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "Bu toplantıyı düzenleme yetkiniz yok. Sadece toplantı sahibi düzenleyebilir.",
			})
		}
		if strings.Contains(err.Error(), "past meetings") {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Geçmiş toplantılar düzenlenemez.",
			})
		}
		if strings.Contains(err.Error(), "not found") {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "Toplantı bulunamadı.",
			})
		}
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Toplantı güncellenemedi.", "edit_meeting", requestID)
	}

	// 7. Sync calendar-synced fields to Google Calendar (best-effort).
	//
	// Desktop synthetic meetings (google_event_id="desktop_<local_session_id>")
	// have NO Google event behind them — pushing the synthetic ID to the
	// Google API would 404 in the best case and corrupt an unrelated event
	// in the worst. Skip the sync entirely for desktop sessions.
	isDesktopSynthetic := strings.HasPrefix(updatedMeeting.GoogleEventID, "desktop_")
	if len(calendarFields) > 0 && updatedMeeting.GoogleEventID != "" && !isDesktopSynthetic {
		sendUpdates := "none"
		if req.SendUpdates != nil {
			sendUpdates = *req.SendUpdates
		}

		if err := h.calendarService.UpdateEvent(
			userID,
			updatedMeeting.GoogleEventID,
			calendarFields,
			sendUpdates,
		); err != nil {
			slog.Warn("failed to sync edit to Google Calendar", "meeting_id", meetingID, "error", err, "request_id", c.Locals("requestID"))
			return c.JSON(fiber.Map{
				"message":          "Toplantı güncellendi (Google Takvim senkronizasyonu başarısız).",
				"meeting":          updatedMeeting,
				"calendar_warning": "Google Takvim senkronizasyonu başarısız oldu.",
			})
		}
	}

	return c.JSON(fiber.Map{
		"message": "Toplantı başarıyla güncellendi.",
		"meeting": updatedMeeting,
	})
}

// GetFullTranscript handles GET /api/meetings/:id/full-transcript
func (h *MeetingHandler) GetFullTranscript(c *fiber.Ctx) error {
	// Get user ID from context
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid user ID"})
	}

	// Get meeting ID
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	// Check access (Reusing existing logic or simple check)
	// We should probably check if user has access to this meeting
	_, err = h.meetingService.GetMeetingByID(userID, meetingID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Meeting not found or access denied"})
	}

	// Fetch chat blocks (corrected transcript)
	blocks, err := h.transcriptRepo.GetChatBlocksByMeetingID(c.Context(), meetingID)
	if err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Transkript yüklenemedi.", "fetch_transcript", requestID)
	}

	// Map to response format
	// The frontend expects a list of objects. We can return the blocks directly or map them.
	// Let's return them as is, matching the frontend expectation of [{speaker, text, start_time...}]

	return c.JSON(blocks)
}

// UpdateRecordingMethod handles PATCH /api/meetings/:id/recording-method
// Body: { "method": "bot" | "desktop" | "skip" | null }
// null (or missing) clears the override so the meeting falls back to the
// user's profile-level default. `skip` (Phase 2.4 B5) is a meeting-level
// only escape hatch that tells the scheduler to do nothing for this
// event.
func (h *MeetingHandler) UpdateRecordingMethod(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Oturum bulunamadı."})
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Geçersiz kullanıcı."})
	}
	meetingID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Geçersiz toplantı ID."})
	}

	// Accept either a string ("bot"|"desktop") or JSON null to clear.
	// Using *string preserves the distinction between "omitted" and "null".
	var body struct {
		Method *string `json:"method"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Geçersiz istek gövdesi."})
	}

	if body.Method != nil && *body.Method != "" && !models.IsValidRecordingMethodOverride(*body.Method) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Kayıt yöntemi 'bot', 'desktop' veya 'skip' olmalı (boş bırakırsanız profil varsayılanı kullanılır).",
			"code":  "INVALID_RECORDING_METHOD",
		})
	}

	// Phase 5 fix 2026-04-20 — delegate to MeetingService so the handler
	// stays at its layer (auth + parse + validation + response mapping).
	if err := h.meetingService.SetRecordingMethodOverride(c.Context(), userID, meetingID, body.Method); err != nil {
		if errors.Is(err, services.ErrMeetingNotOwned) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Toplantı bulunamadı."})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error":  "Toplantı güncellenemedi.",
			"detail": err.Error(),
		})
	}

	// Return effective method so the UI can re-render without a round-trip.
	resolver := services.NewRecordingMethodService()
	effective, err := resolver.GetEffectiveRecordingMethod(c.Context(), meetingID)
	if err != nil {
		// The override write succeeded — swallow this secondary failure
		// and let the client call GET /meetings/:id if it needs exact state.
		effective = ""
	}

	// Phase 2.4 B7: nudge the recorder to re-poll its candidate queue
	// so the new effective_method takes effect without waiting for the
	// 15-min poll. Best-effort — offline recorders pick it up on the
	// next poll regardless.
	services.PublishCalendarInvalidate(h.desktopHub, userID)

	return c.JSON(fiber.Map{
		"meeting_id":       meetingID,
		"override":         body.Method,
		"effective_method": effective,
	})
}
