package services

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

type CalendarService struct{}

func NewCalendarService() *CalendarService {
	return &CalendarService{}
}

type calendarOAuthConfig struct {
	name   string
	config *oauth2.Config
}

func isGoogleReconnectRequiredError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "invalid_grant") ||
		strings.Contains(errStr, "token has been expired") ||
		strings.Contains(errStr, "token has been expired or revoked") ||
		strings.Contains(errStr, "invalid_token") ||
		strings.Contains(errStr, "insufficientpermissions") ||
		strings.Contains(errStr, "access_token_scope_insufficient") ||
		strings.Contains(errStr, "insufficient permission")
}

// ExtractMeetingURL tries to find a meeting URL from various fields in a Google Calendar event
// Only returns actual meeting URLs (Google Meet, Zoom, Teams, etc.), not calendar event links
func (s *CalendarService) ExtractMeetingURL(event *calendar.Event) *string {
	// Check conferenceData (Google Meet links) - this is the most reliable indicator
	if event.ConferenceData != nil && len(event.ConferenceData.EntryPoints) > 0 {
		for _, entry := range event.ConferenceData.EntryPoints {
			if entry.EntryPointType == "video" && entry.Uri != "" {
				return &entry.Uri
			}
		}
	}

	// Check location field for meeting URLs
	if event.Location != "" {
		url := extractURLFromText(event.Location)
		if url != nil {
			return url
		}
	}

	// Check description field for meeting URLs
	if event.Description != "" {
		url := extractURLFromText(event.Description)
		if url != nil {
			return url
		}
	}

	// Do NOT use htmlLink as fallback - it's just a Google Calendar event link, not a meeting URL
	// Only return actual meeting URLs found above
	return nil
}

// extractURLFromText finds actual meeting URLs in text (Google Meet, Zoom, Teams, etc.)
func extractURLFromText(text string) *string {
	// Pattern for common meeting URLs - be specific to avoid false positives
	patterns := []string{
		// Google Meet
		`https?://meet\.google\.com/[a-z-]+`,
		`https?://meet\.google\.com/[a-z]+-[a-z]+-[a-z]+`,
		// Zoom - improved patterns to catch all formats
		`https?://(?:[a-z0-9-]+\.)?zoom\.us/j/\d+(?:\?[^\s<>"']*)?`,
		`https?://(?:[a-z0-9-]+\.)?zoom\.us/my/[^\s<>"']+`,
		`https?://(?:[a-z0-9-]+\.)?zoom\.us/webinar/[^\s<>"']+`,
		`https?://(?:[a-z0-9-]+\.)?zoom\.us/s/\d+(?:\?[^\s<>"']*)?`,
		`https?://zoom\.us/j/\d+(?:\?[^\s<>"']*)?`,
		// Microsoft Teams - improved patterns
		`https?://teams\.microsoft\.com/l/meetup-join/[^\s<>"']+`,
		`https?://teams\.microsoft\.com/meet/[^\s<>"']+`,
		`https?://teams\.microsoft\.com/l/meeting/[^\s<>"']+`,
		// Webex
		`https?://[^\s<>"']+\.webex\.com/[^\s<>"']+`,
		`https?://webex\.com/[^\s<>"']+`,
		// GoToMeeting
		`https?://(?:[a-z]+\.)?gotomeeting\.com/[^\s<>"']+`,
		// BlueJeans
		`https?://(?:[a-z]+\.)?bluejeans\.com/[^\s<>"']+`,
		// Jitsi
		`https?://(?:[a-z]+\.)?jitsi\.meet/[^\s<>"']+`,
		`https?://meet\.jitsi\.si/[^\s<>"']+`,
		// Generic meeting URL pattern (fallback for other services)
		`https?://[^\s<>"']+\.(?:zoom|teams|webex|gotomeeting|bluejeans|jitsi)[^\s<>"']*`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(`(?i)` + pattern) // Case insensitive
		matches := re.FindString(text)
		if matches != "" {
			return &matches
		}
	}

	return nil
}

// getOAuthConfig returns the OAuth2 config for Google Calendar
func (s *CalendarService) getOAuthConfig() (*oauth2.Config, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURI := os.Getenv("GOOGLE_REDIRECT_URI")

	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("Google OAuth not configured")
	}

	// Use frontend URL for callback if not set
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		slog.Warn("FRONTEND_URL not set, OAuth redirect may not work correctly")
		frontendURL = "" // Don't use localhost fallback in production
	}

	if redirectURI == "" {
		if frontendURL == "" {
			return nil, fmt.Errorf("FRONTEND_URL or GOOGLE_REDIRECT_URI must be set")
		}
		redirectURI = frontendURL + "/auth/callback"
	}

	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes: []string{
			"https://www.googleapis.com/auth/calendar",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}

	return config, nil
}

func (s *CalendarService) getRefreshOAuthConfigs() ([]calendarOAuthConfig, error) {
	webConfig, err := s.getOAuthConfig()
	if err != nil {
		return nil, err
	}

	seenClientIDs := map[string]bool{webConfig.ClientID: true}
	configs := []calendarOAuthConfig{
		{
			name:   "web",
			config: webConfig,
		},
	}

	addFallback := func(name, clientID, clientSecret string) {
		clientID = strings.TrimSpace(clientID)
		if clientID == "" || seenClientIDs[clientID] {
			return
		}
		seenClientIDs[clientID] = true

		fallbackConfig := *webConfig
		fallbackConfig.ClientID = clientID
		fallbackConfig.ClientSecret = strings.TrimSpace(clientSecret)
		configs = append(configs, calendarOAuthConfig{
			name:   name,
			config: &fallbackConfig,
		})
	}

	addFallback("desktop",
		os.Getenv("GOOGLE_DESKTOP_CLIENT_ID"),
		os.Getenv("GOOGLE_DESKTOP_CLIENT_SECRET"),
	)
	addFallback("legacy",
		os.Getenv("GOOGLE_LEGACY_CLIENT_ID"),
		os.Getenv("GOOGLE_LEGACY_CLIENT_SECRET"),
	)

	return configs, nil
}

// GetServiceWithRefreshToken exposes calendar service creation for handlers
func (s *CalendarService) GetServiceWithRefreshToken(userID uuid.UUID) (*calendar.Service, error) {
	return s.getCalendarServiceWithRefreshToken(userID)
}

// getCalendarServiceWithRefreshToken creates a calendar service using refresh token from DB.
// Forces an immediate refresh by setting token expiry to the past.
func (s *CalendarService) getCalendarServiceWithRefreshToken(userID uuid.UUID) (*calendar.Service, error) {
	ctx := context.Background()

	// Get refresh token from database
	profileService := NewProfileService()
	refreshToken, err := profileService.GetGoogleRefreshToken(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}
	if refreshToken == nil || *refreshToken == "" {
		return nil, fmt.Errorf("User not connected to Google")
	}

	// Google refresh tokens are bound to the OAuth client that minted them. Web
	// users use GOOGLE_CLIENT_ID/SECRET, while desktop sign-in may save tokens
	// minted by GOOGLE_DESKTOP_CLIENT_ID. Existing users may also carry refresh
	// tokens from the pre-Syncrun Google OAuth client.
	configs, err := s.getRefreshOAuthConfigs()
	if err != nil {
		return nil, err
	}

	var ts oauth2.TokenSource
	var lastErr error

	for _, candidate := range configs {
		// Build token and force it to look expired so refresh happens immediately
		token := &oauth2.Token{
			RefreshToken: *refreshToken,
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		// TokenSource manages refresh using client credentials + refresh token
		candidateTS := candidate.config.TokenSource(ctx, token)

		// Try to get a token to validate the refresh token early
		// This will trigger a refresh if needed and catch errors early
		_, err = candidateTS.Token()
		if err == nil {
			ts = candidateTS
			if candidate.name != "web" {
				slog.Info("Google token refresh succeeded with alternate OAuth client",
					"user_id", userID,
					"oauth_client", candidate.name,
				)
			}
			break
		}

		lastErr = err
		if candidate.name == "web" && len(configs) > 1 {
			slog.Warn("Google token refresh failed with web OAuth client, trying fallback",
				"user_id", userID,
				"fallback_client", configs[1].name,
				"error", err,
			)
		}
	}

	if ts == nil {
		err = lastErr
		if isGoogleReconnectRequiredError(err) {
			// Token is invalid, disconnect the user
			slog.Warn("Google token invalid, disconnecting user", "user_id", userID)
			if err := profileService.DisconnectGoogleCalendar(userID); err != nil {
				slog.Error("failed to disconnect user", "user_id", userID, "error", err)
			}

			return nil, fmt.Errorf("Google Calendar bağlantısı süresi dolmuş veya geçersiz. Lütfen Ayarlar sayfasından Google Takvimi yeniden bağlayın")
		}
		return nil, fmt.Errorf("failed to refresh Google token: %w", err)
	}

	// HTTP client that auto-refreshes
	httpClient := oauth2.NewClient(ctx, ts)

	// Calendar service with refreshed client
	service, err := calendar.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create calendar service: %w", err)
	}

	return service, nil
}

// GetCalendarServiceForUser exposes a calendar service using the user's refresh token
func (s *CalendarService) GetCalendarServiceForUser(userID uuid.UUID) (*calendar.Service, error) {
	return s.getCalendarServiceWithRefreshToken(userID)
}

// FetchUpcomingEvents fetches upcoming events from Google Calendar
// If userID is provided, it uses refresh token from DB (offline access)
// Otherwise, it uses the provided accessToken (legacy method)
func (s *CalendarService) FetchUpcomingEvents(userID *uuid.UUID, accessToken string, maxResults int) ([]*calendar.Event, error) {
	ctx := context.Background()
	var service *calendar.Service
	var err error

	// Prefer refresh token method if userID is provided
	if userID != nil {
		service, err = s.getCalendarServiceWithRefreshToken(*userID)
		if err != nil {
			// Fallback to access token if refresh token fails
			if accessToken == "" {
				return nil, fmt.Errorf("failed to get calendar service: %w", err)
			}
			// Continue with access token method below
		}
	}

	// Fallback to access token method if refresh token method wasn't used
	if service == nil {
		// Create OAuth2 token source
		tokenSource := &StaticTokenSource{AccessToken: accessToken}

		// Create OAuth2 client with the access token
		client := option.WithTokenSource(tokenSource)

		service, err = calendar.NewService(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("failed to create calendar service: %w", err)
		}
	}

	// Get window: last 30 days to next 30 days
	now := time.Now()
	timeMin := now.AddDate(0, 0, -30).Format(time.RFC3339)
	timeMax := now.AddDate(0, 0, 30).Format(time.RFC3339)

	// Fetch events from primary calendar
	events, err := service.Events.List("primary").
		TimeMin(timeMin).
		TimeMax(timeMax).
		MaxResults(int64(2500)). // Fetch maximum allowed per page to avoid missing events
		SingleEvents(true).
		OrderBy("startTime").
		Do()

	if err != nil {
		// Check for OAuth token errors - these indicate refresh token is invalid/expired
		if isGoogleReconnectRequiredError(err) || strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			return nil, fmt.Errorf("Google Calendar bağlantısı süresi dolmuş veya geçersiz. Lütfen Ayarlar sayfasından Google Takvimi yeniden bağlayın")
		}
		return nil, fmt.Errorf("failed to list events: %w", err)
	}

	return events.Items, nil
}

// CreateEvent creates a new event in Google Calendar
// If userID is provided, it uses refresh token from DB (offline access)
// Otherwise, it uses the provided accessToken (legacy method)
func (s *CalendarService) CreateEvent(userID *uuid.UUID, accessToken string, title string, startTime, endTime time.Time, description, location *string, attendees []string, createGoogleMeet bool) (*calendar.Event, error) {
	ctx := context.Background()
	var service *calendar.Service
	var err error

	// Prefer refresh token method if userID is provided
	if userID != nil {
		service, err = s.getCalendarServiceWithRefreshToken(*userID)
		if err != nil {
			// Fallback to access token if refresh token fails
			if accessToken == "" {
				return nil, fmt.Errorf("failed to get calendar service: %w", err)
			}
			// Continue with access token method below
		}
	}

	// Fallback to access token method if refresh token method wasn't used
	if service == nil {
		// Create OAuth2 token source
		tokenSource := &StaticTokenSource{AccessToken: accessToken}

		// Create OAuth2 client with the access token
		client := option.WithTokenSource(tokenSource)

		service, err = calendar.NewService(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("failed to create calendar service: %w", err)
		}
	}

	// Create event object
	event := &calendar.Event{
		Summary: title,
		Start: &calendar.EventDateTime{
			DateTime: startTime.Format(time.RFC3339),
			TimeZone: "UTC",
		},
		End: &calendar.EventDateTime{
			DateTime: endTime.Format(time.RFC3339),
			TimeZone: "UTC",
		},
	}

	// Add description if provided
	if description != nil && *description != "" {
		event.Description = *description
	}

	// Add location if provided
	if location != nil && *location != "" {
		event.Location = *location
	}

	// Add attendees if provided
	if len(attendees) > 0 {
		eventAttendees := make([]*calendar.EventAttendee, 0, len(attendees))
		for _, email := range attendees {
			if email != "" {
				eventAttendees = append(eventAttendees, &calendar.EventAttendee{
					Email: email,
				})
			}
		}
		if len(eventAttendees) > 0 {
			event.Attendees = eventAttendees
		}
	}

	// Create Google Meet link if requested
	if createGoogleMeet {
		event.ConferenceData = &calendar.ConferenceData{
			CreateRequest: &calendar.CreateConferenceRequest{
				RequestId: fmt.Sprintf("%d", time.Now().UnixNano()),
				ConferenceSolutionKey: &calendar.ConferenceSolutionKey{
					Type: "hangoutsMeet",
				},
			},
		}
	}

	// Insert event into primary calendar
	insertCall := service.Events.Insert("primary", event).
		ConferenceDataVersion(1) // Required for creating Google Meet links

	// Send notifications to attendees if there are any
	if len(attendees) > 0 {
		insertCall = insertCall.SendUpdates("all")
	}

	createdEvent, err := insertCall.Do()

	if err != nil {
		return nil, fmt.Errorf("failed to create calendar event: %w", err)
	}

	return createdEvent, nil
}

// UpdateEvent patches an existing Google Calendar event with only the provided fields.
// calendarFields may contain: "title", "start_time", "end_time", "description", "location", "attendees".
// sendUpdates controls attendee notification: "all" (notify) or "none" (silent).
func (s *CalendarService) UpdateEvent(userID uuid.UUID, googleEventID string, calendarFields map[string]interface{}, sendUpdates string) error {
	service, err := s.getCalendarServiceWithRefreshToken(userID)
	if err != nil {
		return fmt.Errorf("failed to init calendar client: %w", err)
	}

	// Build partial event — only set fields present in calendarFields
	event := &calendar.Event{}

	if title, ok := calendarFields["title"].(string); ok {
		event.Summary = title
	}
	if startTime, ok := calendarFields["start_time"].(time.Time); ok {
		event.Start = &calendar.EventDateTime{
			DateTime: startTime.Format(time.RFC3339),
			TimeZone: "UTC",
		}
	}
	if endTime, ok := calendarFields["end_time"].(time.Time); ok {
		event.End = &calendar.EventDateTime{
			DateTime: endTime.Format(time.RFC3339),
			TimeZone: "UTC",
		}
	}
	if desc, ok := calendarFields["description"].(string); ok {
		event.Description = desc
	}
	if loc, ok := calendarFields["location"].(string); ok {
		event.Location = loc
	}
	if attendeeEmails, ok := calendarFields["attendees"].([]string); ok {
		eventAttendees := make([]*calendar.EventAttendee, 0, len(attendeeEmails))
		for _, email := range attendeeEmails {
			if email != "" {
				eventAttendees = append(eventAttendees, &calendar.EventAttendee{Email: email})
			}
		}
		event.Attendees = eventAttendees
	}

	// Validate sendUpdates parameter
	if sendUpdates != "all" {
		sendUpdates = "none"
	}

	_, err = service.Events.Patch("primary", googleEventID, event).
		SendUpdates(sendUpdates).
		Do()
	if err != nil {
		return fmt.Errorf("failed to patch Google Calendar event: %w", err)
	}

	return nil
}

// StaticTokenSource implements oauth2.TokenSource for a static token
type StaticTokenSource struct {
	AccessToken string
}

func (s *StaticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{
		AccessToken: s.AccessToken,
		TokenType:   "Bearer",
	}, nil
}
