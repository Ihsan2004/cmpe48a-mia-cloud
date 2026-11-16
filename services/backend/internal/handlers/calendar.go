package handlers

import (
	"log/slog"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/apperror"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type CalendarHandler struct {
	calendarService      *services.CalendarService
	meetingService       *services.MeetingService
	calendarWatchService *services.CalendarWatchService
	upcomingService      *services.CalendarUpcomingService
	profileService       *services.ProfileService
}

func NewCalendarHandler() *CalendarHandler {
	meetingRepo := repository.NewMeetingRepository()
	briefRepo := repository.NewBriefRepository()
	resolver := services.NewRecordingMethodService()
	profiles := services.NewProfileService()
	return &CalendarHandler{
		calendarService:      services.NewCalendarService(),
		meetingService:       services.NewMeetingService(),
		calendarWatchService: services.NewCalendarWatchService(),
		upcomingService:      services.NewCalendarUpcomingService(meetingRepo, briefRepo, resolver, profiles),
		profileService:       profiles,
	}
}

// GetUpcoming handles GET /api/calendar/upcoming
//
// Phase 2.4 B6: returns the recorder's candidate-queue feed for the
// next 6 h. Each event carries a resolved `effective_method` so the
// recorder can ignore events whose method is `bot` or `skip` without
// a follow-up round-trip.
func (h *CalendarHandler) GetUpcoming(c *fiber.Ctx) error {
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

	// userEmail is needed for the attendee-overlap branch in the meetings
	// query (see GetUpcomingMeetings WHERE clause). Empty string is fine
	// — the SQL gates the attendee match on `userEmail <> ''`.
	userEmail := ""
	if profile, pErr := h.profileService.GetProfile(userID); pErr == nil &&
		profile != nil && profile.Email != nil {
		userEmail = *profile.Email
	}

	resp, err := h.upcomingService.GetUpcomingForRecorder(c.Context(), userID, userEmail)
	if err != nil {
		requestID, _ := c.Locals("requestID").(string)
		return apperror.SendDBError(c, err, "Yaklaşan toplantılar yüklenemedi.", "calendar_upcoming", requestID)
	}
	return c.JSON(resp)
}

// SyncCalendar handles POST /api/sync-calendar
func (h *CalendarHandler) SyncCalendar(c *fiber.Ctx) error {
	var req models.SyncCalendarRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	// Access token is now optional - we prefer refresh token from DB
	// But keep it for backward compatibility

	// Get user ID from context (set by auth middleware)
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

	// Fetch events from Google Calendar
	// Use refresh token from DB (offline access) if available, otherwise fallback to access token
	events, err := h.calendarService.FetchUpcomingEvents(&userID, req.AccessToken, 500)
	if err != nil {
		slog.Error("Failed to fetch events from Google Calendar",
			"user_id", userID,
			"error", err,
			"request_id", c.Locals("requestID"),
		)

		// Check if error is about missing refresh token or connection
		errMsg := err.Error()
		if errMsg == "User not connected to Google" ||
			errMsg == "failed to get refresh token" ||
			errMsg == "User not connected to Google Calendar" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Google Calendar bağlantısı gerekiyor. Lütfen Ayarlar sayfasından Google Takvimi bağlayın.",
			})
		}

		// Check if error is about expired/invalid refresh token
		if strings.Contains(errMsg, "süresi dolmuş") ||
			strings.Contains(errMsg, "geçersiz") ||
			strings.Contains(errMsg, "invalid_grant") ||
			strings.Contains(errMsg, "token has been expired") {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": errMsg, // Use the detailed error message from service
			})
		}

		return apperror.Send(c, apperror.BadGateway(ErrCalendarSyncFailed, err), "fetch_calendar_events")
	}

	slog.Info("Fetched events from Google Calendar",
		"event_count", len(events),
		"user_id", userID,
		"request_id", c.Locals("requestID"),
	)

	// Process and upsert meetings
	syncedCount := 0
	skippedCount := 0
	var googleEventIDs []string // Track all Google event IDs from this sync

	for _, event := range events {
		// Skip all-day events or events without start time
		if event.Start == nil {
			continue
		}

		// Extract meeting URL - only sync events with meeting URLs
		meetingURL := h.calendarService.ExtractMeetingURL(event)

		// Skip events without meeting URL (birthdays, reminders, physical meetings, etc.)
		if meetingURL == nil || *meetingURL == "" {
			skippedCount++
			// Debug: Log skipped events to help diagnose missing meetings
			if event.Summary != "" {
				eventStart := event.Start.DateTime
				if eventStart == "" {
					eventStart = event.Start.Date
				}
				slog.Debug("Skipping event without meeting URL",
					"event_title", event.Summary,
					"event_id", event.Id,
					"start", eventStart,
					"location", event.Location,
					"request_id", c.Locals("requestID"),
				)
			}
			continue
		}

		// Safety: track candidate Google event IDs independently from DB upsert success.
		// This prevents accidental deletions when upsert has transient failures.
		if event.Id != "" {
			googleEventIDs = append(googleEventIDs, event.Id)
		}

		var startTime, endTime time.Time
		var err error

		// Handle dateTime (normal events) or date (all-day events)
		if event.Start.DateTime != "" {
			startTime, err = time.Parse(time.RFC3339, event.Start.DateTime)
			if err != nil {
				skippedCount++
				continue
			}
		} else if event.Start.Date != "" {
			// Skip all-day events for now
			skippedCount++
			continue
		} else {
			skippedCount++
			continue
		}

		if event.End != nil && event.End.DateTime != "" {
			endTime, err = time.Parse(time.RFC3339, event.End.DateTime)
			if err != nil {
				endTime = startTime.Add(1 * time.Hour) // Default 1 hour
			}
		} else {
			endTime = startTime.Add(1 * time.Hour) // Default 1 hour
		}

		slog.Debug("Processing event with meeting URL",
			"event_title", event.Summary,
			"event_id", event.Id,
			"meeting_url", *meetingURL,
			"request_id", c.Locals("requestID"),
		)

		// Extract attendees
		var attendees []models.Attendee
		if event.Attendees != nil {
			for _, att := range event.Attendees {
				attendee := models.Attendee{
					Email: att.Email,
				}
				if att.DisplayName != "" {
					attendee.DisplayName = att.DisplayName
				} else {
					attendee.DisplayName = att.Email // Fallback to email if no display name
				}
				if att.ResponseStatus != "" {
					attendee.ResponseStatus = att.ResponseStatus
				}
				attendees = append(attendees, attendee)
			}
		}

		// Extract organizer info
		var organizerEmail *string
		if event.Organizer != nil {
			if event.Organizer.Email != "" {
				organizerEmail = &event.Organizer.Email
			}
		}

		// Extract description and location
		var description *string
		if event.Description != "" {
			description = &event.Description
		}

		var location *string
		if event.Location != "" {
			location = &event.Location
		}

		// Extract recurring series ID for chain detection
		var calendarSeriesID *string
		if event.RecurringEventId != "" {
			calendarSeriesID = &event.RecurringEventId
		}

		// Create meeting model
		meeting := &models.Meeting{
			ID:               uuid.New(),
			UserID:           userID,
			Title:            event.Summary,
			StartTime:        startTime,
			EndTime:          endTime,
			MeetingURL:       meetingURL,
			Description:      description,
			Location:         location,
			Attendees:        attendees,
			OrganizerEmail:   organizerEmail,
			GoogleEventID:    event.Id,
			CalendarSeriesID: calendarSeriesID,
			CreatedAt:        time.Now(),
		}

		// Upsert meeting
		if err := h.meetingService.UpsertMeeting(meeting); err != nil {
			// Log error but continue processing other events
			slog.Error("Error upserting meeting",
				"event_title", event.Summary,
				"event_id", event.Id,
				"error", err,
				"request_id", c.Locals("requestID"),
			)
			skippedCount++
			continue
		}

		syncedCount++
		slog.Debug("Synced meeting from calendar",
			"event_title", event.Summary,
			"start_time", startTime.Format("2006-01-02 15:04"),
			"request_id", c.Locals("requestID"),
		)
	}

	// Delete meetings that are no longer in Google Calendar
	deletedCount := 0

	// Create a window matching our fetch logic (30 days past, 30 days future)
	// See `FetchUpcomingEvents` implementation in `calendar.go`
	now := time.Now()
	windowStart := now.AddDate(0, 0, -30)
	windowEnd := now.AddDate(0, 0, 30)

	if len(googleEventIDs) == 0 || syncedCount == 0 {
		slog.Warn("Skipping delete phase due to safety check",
			"synced_count", syncedCount,
			"candidate_ids", len(googleEventIDs),
			"request_id", c.Locals("requestID"),
		)
	} else {
		if count, err := h.meetingService.DeleteMeetingsNotInGoogleEventIDs(userID, googleEventIDs, windowStart, windowEnd); err != nil {
			// Log error but don't fail the entire sync
			slog.Error("Error deleting removed meetings",
				"error", err,
				"request_id", c.Locals("requestID"),
			)
		} else {
			deletedCount = count
			if deletedCount > 0 {
				slog.Info("Deleted meetings no longer in Google Calendar",
					"deleted_count", deletedCount,
					"request_id", c.Locals("requestID"),
				)
			}
		}
	}

	slog.Info("Calendar sync complete",
		"synced_count", syncedCount,
		"skipped_count", skippedCount,
		"deleted_count", deletedCount,
		"request_id", c.Locals("requestID"),
	)

	// After successful sync, ensure calendar watch is active for real-time updates
	go func() {
		if err := h.calendarWatchService.EnsureWatchForUser(userID); err != nil {
			slog.Warn("Failed to ensure calendar watch for user",
				"user_id", userID,
				"error", err,
			)
		}
	}()

	return c.JSON(fiber.Map{
		"message":       "Calendar synced successfully",
		"synced_count":  syncedCount,
		"skipped_count": skippedCount,
		"deleted_count": deletedCount,
		"total_events":  len(events),
	})
}

// StartCalendarWatch creates a push notification subscription for real-time calendar updates
// POST /api/calendar/watch
func (h *CalendarHandler) StartCalendarWatch(c *fiber.Ctx) error {
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

	hasActive, err := h.calendarWatchService.HasActiveSubscription(userID)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Takvim izleme durumu kontrol edilemedi. Lütfen tekrar deneyin.", err), "check_calendar_watch")
	}
	if hasActive {
		return c.JSON(fiber.Map{
			"message": "Calendar watch already active",
		})
	}

	// Create calendar watch subscription
	subscription, err := h.calendarWatchService.WatchCalendar(userID)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Takvim izleme başlatılamadı. Lütfen tekrar deneyin.", err), "create_calendar_watch")
	}

	return c.JSON(fiber.Map{
		"message":    "Calendar watch created successfully",
		"channel_id": subscription.ChannelID,
		"expiration": subscription.Expiration.Format(time.RFC3339),
	})
}

// StopCalendarWatch stops push notifications for a user's calendar
// DELETE /api/calendar/watch
func (h *CalendarHandler) StopCalendarWatch(c *fiber.Ctx) error {
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

	// Stop calendar watch subscription
	err = h.calendarWatchService.StopWatchForUser(userID)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Takvim izleme durdurulamadı.", err), "stop_calendar_watch")
	}

	return c.JSON(fiber.Map{
		"message": "Calendar watch stopped successfully",
	})
}
