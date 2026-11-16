package services

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/api/calendar/v3"
)

// CalendarSubscription represents a Google Calendar push notification subscription
type CalendarSubscription struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"user_id"`
	ChannelID  string    `json:"channel_id"`
	ResourceID string    `json:"resource_id"`
	Expiration time.Time `json:"expiration"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CalendarWatchService handles Google Calendar push notifications (webhooks)
type CalendarWatchService struct {
	calendarService *CalendarService
	db              *pgxpool.Pool
}

// NewCalendarWatchService creates a new CalendarWatchService
func NewCalendarWatchService() *CalendarWatchService {
	return &CalendarWatchService{
		calendarService: NewCalendarService(),
		db:              repository.DB,
	}
}

func normalizeWebhookBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(base, "/api")
}

func deriveWebhookBaseURLFromFrontend(frontendURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(frontendURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return frontendURL
	}

	if strings.EqualFold(parsed.Hostname(), "console.syncrun.net") {
		parsed.Host = "api.syncrun.net"
	}

	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// GetWebhookURL returns the webhook URL for Google Calendar notifications
// In local development, you need to use ngrok or similar to expose localhost to the internet
// Example: ngrok http 8080 → https://abc123.ngrok.io
// Then set WEBHOOK_BASE_URL=https://abc123.ngrok.io in your .env file
func GetWebhookURL() string {
	webhookBaseURL := os.Getenv("WEBHOOK_BASE_URL")
	if webhookBaseURL == "" {
		// Fallback to production URL
		frontendURL := os.Getenv("FRONTEND_URL")
		if frontendURL != "" {
			webhookBaseURL = deriveWebhookBaseURLFromFrontend(frontendURL)
		} else {
			// LOCAL DEVELOPMENT NOTE:
			// Google Calendar webhooks require a publicly accessible HTTPS URL.
			// For local development, use ngrok or similar:
			//   1. Run: ngrok http 8080
			//   2. Copy the https URL (e.g., https://abc123.ngrok.io)
			//   3. Set WEBHOOK_BASE_URL=https://abc123.ngrok.io in .env
			//   4. Restart the backend server
			// In production, set FRONTEND_URL or WEBHOOK_BASE_URL in .env
			slog.Warn("WEBHOOK_BASE_URL and FRONTEND_URL not set, webhook URL may not work correctly")
			webhookBaseURL = "" // Don't use localhost fallback in production
		}
	}
	// Strip trailing /api or /api/ if present to avoid double /api/api/
	webhookBaseURL = normalizeWebhookBaseURL(webhookBaseURL)
	return webhookBaseURL + "/api/webhooks/google-calendar"
}

// WatchCalendar creates a push notification subscription for a user's calendar
// This tells Google to notify our webhook whenever events change
func (s *CalendarWatchService) WatchCalendar(userID uuid.UUID) (*CalendarSubscription, error) {
	// Get calendar service for this user (uses refresh token from DB)
	calService, err := s.calendarService.GetCalendarServiceForUser(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get calendar service: %w", err)
	}

	// Generate unique channel ID for this subscription
	channelID := uuid.New().String()

	// Get webhook URL
	webhookURL := GetWebhookURL()

	slog.Info("Creating calendar watch", "user_id", userID, "webhook_url", webhookURL, "channel_id", channelID)

	// Create watch request
	// Google Calendar push notifications expire after ~7 days
	channel := &calendar.Channel{
		Id:      channelID,
		Type:    "web_hook",
		Address: webhookURL,
		// Token is optional - we use channelID to identify the user
		Token: userID.String(), // Store userID in token for quick lookup
	}

	// Call Google Calendar API to set up the watch
	ctx := context.Background()
	watchCall := calService.Events.Watch("primary", channel)

	response, err := watchCall.Context(ctx).Do()
	if err != nil {
		if isGoogleReconnectRequiredError(err) {
			slog.Warn("Google calendar watch permission invalid, disconnecting user", "user_id", userID, "error", err)
			if disconnectErr := NewProfileService().DisconnectGoogleCalendar(userID); disconnectErr != nil {
				slog.Error("failed to disconnect user after calendar watch error", "user_id", userID, "error", disconnectErr)
			}
		}
		return nil, fmt.Errorf("failed to create calendar watch: %w", err)
	}

	// Parse expiration time (Google returns milliseconds since epoch)
	expirationMs := response.Expiration
	expiration := time.UnixMilli(expirationMs)

	slog.Info("Calendar watch response received", "resource_id", response.ResourceId, "expiration", expiration.Format(time.RFC3339))

	// Save subscription to database
	subscription := &CalendarSubscription{
		ID:         uuid.New(),
		UserID:     userID,
		ChannelID:  channelID,
		ResourceID: response.ResourceId,
		Expiration: expiration,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err = s.saveSubscription(subscription)
	if err != nil {
		// Try to stop the watch if we can't save it
		s.stopWatch(calService, channelID, response.ResourceId)
		return nil, fmt.Errorf("failed to save subscription: %w", err)
	}

	slog.Info("Calendar watch created successfully", "user_id", userID)
	return subscription, nil
}

// StopWatchForUser stops the calendar watch for a user
func (s *CalendarWatchService) StopWatchForUser(userID uuid.UUID) error {
	// Get existing subscription
	subscription, err := s.getSubscriptionByUserID(userID)
	if err != nil {
		return nil // No subscription to stop
	}

	// Get calendar service
	calService, err := s.calendarService.GetCalendarServiceForUser(userID)
	if err != nil {
		return fmt.Errorf("failed to get calendar service: %w", err)
	}

	// Stop the watch
	err = s.stopWatch(calService, subscription.ChannelID, subscription.ResourceID)
	if err != nil {
		slog.Warn("Failed to stop calendar watch", "error", err)
		// Continue to delete from DB anyway
	}

	// Delete from database
	return s.deleteSubscription(subscription.ID)
}

// stopWatch stops a calendar watch on Google's side
func (s *CalendarWatchService) stopWatch(calService *calendar.Service, channelID, resourceID string) error {
	ctx := context.Background()
	channel := &calendar.Channel{
		Id:         channelID,
		ResourceId: resourceID,
	}

	return calService.Channels.Stop(channel).Context(ctx).Do()
}

// GetSubscriptionByChannelID finds a subscription by its channel ID (used in webhook)
func (s *CalendarWatchService) GetSubscriptionByChannelID(channelID string) (*CalendarSubscription, error) {
	query := `
		SELECT id, user_id, channel_id, resource_id, expiration, created_at, updated_at
		FROM calendar_subscriptions
		WHERE channel_id = $1
	`

	var sub CalendarSubscription
	err := s.db.QueryRow(context.Background(), query, channelID).Scan(
		&sub.ID, &sub.UserID, &sub.ChannelID, &sub.ResourceID,
		&sub.Expiration, &sub.CreatedAt, &sub.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("subscription not found for channel %s", channelID)
		}
		return nil, err
	}

	return &sub, nil
}

// getSubscriptionByUserID finds a subscription by user ID
func (s *CalendarWatchService) getSubscriptionByUserID(userID uuid.UUID) (*CalendarSubscription, error) {
	query := `
		SELECT id, user_id, channel_id, resource_id, expiration, created_at, updated_at
		FROM calendar_subscriptions
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`

	var sub CalendarSubscription
	err := s.db.QueryRow(context.Background(), query, userID).Scan(
		&sub.ID, &sub.UserID, &sub.ChannelID, &sub.ResourceID,
		&sub.Expiration, &sub.CreatedAt, &sub.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("no subscription found for user %s", userID)
		}
		return nil, err
	}

	return &sub, nil
}

// HasActiveSubscription checks if user has an active (non-expired) subscription
func (s *CalendarWatchService) HasActiveSubscription(userID uuid.UUID) (bool, error) {
	sub, err := s.getSubscriptionByUserID(userID)
	if err != nil {
		return false, nil // No subscription
	}

	// Check if subscription is still valid (with 1 hour buffer)
	return sub.Expiration.After(time.Now().Add(1 * time.Hour)), nil
}

// RenewExpiringSubscriptions renews subscriptions that are about to expire
// This should be called by a background job (cron)
func (s *CalendarWatchService) RenewExpiringSubscriptions() error {
	// Find subscriptions expiring in the next 24 hours
	query := `
		SELECT id, user_id, channel_id, resource_id, expiration
		FROM calendar_subscriptions
		WHERE expiration < $1 AND expiration > NOW()
	`

	renewBefore := time.Now().Add(24 * time.Hour)
	rows, err := s.db.Query(context.Background(), query, renewBefore)
	if err != nil {
		return err
	}
	defer rows.Close()

	var toRenew []CalendarSubscription
	for rows.Next() {
		var sub CalendarSubscription
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.ChannelID, &sub.ResourceID, &sub.Expiration); err != nil {
			continue
		}
		toRenew = append(toRenew, sub)
	}

	slog.Info("Found calendar subscriptions to renew", "count", len(toRenew))

	for _, sub := range toRenew {
		// Stop old subscription
		s.StopWatchForUser(sub.UserID)

		// Create new subscription
		_, err := s.WatchCalendar(sub.UserID)
		if err != nil {
			slog.Error("Failed to renew subscription", "user_id", sub.UserID, "error", err)
			continue
		}
		slog.Info("Renewed subscription", "user_id", sub.UserID)
	}

	return s.EnsureWatchesForConnectedUsers(100)
}

// EnsureWatchesForConnectedUsers repairs users who have a Google refresh token
// but no active Calendar push subscription. This covers deployments where
// WEBHOOK_BASE_URL was previously wrong or unset.
func (s *CalendarWatchService) EnsureWatchesForConnectedUsers(limit int) error {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT p.id
		FROM profiles p
		LEFT JOIN LATERAL (
			SELECT expiration
			FROM calendar_subscriptions cs
			WHERE cs.user_id = p.id
			ORDER BY created_at DESC
			LIMIT 1
		) cs ON TRUE
		WHERE p.google_refresh_token IS NOT NULL
		  AND p.google_refresh_token <> ''
		  AND COALESCE(p.is_calendar_connected, FALSE) = TRUE
		  AND (cs.expiration IS NULL OR cs.expiration <= NOW() + INTERVAL '1 hour')
		LIMIT $1
	`

	rows, err := s.db.Query(context.Background(), query, limit)
	if err != nil {
		return err
	}
	defer rows.Close()

	var userIDs []uuid.UUID
	for rows.Next() {
		var userID uuid.UUID
		if err := rows.Scan(&userID); err != nil {
			slog.Warn("Failed to scan calendar watch repair candidate", "error", err)
			continue
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	slog.Info("Found connected users needing calendar watch repair", "count", len(userIDs))

	for _, userID := range userIDs {
		if _, err := s.WatchCalendar(userID); err != nil {
			slog.Error("Failed to repair calendar watch", "user_id", userID, "error", err)
			continue
		}
		slog.Info("Repaired calendar watch", "user_id", userID)
	}

	return nil
}

// saveSubscription saves or updates a subscription in the database
func (s *CalendarWatchService) saveSubscription(sub *CalendarSubscription) error {
	ctx := context.Background()

	// First, delete any existing subscription for this user
	deleteQuery := `DELETE FROM calendar_subscriptions WHERE user_id = $1`
	_, _ = s.db.Exec(ctx, deleteQuery, sub.UserID)

	// Insert new subscription
	query := `
		INSERT INTO calendar_subscriptions (id, user_id, channel_id, resource_id, expiration, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := s.db.Exec(ctx, query,
		sub.ID, sub.UserID, sub.ChannelID, sub.ResourceID,
		sub.Expiration, sub.CreatedAt, sub.UpdatedAt,
	)
	return err
}

// deleteSubscription removes a subscription from the database
func (s *CalendarWatchService) deleteSubscription(subID uuid.UUID) error {
	query := `DELETE FROM calendar_subscriptions WHERE id = $1`
	_, err := s.db.Exec(context.Background(), query, subID)
	return err
}

// EnsureWatchForUser ensures a user has an active calendar watch
// Creates one if it doesn't exist or if it's expired/expiring soon
func (s *CalendarWatchService) EnsureWatchForUser(userID uuid.UUID) error {
	hasActive, err := s.HasActiveSubscription(userID)
	if err != nil {
		return err
	}

	if hasActive {
		slog.Info("User already has active calendar subscription", "user_id", userID)
		return nil
	}

	// Create new subscription
	_, err = s.WatchCalendar(userID)
	return err
}
