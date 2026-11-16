package handlers

import (
	"context"
	"encoding/base64"
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
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	oauth2api "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

type AuthHandler struct{}

func NewAuthHandler() *AuthHandler {
	return &AuthHandler{}
}

// ensureCalendarWatchAsync keeps Google Calendar push sync self-healing.
// Manual sync should not be the only path that recreates a missing/expired
// watch subscription after the user has already connected their calendar.
func (h *AuthHandler) ensureCalendarWatchAsync(userID uuid.UUID, source string) {
	go func() {
		if err := services.NewCalendarWatchService().EnsureWatchForUser(userID); err != nil {
			slog.Warn("failed to ensure calendar watch",
				"user_id", userID,
				"source", source,
				"error", err,
			)
			return
		}

		slog.Info("calendar watch ensured",
			"user_id", userID,
			"source", source,
		)
	}()
}

// GetGoogleAuthURL returns the Google OAuth authorization URL
// Query param: mode=signup (force consent) or mode=login (smart detection)
func (h *AuthHandler) GetGoogleAuthURL(c *fiber.Ctx) error {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURI := os.Getenv("GOOGLE_REDIRECT_URI")
	platform := c.Query("platform", "web")
	redirectMode := c.Query("redirect", "false") == "true"

	if clientID == "" || clientSecret == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Google OAuth not configured",
		})
	}

	// Use frontend URL for callback if not set
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		slog.Warn("FRONTEND_URL not set, OAuth redirect may not work correctly", "request_id", c.Locals("requestID"))
		frontendURL = "" // Don't use localhost fallback in production
	}

	if redirectURI == "" {
		if frontendURL == "" {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "FRONTEND_URL or GOOGLE_REDIRECT_URI must be set",
			})
		}
		redirectURI = frontendURL + "/auth/callback"
	}

	// Check mode: signup (force consent) or login (smart)
	mode := c.Query("mode", "login")

	// Both modes request Calendar access + Offline (Refresh Token)
	// The difference is "prompt"
	scopes := []string{
		"https://www.googleapis.com/auth/calendar",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	}

	var statePrefix string
	var authOptions []oauth2.AuthCodeOption

	if mode == "signup" {
		// Explicit Connect/Signup: Force consent to guarantee Refresh Token
		statePrefix = "signup_"
		authOptions = []oauth2.AuthCodeOption{
			oauth2.AccessTypeOffline,
			oauth2.SetAuthURLParam("prompt", "consent"),
		}
	} else {
		// Login/Smart: Try silent, but request offline in case new user
		statePrefix = "login_"
		authOptions = []oauth2.AuthCodeOption{
			oauth2.AccessTypeOffline,
			oauth2.SetAuthURLParam("prompt", "select_account"),
		}
	}

	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes:       scopes,
		Endpoint:     google.Endpoint,
	}

	// Generate state token for CSRF protection with mode prefix
	state := base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("%s%d", statePrefix, time.Now().UnixNano())))

	// Try to store state in cookie (may not work cross-domain)
	c.Cookie(&fiber.Cookie{
		Name:     "oauth_state",
		Value:    state,
		HTTPOnly: true,
		SameSite: "Lax",
		MaxAge:   600, // 10 minutes
		Path:     "/",
	})

	url := config.AuthCodeURL(state, authOptions...)

	if redirectMode {
		return c.Redirect(url, fiber.StatusTemporaryRedirect)
	}

	return c.JSON(fiber.Map{
		"auth_url": url,
		"state":    state,
		"mode":     mode,
		"platform": platform,
	})
}

// GoogleCallback handles the OAuth callback from Google
func (h *AuthHandler) GoogleCallback(c *fiber.Ctx) error {
	code := c.Query("code")
	state := c.Query("state")

	// Verify state token
	// Primary method: Get from header (frontend sends it from sessionStorage)
	storedState := c.Get("X-OAuth-State")

	// Fallback: Try to get from cookie (may work in same-domain scenarios)
	if storedState == "" {
		storedState = c.Cookies("oauth_state")
	}

	// Allow bypassing state check in development mode (for testing)
	allowStateBypass := os.Getenv("OAUTH_ALLOW_STATE_BYPASS") == "true"

	// Determine mode and check for Magic State
	var isMagicLink bool
	var magicUserID string

	if decodedState, err := base64.URLEncoding.DecodeString(state); err == nil {
		stateStr := string(decodedState)
		if strings.HasPrefix(stateStr, "magic_") {
			isMagicLink = true
			parts := strings.Split(stateStr, "_")
			if len(parts) >= 2 {
				magicUserID = parts[1]
			}
			// Magic link doesn't require stored state check since it's initiated from email
			allowStateBypass = true
		}
	}

	if !allowStateBypass && (storedState == "" || storedState != state) {
		slog.Warn("OAuth state mismatch", "request_id", c.Locals("requestID"), "stored_state", storedState, "received_state", state)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Geçersiz oturum durumu. Lütfen tekrar giriş yapın.",
		})
	}

	// Clear state cookie
	c.Cookie(&fiber.Cookie{
		Name:     "oauth_state",
		Value:    "",
		HTTPOnly: true,
		SameSite: "Lax",
		MaxAge:   -1,
	})

	if code == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Authorization code not provided",
		})
	}

	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURI := os.Getenv("GOOGLE_REDIRECT_URI")

	// Use frontend URL for callback if not set
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = ""
	}

	if redirectURI == "" {
		if frontendURL == "" {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "FRONTEND_URL or GOOGLE_REDIRECT_URI must be set",
			})
		}
		redirectURI = frontendURL + "/auth/callback"
	}

	// Always request same scopes to match what was asked
	scopes := []string{
		"https://www.googleapis.com/auth/calendar",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	}

	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes:       scopes,
		Endpoint:     google.Endpoint,
	}

	ctx := context.Background()

	// Exchange code for token
	requestID, _ := c.Locals("requestID").(string)
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Google ile kimlik doğrulama başarısız oldu. Lütfen tekrar deneyin.", err), "google_token_exchange", requestID)
	}

	// Check calendar access (validation)
	// We can trust the scope grant but checking effectively validates the token
	client := config.Client(ctx, token)
	service, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err == nil {
		_, _ = service.CalendarList.List().MaxResults(1).Do()
		// We ignore error here, if they didn't grant calendar we just won't be able to sync
	}

	// Get user info from Google
	oauth2Service, err := oauth2api.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to create OAuth2 service",
		})
	}

	userInfo, err := oauth2Service.Userinfo.Get().Do()
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Google kullanıcı bilgileri alınamadı. Lütfen tekrar deneyin.", err), "google_userinfo", requestID)
	}

	// Auth Logic
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	var user *models.User
	var isNewUser bool

	if isMagicLink && magicUserID != "" {
		// MAGIC LINK FLOW
		// ----------------
		muid, err := uuid.Parse(magicUserID)
		if err == nil {
			user, err = authService.GetUserByID(ctx, muid)
			// Verify it's the same email to prevent attack
			if err == nil && user.Email == userInfo.Email {
				// Mark as verified
				_, _ = repository.DB.Exec(ctx, "UPDATE public.users SET email_verified = TRUE WHERE id = $1", user.ID)
				isNewUser = true // Treat as "new" login effectively
			} else {
				// Email mismatch or user not found, fallback to standard flow
				slog.Warn("Magic link user mismatch", "request_id", c.Locals("requestID"), "user_email", user.Email, "google_email", userInfo.Email)
				user = nil
			}
		}
	}

	if user == nil {
		// STANDARD / FALLBACK FLOW
		// ------------------------
		existingUser, err := authService.GetUserByEmail(ctx, userInfo.Email)
		if err != nil {
			// CREATE NEW USER
			randomPassword := fmt.Sprintf("oauth_%s_%d", userInfo.Id, time.Now().UnixNano())
			user, err = authService.CreateUser(ctx, userInfo.Email, randomPassword, userInfo.Name)
			if err != nil {
				return apperror.SendDBError(c, err, "Kullanıcı oluşturulamadı. Lütfen tekrar deneyin.", "create_user", requestID)
			}
			isNewUser = true
		} else {
			// EXISTING USER
			user = existingUser
			isNewUser = false
		}

		// Always mark verified if coming from Google
		_, _ = repository.DB.Exec(ctx, "UPDATE public.users SET email_verified = TRUE WHERE id = $1", user.ID)
	}

	// TOKEN HANDLING
	// --------------

	hasCalendarConnection := false

	// If Google gave us a refresh token, SAVE IT.
	if token.RefreshToken != "" {
		profileService := services.NewProfileService()
		if err := profileService.SaveGoogleConnection(user.ID, token.RefreshToken, userInfo.Email); err != nil {
			slog.Warn("Failed to save Google refresh token", "request_id", c.Locals("requestID"), "error", err)
		} else {
			hasCalendarConnection = true
		}
	} else {
		// If no refresh token, check if we have one in DB
		profileService := services.NewProfileService()
		rt, err := profileService.GetGoogleRefreshToken(user.ID)
		if err != nil || rt == nil || *rt == "" {
			// CRITICAL: We need a refresh token but don't have one.
			// If this is a magic link or signup mode, we really should have gotten one.
			// If login mode, we might need to prompt user.
			// But for now, we just proceed and let the frontend "Calendar Not Connected" UI handle it.
			slog.Warn("User logged in but no refresh token available", "request_id", c.Locals("requestID"), "user_id", user.ID)
		} else {
			hasCalendarConnection = true
		}
	}

	if hasCalendarConnection {
		h.ensureCalendarWatchAsync(user.ID, "google_callback")
	}

	// Generate JWT tokens
	accessToken, err := authService.GenerateAccessToken(user.ID, user.Email)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to generate access token",
		})
	}

	refreshToken, err := authService.GenerateRefreshToken(user.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to generate refresh token",
		})
	}

	if err := authService.StoreRefreshToken(ctx, user.ID, refreshToken); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to store refresh token",
		})
	}

	_ = authService.UpdateLastLogin(ctx, user.ID)

	// Set refresh token as HttpOnly cookie (XSS protection)
	c.Cookie(&fiber.Cookie{
		Name:     "mia_refresh_token",
		Value:    refreshToken,
		Path:     "/api/auth",
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
		MaxAge:   30 * 24 * 60 * 60, // 30 days
	})

	return c.JSON(fiber.Map{
		"user": models.User{
			ID:            user.ID,
			Email:         user.Email,
			EmailVerified: true,
			FullName:      user.FullName,
			IsActive:      user.IsActive,
			// Admin field removed
		},
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_in":    30 * 60,
		"google_token":  token.AccessToken,
		"is_new_user":   isNewUser,
		"redirect_url": fmt.Sprintf("%s/auth/callback?access_token=%s&refresh_token=%s",
			frontendURL,
			accessToken,
			refreshToken,
		),
	})
}

// extractUserIDFromAuth tries to parse JWT from Authorization header and return user UUID
// Now uses native JWT instead of Supabase JWT
func extractUserIDFromAuth(authHeader string) (*uuid.UUID, error) {
	if authHeader == "" {
		return nil, fmt.Errorf("missing authorization header")
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, fmt.Errorf("invalid authorization header format")
	}

	authService, err := services.NewAuthService()
	if err != nil {
		return nil, fmt.Errorf("authentication service not available")
	}

	tokenString := parts[1]
	claims, err := authService.ValidateAccessToken(tokenString)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return nil, fmt.Errorf("user id not found in token")
	}

	uid, err := uuid.Parse(sub)
	if err != nil {
		return nil, fmt.Errorf("invalid user id format")
	}

	return &uid, nil
}

// SaveGoogleToken handles POST /api/auth/google-token
// This endpoint receives the refresh token from the frontend and saves it to the user's profile
func (h *AuthHandler) SaveGoogleToken(c *fiber.Ctx) error {
	slog.Debug("Hit SaveGoogleToken endpoint", "request_id", c.Locals("requestID"))
	// Get user ID from context (set by auth middleware)
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "User ID not found",
		})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid user ID",
		})
	}

	var body struct {
		RefreshToken string `json:"refresh_token"`
		GoogleEmail  string `json:"google_email,omitempty"`
	}

	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	slog.Debug("Received refresh token", "request_id", c.Locals("requestID"), "token_length", len(body.RefreshToken))

	if body.RefreshToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Refresh token is required",
		})
	}

	// Save refresh token to user's profile
	profileService := services.NewProfileService()

	requestID, _ := c.Locals("requestID").(string)

	if body.GoogleEmail != "" {
		// Save with Google email for account linking
		if err := profileService.SaveGoogleConnection(userID, body.RefreshToken, body.GoogleEmail); err != nil {
			return apperror.SendDBError(c, err, "Google bağlantısı kaydedilemedi. Lütfen tekrar deneyin.", "save_google_connection", requestID)
		}
	} else {
		// Save just the refresh token
		if err := profileService.SaveGoogleRefreshToken(userID, body.RefreshToken); err != nil {
			return apperror.SendDBError(c, err, "Token kaydedilemedi. Lütfen tekrar deneyin.", "save_refresh_token", requestID)
		}
	}

	h.ensureCalendarWatchAsync(userID, "save_google_token")

	return c.JSON(fiber.Map{
		"message":               "Google Calendar connected successfully",
		"is_calendar_connected": true,
	})
}

// GetGoogleRefreshToken handles GET /api/auth/google-refresh-token
// This endpoint retrieves the Google refresh token from the user's profile (stored in Supabase)
func (h *AuthHandler) GetGoogleRefreshToken(c *fiber.Ctx) error {
	// Get user ID from context (set by auth middleware)
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "User ID not found",
		})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid user ID",
		})
	}

	// Get refresh token from user's profile
	profileService := services.NewProfileService()
	refreshToken, err := profileService.GetGoogleRefreshToken(userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Refresh token not found",
		})
	}

	if refreshToken == nil || *refreshToken == "" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "No refresh token available",
		})
	}

	return c.JSON(fiber.Map{
		"refresh_token": *refreshToken,
	})
}

// DisconnectGoogleCalendar handles POST /api/auth/google-disconnect
// This endpoint removes the Google Calendar connection from the user's profile
func (h *AuthHandler) DisconnectGoogleCalendar(c *fiber.Ctx) error {
	slog.Debug("Hit DisconnectGoogleCalendar endpoint", "request_id", c.Locals("requestID"))

	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "User ID not found",
		})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid user ID",
		})
	}

	profileService := services.NewProfileService()

	// Stop any active webhook subscription before revoking/removing the
	// refresh token. Once the token is gone we may lose the ability to stop
	// the channel cleanly on Google's side.
	if err := services.NewCalendarWatchService().StopWatchForUser(userID); err != nil {
		slog.Warn("Failed to stop calendar watch during disconnect",
			"request_id", c.Locals("requestID"),
			"user_id", userID,
			"error", err,
		)
	}

	// Get current refresh token to revoke it
	refreshToken, err := profileService.GetGoogleRefreshToken(userID)
	if err == nil && refreshToken != nil && *refreshToken != "" {
		// Try to revoke the token with Google (best effort)
		revokeURL := fmt.Sprintf("https://oauth2.googleapis.com/revoke?token=%s", *refreshToken)
		resp, err := http.Post(revokeURL, "application/x-www-form-urlencoded", nil)
		if err != nil {
			slog.Warn("Failed to revoke token with Google", "request_id", c.Locals("requestID"), "error", err)
		} else {
			resp.Body.Close()
			slog.Debug("Token revoked with Google", "request_id", c.Locals("requestID"), "status_code", resp.StatusCode)
		}
	}

	// Disconnect from database
	requestID, _ := c.Locals("requestID").(string)
	if err := profileService.DisconnectGoogleCalendar(userID); err != nil {
		return apperror.SendDBError(c, err, "Takvim bağlantısı kaldırılamadı. Lütfen tekrar deneyin.", "disconnect_calendar", requestID)
	}

	return c.JSON(fiber.Map{
		"message":               "Google Calendar disconnected successfully",
		"is_calendar_connected": false,
	})
}

// GetCalendarConnectionURL handles GET /api/auth/google/connect
// This endpoint returns OAuth URL specifically for connecting calendar (for users who signed up with email)
func (h *AuthHandler) GetCalendarConnectionURL(c *fiber.Ctx) error {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURI := os.Getenv("GOOGLE_REDIRECT_URI")

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		slog.Warn("FRONTEND_URL not set, OAuth redirect may not work correctly", "request_id", c.Locals("requestID"))
		frontendURL = "" // Don't use localhost fallback in production
	}

	// Use the same redirect URI as main auth (already registered in Google Console)
	if redirectURI == "" {
		if frontendURL == "" {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "FRONTEND_URL or GOOGLE_REDIRECT_URI must be set",
			})
		}
		redirectURI = frontendURL + "/auth/callback"
	}

	if clientID == "" || clientSecret == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Google OAuth not configured",
		})
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

	// Generate state token with "connect_" prefix to identify calendar connection flow
	state := base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("connect_%d", time.Now().UnixNano())))

	c.Cookie(&fiber.Cookie{
		Name:     "oauth_state",
		Value:    state,
		HTTPOnly: true,
		SameSite: "Lax",
		MaxAge:   600,
		Path:     "/",
	})

	url := config.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	return c.JSON(fiber.Map{
		"auth_url": url,
		"state":    state,
		"mode":     "connect", // Indicates this is for connecting, not signing in
	})
}

// CheckCalendarConnection handles GET /api/auth/calendar-status
// This endpoint checks if the current user has connected their Google Calendar
func (h *AuthHandler) CheckCalendarConnection(c *fiber.Ctx) error {
	userIDStr, ok := c.Locals("userID").(string)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "User ID not found",
		})
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid user ID",
		})
	}

	requestID, _ := c.Locals("requestID").(string)
	profileService := services.NewProfileService()
	connected, err := profileService.IsCalendarConnected(userID)
	if err != nil {
		return apperror.SendDBError(c, err, "Takvim bağlantı durumu kontrol edilemedi. Lütfen tekrar deneyin.", "check_calendar_connection", requestID)
	}

	return c.JSON(fiber.Map{
		"is_calendar_connected": connected,
	})
}
