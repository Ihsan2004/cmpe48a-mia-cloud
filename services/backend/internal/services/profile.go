package services

import (
	"context"
	"fmt"
	"log/slog"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

type ProfileService struct{}

func NewProfileService() *ProfileService {
	return &ProfileService{}
}

// GetProfile retrieves a user's profile
func (s *ProfileService) GetProfile(userID uuid.UUID) (*models.Profile, error) {
	query := `
		SELECT 
			p.id,
			COALESCE(p.email, u.email) AS email,
			p.full_name,
			p.job_title,
			p.company,
			p.timezone,
			p.default_meeting_duration,
			p.work_start_hour,
			p.work_end_hour,
			COALESCE(p.bot_name, 'MIA Assistant') AS bot_name,
			COALESCE(p.bot_language, 'auto') AS bot_language,
			p.google_refresh_token,
			COALESCE(p.is_calendar_connected, FALSE) AS is_calendar_connected,
			p.google_email,
			COALESCE(p.interface_language, 'tr') AS interface_language,
			COALESCE(p.brief_lead_time_minutes, 10) AS brief_lead_time_minutes,
			COALESCE(p.recording_method, 'desktop') AS recording_method,
			COALESCE(p.brief_deliver_via_email, false) AS brief_deliver_via_email,
			COALESCE(p.brief_deliver_via_slack, false) AS brief_deliver_via_slack,
			COALESCE(p.teams_onboarding_status, 'pending') AS teams_onboarding_status,
			p.email_unsubscribed_at,
			p.updated_at
		FROM profiles p
		LEFT JOIN public.users u ON u.id = p.id
		WHERE p.id = $1
	`

	var profile models.Profile
	err := repository.DB.QueryRow(context.Background(), query, userID).Scan(
		&profile.ID,
		&profile.Email,
		&profile.FullName,
		&profile.JobTitle,
		&profile.Company,
		&profile.Timezone,
		&profile.DefaultMeetingDuration,
		&profile.WorkStartHour,
		&profile.WorkEndHour,
		&profile.BotName,
		&profile.BotLanguage,
		&profile.GoogleRefreshToken,
		&profile.IsCalendarConnected,
		&profile.GoogleEmail,
		&profile.InterfaceLanguage,
		&profile.BriefLeadTimeMinutes,
		&profile.RecordingMethod,
		&profile.BriefDeliverViaEmail,
		&profile.BriefDeliverViaSlack,
		&profile.TeamsOnboardingStatus,
		&profile.EmailUnsubscribedAt,
		&profile.UpdatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}

	return &profile, nil
}

// UpdateProfile updates a user's profile
func (s *ProfileService) UpdateProfile(
	userID uuid.UUID,
	fullName string,
	jobTitle *string,
	company *string,
	timezone string,
	defaultDuration int,
	workStart *string,
	workEnd *string,
	botName string,
	botLanguage string,
	interfaceLanguage string,
	briefLeadTimeMinutes *int,
	recordingMethod string, // "" = leave column untouched; "bot"/"desktop" to set
) error {
	// Default bot name if empty
	if botName == "" {
		botName = "MIA Assistant"
	}
	// Default bot language if empty
	if botLanguage == "" {
		botLanguage = "auto"
	}
	// Default interface language if empty
	if interfaceLanguage == "" {
		interfaceLanguage = "tr"
	}

	// recording_method is nullable-update via NULLIF('',...) — passing ""
	// as the parameter keeps the existing column value intact. Caller
	// already validated the enum so we don't re-check here.
	var recordingMethodParam *string
	if recordingMethod != "" {
		recordingMethodParam = &recordingMethod
	}

	query := `
		UPDATE profiles
		SET full_name = $1,
			job_title = $2,
			company = $3,
			timezone = $4,
			default_meeting_duration = $5,
			work_start_hour = $6,
			work_end_hour = $7,
			bot_name = $8,
			bot_language = $9,
			interface_language = $10,
			brief_lead_time_minutes = COALESCE($12, brief_lead_time_minutes),
			recording_method = COALESCE($13, recording_method),
			updated_at = NOW()
		WHERE id = $11
	`

	_, err := repository.DB.Exec(context.Background(), query,
		fullName,
		jobTitle,
		company,
		timezone,
		defaultDuration,
		workStart,
		workEnd,
		botName,
		botLanguage,
		interfaceLanguage,
		userID,
		briefLeadTimeMinutes,
		recordingMethodParam,
	)
	if err != nil {
		return fmt.Errorf("failed to update profile: %w", err)
	}

	// Also update the users table for consistency
	userQuery := `UPDATE users SET full_name = $1, updated_at = NOW() WHERE id = $2`
	_, err = repository.DB.Exec(context.Background(), userQuery, fullName, userID)
	if err != nil {
		slog.Warn("failed to update users table during profile update", "error", err)
		// Don't fail the whole operation if users table update fails
	}

	return nil
}

// EnsureProfileExists creates a profile if it doesn't exist
func (s *ProfileService) EnsureProfileExists(userID uuid.UUID, email string, fullName string) error {
	query := `
		INSERT INTO profiles (id, full_name, email, timezone, default_meeting_duration, updated_at)
		VALUES ($1, COALESCE(NULLIF($2, ''), $3), $3, 'UTC', 30, NOW())
		ON CONFLICT (id) DO NOTHING
	`

	// Use email as fallback for full_name if not provided
	if fullName == "" {
		fullName = email
	}

	_, err := repository.DB.Exec(context.Background(), query, userID, fullName, email)
	if err != nil {
		return fmt.Errorf("failed to ensure profile exists: %w", err)
	}

	return nil
}

// SaveGoogleRefreshToken saves the Google refresh token for a user
func (s *ProfileService) SaveGoogleRefreshToken(userID uuid.UUID, refreshToken string) error {
	// First ensure profile exists
	query := `
		INSERT INTO profiles (id, full_name, timezone, default_meeting_duration, google_refresh_token, is_calendar_connected, updated_at)
		VALUES ($1, 'Google User', 'UTC', 30, $2, TRUE, NOW())
		ON CONFLICT (id) DO UPDATE SET
			google_refresh_token = $2,
			is_calendar_connected = TRUE,
			updated_at = NOW()
	`

	_, err := repository.DB.Exec(context.Background(), query, userID, refreshToken)
	if err != nil {
		return fmt.Errorf("failed to save refresh token: %w", err)
	}

	return nil
}

// SaveGoogleConnection saves Google refresh token and email for a user
func (s *ProfileService) SaveGoogleConnection(userID uuid.UUID, refreshToken string, googleEmail string) error {
	// Use UPSERT to create profile if it doesn't exist
	query := `
		INSERT INTO profiles (id, full_name, email, timezone, default_meeting_duration, google_refresh_token, google_email, is_calendar_connected, updated_at)
		VALUES ($1, COALESCE(NULLIF($3, ''), 'Google User'), $3, 'UTC', 30, $2, $3, TRUE, NOW())
		ON CONFLICT (id) DO UPDATE SET
			google_refresh_token = $2,
			google_email = $3,
			is_calendar_connected = TRUE,
			updated_at = NOW()
	`

	_, err := repository.DB.Exec(context.Background(), query, userID, refreshToken, googleEmail)
	if err != nil {
		return fmt.Errorf("failed to save google connection: %w", err)
	}

	return nil
}

// DisconnectGoogleCalendar removes the Google calendar connection
func (s *ProfileService) DisconnectGoogleCalendar(userID uuid.UUID) error {
	query := `
		UPDATE profiles
		SET google_refresh_token = NULL, 
			is_calendar_connected = FALSE,
			updated_at = NOW()
		WHERE id = $1
	`

	result, err := repository.DB.Exec(context.Background(), query, userID)
	if err != nil {
		return fmt.Errorf("failed to disconnect calendar: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("profile not found")
	}

	return nil
}

// GetGoogleRefreshToken retrieves the Google refresh token for a user
func (s *ProfileService) GetGoogleRefreshToken(userID uuid.UUID) (*string, error) {
	query := `
		SELECT google_refresh_token
		FROM profiles
		WHERE id = $1
	`

	var refreshToken *string
	err := repository.DB.QueryRow(context.Background(), query, userID).Scan(&refreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	return refreshToken, nil
}

// FindUserByGoogleEmail finds a user by their Google email (for account linking)
func (s *ProfileService) FindUserByGoogleEmail(googleEmail string) (*uuid.UUID, error) {
	query := `
		SELECT id
		FROM profiles
		WHERE google_email = $1
	`

	var userID uuid.UUID
	err := repository.DB.QueryRow(context.Background(), query, googleEmail).Scan(&userID)
	if err != nil {
		return nil, err // May be pgx.ErrNoRows which is expected
	}

	return &userID, nil
}

// UpdateNotificationPreferences toggles one or both of the brief-
// delivery channel flags. nil pointers are no-ops so a PATCH with only
// `brief_deliver_via_email` doesn't disturb the Slack toggle. When
// brief_deliver_via_email is flipped to true we intentionally clear
// email_unsubscribed_at — the user is explicitly re-opting in and we
// don't want a stale unsubscribe timestamp to silently block every
// future delivery.
func (s *ProfileService) UpdateNotificationPreferences(userID uuid.UUID, email, slack *bool) error {
	if userID == uuid.Nil {
		return fmt.Errorf("userID cannot be nil")
	}
	if email == nil && slack == nil {
		return nil
	}

	query := `
		UPDATE profiles
		SET brief_deliver_via_email = COALESCE($2, brief_deliver_via_email),
		    brief_deliver_via_slack = COALESCE($3, brief_deliver_via_slack),
		    email_unsubscribed_at = CASE
		        WHEN $2 = TRUE THEN NULL
		        ELSE email_unsubscribed_at
		    END,
		    updated_at = NOW()
		WHERE id = $1
	`
	if _, err := repository.DB.Exec(context.Background(), query, userID, email, slack); err != nil {
		return fmt.Errorf("failed to update notification preferences: %w", err)
	}
	return nil
}

// EmailForUser returns the Syncrun email for a user — first choice is
// profiles.email, falling back to users.email. Used by the Slack
// integration handler to look up the user on Slack's side via
// users.lookupByEmail during OAuth install.
func (s *ProfileService) EmailForUser(userID uuid.UUID) (string, error) {
	if userID == uuid.Nil {
		return "", fmt.Errorf("userID cannot be nil")
	}
	var email string
	err := repository.DB.QueryRow(context.Background(), `
		SELECT COALESCE(p.email, u.email, '') AS email
		FROM profiles p
		LEFT JOIN users u ON u.id = p.id
		WHERE p.id = $1
	`, userID).Scan(&email)
	if err != nil {
		return "", fmt.Errorf("failed to load user email: %w", err)
	}
	return email, nil
}

// SetEmailUnsubscribed flips profiles.email_unsubscribed_at to NOW().
// Idempotent — a second click on the unsubscribe link is a no-op,
// not an error, because the first write already durably opted the user
// out. Returns whether a state change actually occurred so the handler
// can tailor its user-facing copy.
func (s *ProfileService) SetEmailUnsubscribed(userID uuid.UUID) (changed bool, err error) {
	if userID == uuid.Nil {
		return false, fmt.Errorf("userID cannot be nil")
	}
	tag, err := repository.DB.Exec(context.Background(), `
		UPDATE profiles
		SET email_unsubscribed_at = NOW(),
		    brief_deliver_via_email = false,
		    updated_at = NOW()
		WHERE id = $1 AND email_unsubscribed_at IS NULL
	`, userID)
	if err != nil {
		return false, fmt.Errorf("failed to set unsubscribe: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClearEmailUnsubscribe undoes SetEmailUnsubscribed. Triggered from the
// dashboard when the user manually re-enables brief emails. We intentionally
// DO NOT flip brief_deliver_via_email back to true — the user must also
// re-check the toggle on /settings/notifications. This keeps opt-in
// consent explicit even when restoring state.
func (s *ProfileService) ClearEmailUnsubscribe(userID uuid.UUID) error {
	if userID == uuid.Nil {
		return fmt.Errorf("userID cannot be nil")
	}
	_, err := repository.DB.Exec(context.Background(), `
		UPDATE profiles
		SET email_unsubscribed_at = NULL,
		    updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("failed to clear unsubscribe: %w", err)
	}
	return nil
}

// IsCalendarConnected checks if a user has connected their Google Calendar
func (s *ProfileService) IsCalendarConnected(userID uuid.UUID) (bool, error) {
	query := `
		SELECT COALESCE(is_calendar_connected, FALSE)
		FROM profiles
		WHERE id = $1
	`

	var connected bool
	err := repository.DB.QueryRow(context.Background(), query, userID).Scan(&connected)
	if err != nil {
		return false, fmt.Errorf("failed to check calendar connection: %w", err)
	}

	return connected, nil
}

