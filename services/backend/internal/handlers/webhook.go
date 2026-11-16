package handlers

import (
	"context"
	"log/slog"
	"os"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/worker"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"google.golang.org/api/calendar/v3"
)

// WebhookHandler handles incoming webhooks from external services
type WebhookHandler struct {
	calendarWatchService *services.CalendarWatchService
	calendarHandler      *CalendarHandler
	postMeetingService   *services.PostMeetingService
	meetingRepo          repository.MeetingRepository
}

// NewWebhookHandler creates a new WebhookHandler
func NewWebhookHandler() *WebhookHandler {
	return &WebhookHandler{
		calendarWatchService: services.NewCalendarWatchService(),
		calendarHandler:      NewCalendarHandler(),
		postMeetingService:   services.NewPostMeetingService(),
		meetingRepo:          repository.NewMeetingRepository(),
	}
}

// GoogleCalendarWebhook handles push notifications from Google Calendar
// This is called by Google whenever calendar events change
//
// Google sends the following headers:
// - X-Goog-Channel-ID: The channel ID we specified when creating the watch
// - X-Goog-Resource-ID: The resource ID returned by Google
// - X-Goog-Resource-State: "sync" (initial sync) or "exists" (change detected)
// - X-Goog-Resource-URI: The URI being watched
// - X-Goog-Message-Number: Incrementing message number
//
// Note: Google expects a 200 response within a few seconds, or it may retry
func (h *WebhookHandler) GoogleCalendarWebhook(c *fiber.Ctx) error {
	// Get headers from Google
	channelID := c.Get("X-Goog-Channel-ID")
	resourceState := c.Get("X-Goog-Resource-State")
	resourceID := c.Get("X-Goog-Resource-ID")

	slog.Info("📬 Received Google Calendar webhook", "channel_id", channelID, "state", resourceState, "resource_id", resourceID)

	// Validate channel ID
	if channelID == "" {
		slog.Warn("❌ Missing X-Goog-Channel-ID header")
		return c.SendStatus(fiber.StatusBadRequest)
	}

	// Find the subscription by channel ID
	subscription, err := h.calendarWatchService.GetSubscriptionByChannelID(channelID)
	if err != nil {
		slog.Warn("❌ Subscription not found for channel", "channel_id", channelID, "error", err)
		// Return 404 so Google knows to stop sending notifications
		return c.SendStatus(fiber.StatusNotFound)
	}

	// Handle based on resource state
	switch resourceState {
	case "sync":
		// Initial sync notification - just acknowledge
		slog.Info("🔄 Sync notification received", "user_id", subscription.UserID)
		return c.SendStatus(fiber.StatusOK)

	case "exists":
		// Something changed in the calendar - trigger a sync
		slog.Info("📅 Calendar change detected", "user_id", subscription.UserID)

		// Trigger calendar sync in background
		if queued := worker.GlobalPool.SubmitWithTimeout(func() {
			h.syncCalendarForUser(subscription.UserID)
		}, 2*time.Second); !queued {
			slog.Warn("⚠️ Worker queue saturated, running calendar sync in detached goroutine", "user_id", subscription.UserID)
			go h.syncCalendarForUser(subscription.UserID)
		}

		// Return OK immediately (Google expects quick response)
		return c.SendStatus(fiber.StatusOK)

	default:
		// Unknown state - just acknowledge
		slog.Warn("⚠️ Unknown resource state", "state", resourceState)
		return c.SendStatus(fiber.StatusOK)
	}
}

// syncCalendarForUser triggers a calendar sync for a specific user
func (h *WebhookHandler) syncCalendarForUser(userID uuid.UUID) {
	slog.Info("Starting automatic calendar sync", "user_id", userID)

	// Use the existing calendar sync logic
	calendarService := services.NewCalendarService()
	meetingService := services.NewMeetingService()

	// Fetch events from Google Calendar
	events, err := calendarService.FetchUpcomingEvents(&userID, "", 50)
	if err != nil {
		slog.Error("Failed to fetch calendar events", "user_id", userID, "error", err)
		return
	}

	slog.Info("Fetched events from Google Calendar", "count", len(events), "user_id", userID)

	// Process and sync events (same logic as SyncCalendar handler)
	syncedCount := 0
	var googleEventIDs []string

	for _, event := range events {
		if event.Start == nil {
			continue
		}

		// Skip all-day events
		if event.Start.DateTime == "" {
			continue
		}

		// Extract meeting URL
		meetingURL := calendarService.ExtractMeetingURL(event)

		// Skip events without meeting URL (birthdays, reminders, physical meetings, etc.)
		if meetingURL == nil || *meetingURL == "" {
			continue
		}

		// Safety: track candidate Google event IDs independently from DB upsert success.
		if event.Id != "" {
			googleEventIDs = append(googleEventIDs, event.Id)
		}

		// Parse start time
		startTime, err := parseEventTime(event.Start)
		if err != nil {
			continue
		}

		// Parse end time
		endTime, err := parseEventTime(event.End)
		if err != nil {
			continue
		}

		// Build attendees list
		var attendees []models.Attendee
		for _, a := range event.Attendees {
			attendee := models.Attendee{
				Email: a.Email,
			}
			if a.DisplayName != "" {
				attendee.DisplayName = a.DisplayName
			} else {
				attendee.DisplayName = a.Email
			}
			if a.ResponseStatus != "" {
				attendee.ResponseStatus = a.ResponseStatus
			}
			attendees = append(attendees, attendee)
		}

		// Extract organizer
		var organizerEmail *string
		if event.Organizer != nil {
			if event.Organizer.Email != "" {
				organizerEmail = &event.Organizer.Email
			}
		}

		// Description and location
		var description, location *string
		if event.Description != "" {
			description = &event.Description
		}
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
		if err := meetingService.UpsertMeeting(meeting); err != nil {
			continue
		}

		syncedCount++
	}

	// Delete meetings no longer in Google Calendar
	deletedCount := 0

	// Create a window matching our fetch logic (30 days past, 30 days future)
	// Actually, the calendar sync logic in `syncCalendarForUser` uses `calendarService.FetchUpcomingEvents`,
	// which hardcodes "last 30 days to next 30 days" inside `calendar.go`.
	// We need to match that window here.
	now := time.Now()
	windowStart := now.AddDate(0, 0, -30) // Match calendar.go:250
	windowEnd := now.AddDate(0, 0, 30)    // Match calendar.go:251

	if len(googleEventIDs) == 0 || syncedCount == 0 {
		slog.Warn("Skipping delete phase due to safety check", "user_id", userID, "synced", syncedCount, "candidate_ids", len(googleEventIDs))
	} else if count, err := meetingService.DeleteMeetingsNotInGoogleEventIDs(userID, googleEventIDs, windowStart, windowEnd); err == nil {
		deletedCount = count
	} else {
		slog.Error("Auto-sync delete phase failed", "user_id", userID, "error", err)
	}

	slog.Info("Auto-sync complete", "user_id", userID, "synced", syncedCount, "deleted", deletedCount)
}

// parseEventTime parses a Google Calendar event datetime
func parseEventTime(eventDateTime *calendar.EventDateTime) (time.Time, error) {
	if eventDateTime.DateTime != "" {
		return time.Parse(time.RFC3339, eventDateTime.DateTime)
	}
	// For all-day events
	return time.Parse("2006-01-02", eventDateTime.Date)
}

// BotWebhookRequest represents the payload from the external bot API
// This is called when the bot leaves a meeting or the meeting ends
type BotWebhookRequest struct {
	MeetingID  string `json:"meeting_id"`  // Our meeting UUID
	MeetingURL string `json:"meeting_url"` // The meeting URL (Google Meet, Zoom, etc.)
	Status     string `json:"status"`      // "completed", "failed", "left"
	Duration   int    `json:"duration"`    // Meeting duration in seconds
	Transcript bool   `json:"transcript"`  // Whether transcript was captured
	Reason     string `json:"reason"`      // Optional: why bot left (e.g., "meeting_ended", "kicked", "error")
}

// BotCompletionWebhook handles notifications from the external bot API when a meeting ends
// This triggers automatic summarization and email follow-up
//
// Expected workflow:
// 1. External bot joins meeting via our API
// 2. Bot records transcript to Supabase
// 3. When meeting ends, external bot calls this webhook
// 4. We trigger async summarization + email
//
// POST /api/webhooks/bot-completion
func (h *WebhookHandler) BotCompletionWebhook(c *fiber.Ctx) error {
	var req BotWebhookRequest
	if err := c.BodyParser(&req); err != nil {
		slog.Warn("Invalid bot webhook payload", "error", err)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	slog.Info("🤖 Received bot completion webhook",
		"meeting_id", req.MeetingID,
		"url", req.MeetingURL,
		"status", req.Status,
		"duration", req.Duration,
		"transcript", req.Transcript,
	)

	// Validate required fields
	if req.MeetingID == "" && req.MeetingURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "meeting_id or meeting_url is required",
		})
	}

	// Parse meeting ID if provided
	var meetingID *uuid.UUID
	if req.MeetingID != "" {
		id, err := uuid.Parse(req.MeetingID)
		if err != nil {
			slog.Warn("⚠️ Invalid meeting_id format", "meeting_id", req.MeetingID)
		} else {
			meetingID = &id
		}
	}

	// Handle based on status
	switch req.Status {
	case "completed", "left":
		// 1. Status Update: Backend immediately marks meeting as completed in Main DB
		// This happens synchronously before acknowledging the bot
		status := "completed"
		updateSuccess := false

		if meetingID != nil {
			if err := h.postMeetingService.UpdateBotStatus(*meetingID, status); err != nil {
				slog.Warn("⚠️ Failed to update bot status", "status", status, "meeting_id", *meetingID, "error", err)
				slog.Info("Attempting URL fallback...")
			} else {
				slog.Info("✅ Meeting marked as completed in DB", "meeting_id", *meetingID)
				updateSuccess = true
			}
		}

		// Fallback: if update failed or no ID, try finding meeting by URL
		if !updateSuccess && req.MeetingURL != "" {
			foundMeetingID, err := h.meetingRepo.GetMeetingIDByURL(context.Background(), req.MeetingURL)
			if err == nil {
				if err := h.postMeetingService.UpdateBotStatus(foundMeetingID, status); err != nil {
					slog.Warn("⚠️ Failed to update bot status by URL", "error", err)
				} else {
					slog.Info("✅ Meeting (found by URL) marked as completed", "meeting_id", foundMeetingID)
					// Update meetingID pointer for post-processing
					meetingID = &foundMeetingID
				}
			} else {
				slog.Warn("⚠️ Could not find meeting by URL to update status", "url", req.MeetingURL)
			}
		}

		// Phase 3.5 — post-meeting pipeline is triggered by the ElevenLabs
		// webhook (IngestWebhookResult) after transcript_segments land in DB,
		// NOT by bot-completion. Enqueuing here races with ElevenLabs ingest
		// and causes ProcessMeetingCompletion to run on an empty transcript,
		// flipping the summary to `no_transcript` even when the batch path
		// eventually succeeds.
		//
		// Legacy escape hatch: LEGACY_BOT_COMPLETION_TRIGGER=true keeps the
		// old behaviour for environments still on the realtime bot pipeline
		// (none in production after Phase 7).
		if meetingID != nil && os.Getenv("LEGACY_BOT_COMPLETION_TRIGGER") == "true" {
			slog.Info("🚀 [LEGACY] Triggering background Meeting Intelligence workflow", "meeting_id", *meetingID)
			jobID, err := worker.EnqueuePostMeetingCompletionJob(context.Background(), *meetingID, req.MeetingURL)
			if err != nil {
				slog.Error("❌ Failed to enqueue post-meeting job, using in-memory fallback", "meeting_id", *meetingID, "error", err)
				if queued := worker.GlobalPool.SubmitWithTimeout(func() {
					if processErr := h.postMeetingService.ProcessMeetingCompletion(meetingID, req.MeetingURL); processErr != nil {
						slog.Error("❌ Fallback post-meeting processing failed", "meeting_id", *meetingID, "error", processErr)
					}
				}, 2*time.Second); !queued {
					slog.Warn("⚠️ Worker queue still saturated, using detached goroutine fallback", "meeting_id", *meetingID)
					go func(id *uuid.UUID, meetingURL string) {
						if processErr := h.postMeetingService.ProcessMeetingCompletion(id, meetingURL); processErr != nil {
							slog.Error("❌ Detached fallback post-meeting processing failed", "meeting_id", *id, "error", processErr)
						}
					}(meetingID, req.MeetingURL)
				}
			} else {
				slog.Info("✅ Post-meeting job enqueued", "meeting_id", *meetingID, "job_id", jobID)
			}
		} else if meetingID != nil {
			slog.Info("bot-completion recorded; post-meeting pipeline deferred to ElevenLabs webhook",
				"meeting_id", *meetingID, "duration", req.Duration, "exit_reason", req.Reason)
		}

		return c.JSON(fiber.Map{
			"success": true,
			"message": "Completion signal recorded. Post-meeting pipeline runs after ElevenLabs webhook ingest.",
		})

	case "failed":
		slog.Error("Bot failed for meeting", "meeting_id", req.MeetingID, "reason", req.Reason)
		// Update bot status to failed
		if meetingID != nil {
			h.postMeetingService.UpdateBotStatus(*meetingID, "failed")
		}
		return c.JSON(fiber.Map{
			"success": true,
			"message": "Status updated",
		})

	default:
		slog.Warn("Unknown bot status", "status", req.Status)
		return c.JSON(fiber.Map{
			"success": true,
			"message": "Status acknowledged",
		})
	}
}
