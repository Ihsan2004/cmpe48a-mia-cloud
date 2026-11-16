package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"mia-website/backend/internal/pkg/apperror"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type BotHandler struct {
	profileService *services.ProfileService
	meetingRepo    repository.MeetingRepository
}

func NewBotHandler() *BotHandler {
	return &BotHandler{
		profileService: services.NewProfileService(),
		meetingRepo:    repository.NewMeetingRepository(),
	}
}

// JoinMeetingRequest represents the request to join a meeting with bot
type JoinMeetingRequest struct {
	MeetingURL string `json:"meeting_url"`
}

// JoinMeeting handles POST /api/meetings/:id/join
// Triggers the external bot API to join the meeting
func (h *BotHandler) JoinMeeting(c *fiber.Ctx) error {
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

	// Get user profile for bot name/language
	profile, err := h.profileService.GetProfile(userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Profil bilgileri alınamadı."})
	}

	// Parse request body
	var req JoinMeetingRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	if req.MeetingURL == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Toplantı bağlantısı gereklidir.",
		})
	}

	// Block force-join if bot is actively recording — prevent accidental disruption
	currentStatus, _ := h.meetingRepo.GetBotStatus(c.Context(), meetingID)
	if currentStatus == "recording" {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": "Asistan şu anda kayıt yapıyor. Kayıt sırasında yeniden gönderilemez.",
		})
	}

	// Get external bot API URL from environment
	botAPIURL := os.Getenv("EXTERNAL_BOT_API_URL")
	if botAPIURL == "" {
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
	slog.Info("Profile loaded", "bot_name", botName, "language", botLanguage, "request_id", c.Locals("requestID"))

	// Prepare request to external bot API — manual joins use force to replace stuck containers
	botRequest := map[string]interface{}{
		"meeting_url": req.MeetingURL,
		"bot_name":    botName,
		"meeting_id":  meetingID.String(),
		"force":       true,
	}
	// Only send language if explicitly set (not "auto") — ElevenLabs auto-detects when omitted
	if botLanguage != "" && botLanguage != "auto" {
		botRequest["language"] = botLanguage
	}

	slog.Info("Sending bot join request (force)", "bot_name", botName, "meeting_url", req.MeetingURL, "request_id", c.Locals("requestID"))

	jsonData, err := json.Marshal(botRequest)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Asistan isteği hazırlanamadı.",
		})
	}

	slog.Debug("Bot API payload", "payload", string(jsonData), "request_id", c.Locals("requestID"))

	// Reset bot_status BEFORE calling bot API — prevents stale events from old container overriding
	if err := h.meetingRepo.UpdateBotStatusConnecting(c.Context(), meetingID); err != nil {
		slog.Warn("Failed to reset bot status before force join", "error", err, "meeting_id", meetingID, "request_id", c.Locals("requestID"))
	}

	// Call external bot API
	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	fullURL := fmt.Sprintf("%s/meetings/join", botAPIURL)
	slog.Debug("Bot API URL", "url", fullURL, "request_id", c.Locals("requestID"))

	req2, err := http.NewRequest("POST", fullURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return apperror.Send(c, apperror.Internal("Asistan isteği oluşturulamadı.", err), "create_bot_request")
	}
	req2.Header.Set("Content-Type", "application/json")
	if botAPIKey := os.Getenv("EXTERNAL_BOT_API_KEY"); botAPIKey != "" {
		req2.Header.Set("x-bot-api-key", botAPIKey)
	}

	resp, err := client.Do(req2)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway(ErrBotConnectionFailed, err), "connect_bot_api")
	}
	defer resp.Body.Close()

	// Parse bot API response
	var botResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&botResponse); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Asistan yanıtı işlenemedi.",
		})
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.Status(resp.StatusCode).JSON(botResponse)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"message":    "Bot join request sent",
		"meeting_id": meetingID.String(),
		"bot_status": "connecting",
		"details":    botResponse,
	})
}

// ToggleBot handles PATCH /api/meetings/:id/bot
// Allows user to enable/disable automatic bot joining
func (h *BotHandler) ToggleBot(c *fiber.Ctx) error {
	// Get user ID from context
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Oturum bilgisi bulunamadı. Lütfen tekrar giriş yapın.",
		})
	}

	if _, err := uuid.Parse(userIDStr); err != nil {
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

	// Parse request body
	var req struct {
		AutoBotEnabled *bool `json:"auto_bot_enabled"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz istek. Lütfen bilgileri kontrol edin.",
		})
	}

	if req.AutoBotEnabled == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Lütfen bot ayarını belirtin.",
		})
	}

	// Update auto_bot_enabled
	err = h.meetingRepo.UpdateAutoBotEnabled(c.Context(), meetingID, *req.AutoBotEnabled)
	if err != nil {
		return apperror.SendDBError(c, err, "Bot tercihi güncellenemedi.", "update_bot_preference")
	}

	status := "enabled"
	if !*req.AutoBotEnabled {
		status = "disabled"
	}

	return c.JSON(fiber.Map{
		"message":          fmt.Sprintf("Automatic bot joining %s", status),
		"meeting_id":       meetingID.String(),
		"auto_bot_enabled": *req.AutoBotEnabled,
	})
}

// Heartbeat handles POST /api/meetings/:id/heartbeat
// Updates last_heartbeat_at for watchdog
func (h *BotHandler) Heartbeat(c *fiber.Ctx) error {
	// Parse meeting ID
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	// Update last_heartbeat_at
	rows, err := h.meetingRepo.UpdateMeetingHeartbeat(c.Context(), meetingID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update heartbeat"})
	}
	if rows == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Meeting not found"})
	}

	return c.JSON(fiber.Map{"status": "ok", "timestamp": time.Now()})
}

// BotEventRequest represents the webhook payload from bot
type BotEventRequest struct {
	Event  string                 `json:"event"`  // joined, failed, completed
	Reason string                 `json:"reason"` // for failures
	Meta   map[string]interface{} `json:"metadata"`
}

// HandleBotEvent handles POST /api/meetings/:id/events
// Webhook for bot status updates
func (h *BotHandler) HandleBotEvent(c *fiber.Ctx) error {
	meetingIDStr := c.Params("id")
	meetingID, err := uuid.Parse(meetingIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid meeting ID"})
	}

	var req BotEventRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	rows, err := h.meetingRepo.UpdateBotEvent(c.Context(), meetingID, req.Event)
	if err != nil {
		if err.Error() == "unknown bot event: "+req.Event {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Unknown event"})
		}
		slog.Error("Failed to update bot status for event", "event", req.Event, "error", err, "meeting_id", meetingID, "request_id", c.Locals("requestID"))
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Database update failed"})
	}

	// For completed event, check if we actually updated the row (won the race)
	if req.Event == "completed" {
		if rows > 0 {
			slog.Info("Bot signal completed meeting", "meeting_id", meetingID, "request_id", c.Locals("requestID"))
		} else {
			slog.Warn("Bot signal ignored (already completed)", "meeting_id", meetingID, "request_id", c.Locals("requestID"))
		}
	}

	return c.JSON(fiber.Map{"status": "updated", "new_state": req.Event})
}
