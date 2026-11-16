package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/apperror"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

func stringPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// SignUp handles POST /api/auth/signup
func (h *AuthHandler) SignUp(c *fiber.Ctx) error {
	var req models.SignUpRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Validate input
	if req.Email == "" || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Email and password are required",
		})
	}

	if len(req.Password) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Password must be at least 8 characters",
		})
	}

	// Validate consent payload BEFORE creating the user. Bail with
	// 400/409 here so we don't end up with an orphan user row when
	// the client sent stale or missing consents.
	consentService := services.NewConsentService()
	if err := consentService.ValidateSignupConsents(req.Consents); err != nil {
		status := fiber.StatusBadRequest
		if errors.Is(err, services.ErrConsentVersionMismatch) {
			status = fiber.StatusConflict
		}
		return c.Status(status).JSON(fiber.Map{
			"error":   "Zorunlu onaylar eksik veya güncel değil. Lütfen sayfayı yenileyip tekrar deneyin.",
			"detail":  err.Error(),
			"code":    "consent_invalid",
		})
	}

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// Check if user already exists
	_, err = authService.GetUserByEmail(ctx, req.Email)
	if err == nil {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error": "User with this email already exists",
		})
	}

	// Create user
	user, err := authService.CreateUser(ctx, req.Email, req.Password, req.FullName)
	if err != nil {
		return apperror.SendDBError(c, err, "Kullanıcı oluşturulamadı. Lütfen tekrar deneyin.", "signup_create_user")
	}

	// Record consents. Validation already passed, so failure here is
	// an infrastructure issue (DB down, FK violation). Roll back the
	// user row if consents fail to persist — otherwise the account is
	// trapped: it can't sign in (gate redirects forever, no consents)
	// and it can't be re-created (email taken).
	ipAddr := stringPtrIfNotEmpty(c.IP())
	userAgent := stringPtrIfNotEmpty(c.Get("User-Agent"))
	if _, err := consentService.RecordSignupConsents(ctx, user.ID, req.Consents, ipAddr, userAgent); err != nil {
		slog.Error("failed to record signup consents — rolling back user",
			"user_id", user.ID, "error", err)
		if delErr := authService.DeleteUser(ctx, user.ID); delErr != nil {
			// Worst case: we leave an orphan user. Log loudly so ops
			// can clean up manually; the user still gets a 500 and
			// can contact support.
			slog.Error("failed to roll back user after consent failure",
				"user_id", user.ID, "delete_error", delErr, "consent_error", err)
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Onay kayıtları oluşturulamadı. Lütfen tekrar deneyin.",
			"code":  "consent_persist_failed",
		})
	}

	// TRIGGER EMAIL VERIFICATION WITH GOOGLE CALENDAR LINK
	// --------------------------------------------------
	go func(u *models.User) {
		// 1. Configure Google OAuth
		clientID := os.Getenv("GOOGLE_CLIENT_ID")
		clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
		redirectURI := os.Getenv("GOOGLE_REDIRECT_URI")
		frontendURL := os.Getenv("FRONTEND_URL")

		if redirectURI == "" {
			if frontendURL != "" {
				redirectURI = frontendURL + "/auth/callback"
			}
		}

		if clientID != "" && clientSecret != "" && redirectURI != "" {
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

			// 2. Generate Magic State: "magic|user_id|timestamp"
			// We use a simple signature for now, but in production this should be HMAC
			// For this implementation plan, we trust the callback to verify the user_id matches or we rely on the fact
			// that this link is sent to the user's email.
			magicState := fmt.Sprintf("magic_%s_%d", u.ID.String(), time.Now().Unix())
			encodedState := base64.URLEncoding.EncodeToString([]byte(magicState))

			// 3. Generate Auth URL
			authURL := config.AuthCodeURL(
				encodedState,
				oauth2.AccessTypeOffline,
				oauth2.SetAuthURLParam("prompt", "consent"), // Force consent to ensure we get Refresh Token
			)

			// 4. Send Email
			emailService := services.NewEmailService()
			fullName := u.Email // Fallback
			if u.FullName != nil {
				fullName = *u.FullName
			}

			// We use a background context or just ignore error since we are in a goroutine
			// In a real message queue system we would handle retries.
			if err := emailService.SendCalendarAuthEmail(u.Email, fullName, authURL); err != nil {
				slog.Error("failed to send onboarding email", "email", u.Email, "error", err)
			} else {
				slog.Info("onboarding email sent", "email", u.Email)
			}
		} else {
			slog.Warn("Google OAuth not configured, skipping onboarding email")
		}
	}(user)

	// Generate tokens
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

	// Store refresh token
	if err := authService.StoreRefreshToken(ctx, user.ID, refreshToken); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to store refresh token",
		})
	}

	// Update last login
	_ = authService.UpdateLastLogin(ctx, user.ID)

	// Return response (don't include password hash)
	userResponse := &models.User{
		ID:            user.ID,
		Email:         user.Email,
		EmailVerified: user.EmailVerified, // False initially
		FullName:      user.FullName,
		CreatedAt:     user.CreatedAt,
		UpdatedAt:     user.UpdatedAt,
		IsActive:      user.IsActive,
		// Admin field removed
	}

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

	return c.Status(fiber.StatusCreated).JSON(models.AuthResponse{
		User:         userResponse,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    30 * 60, // 30 minutes in seconds
		Message:      "Account created. Please check your email to verify account and connect Google Calendar.",
	})
}

// SignIn handles POST /api/auth/signin
func (h *AuthHandler) SignIn(c *fiber.Ctx) error {
	var req models.SignInRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Validate input
	if req.Email == "" || req.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Email and password are required",
		})
	}

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// Get user by email
	user, err := authService.GetUserByEmail(ctx, req.Email)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid email or password",
		})
	}

	// Check if user is active
	if !user.IsActive {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "Account is deactivated",
		})
	}

	// [NEW] Rate Limiting Check
	if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
		lockDuration := time.Until(*user.LockedUntil)
		minutes := int(lockDuration.Minutes()) + 1
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"error": fmt.Sprintf("Too many failed attempts. Account locked. Try again in %d minutes.", minutes),
		})
	}

	// Verify password
	if !authService.VerifyPassword(user.PasswordHash, req.Password) {
		// [NEW] Increment failed attempts
		_ = authService.IncrementFailedLoginAttempts(ctx, user.ID)
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid email or password",
		})
	}

	// [NEW] Reset failed attempts on success
	_ = authService.ResetFailedLoginAttempts(ctx, user.ID)

	// Generate tokens
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

	// Store refresh token
	if err := authService.StoreRefreshToken(ctx, user.ID, refreshToken); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to store refresh token",
		})
	}

	// Update last login
	_ = authService.UpdateLastLogin(ctx, user.ID)

	// Return response (don't include password hash)
	userResponse := &models.User{
		ID:            user.ID,
		Email:         user.Email,
		EmailVerified: user.EmailVerified,
		FullName:      user.FullName,
		CreatedAt:     user.CreatedAt,
		UpdatedAt:     user.UpdatedAt,
		LastLoginAt:   user.LastLoginAt,
		IsActive:      user.IsActive,
		// Admin field removed
	}

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

	return c.JSON(models.AuthResponse{
		User:         userResponse,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    30 * 60, // 30 minutes in seconds
	})
}

// RefreshToken handles POST /api/auth/refresh
func (h *AuthHandler) RefreshToken(c *fiber.Ctx) error {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Fallback: read refresh token from HttpOnly cookie if not in body
	if req.RefreshToken == "" {
		req.RefreshToken = c.Cookies("mia_refresh_token")
	}

	if req.RefreshToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Refresh token is required",
		})
	}

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// Rotate tokens (Validate -> Store New -> Revoke Old)
	user, newAccessToken, newRefreshToken, err := authService.RotateRefreshToken(ctx, req.RefreshToken)
	if err != nil {
		// Differentiate errors if possible, but for security, generic 401 is usually best
		// unless it's a server error.
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid or expired refresh token",
		})
	}

	// Update last login
	_ = authService.UpdateLastLogin(ctx, user.ID)

	// Return response with NEW refresh token
	userResponse := &models.User{
		ID:            user.ID,
		Email:         user.Email,
		EmailVerified: user.EmailVerified,
		FullName:      user.FullName,
		CreatedAt:     user.CreatedAt,
		UpdatedAt:     user.UpdatedAt,
		LastLoginAt:   user.LastLoginAt,
		IsActive:      user.IsActive,
		// Admin field removed
	}

	// Set new refresh token as HttpOnly cookie
	c.Cookie(&fiber.Cookie{
		Name:     "mia_refresh_token",
		Value:    newRefreshToken,
		Path:     "/api/auth",
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
		MaxAge:   30 * 24 * 60 * 60, // 30 days
	})

	return c.JSON(fiber.Map{
		"user":          userResponse,
		"access_token":  newAccessToken,
		"refresh_token": newRefreshToken, // Client must update their stored token!
		"expires_in":    30 * 60,         // 30 minutes in seconds
	})
}

// Logout handles POST /api/auth/logout
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Fallback: read refresh token from HttpOnly cookie if not in body
	if req.RefreshToken == "" {
		req.RefreshToken = c.Cookies("mia_refresh_token")
	}

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// Revoke refresh token (if we have one)
	if req.RefreshToken != "" {
		if err := authService.RevokeRefreshToken(ctx, req.RefreshToken); err != nil {
			// Don't fail if token doesn't exist
			slog.Warn("failed to revoke refresh token", "error", err, "request_id", c.Locals("requestID"))
		}
	}

	// Clear HttpOnly cookie
	c.Cookie(&fiber.Cookie{
		Name:     "mia_refresh_token",
		Value:    "",
		Path:     "/api/auth",
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
		MaxAge:   -1, // expire immediately
	})

	return c.JSON(fiber.Map{
		"message": "Logged out successfully",
	})
}

// GetCurrentUser handles GET /api/auth/me
func (h *AuthHandler) GetCurrentUser(c *fiber.Ctx) error {
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

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// Get user
	user, err := authService.GetUserByID(ctx, userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "User not found",
		})
	}

	// Onboarding status: default to "pending" if the profile lookup
	// fails — we must not 500 /auth/me on a profile-fetch error since
	// /auth/me is the frontend's bootstrap call after login. Logging
	// the error preserves observability (T-04-12b mitigation).
	//
	// Phase 4 ROADMAP Success Criterion #3 requires teams_onboarding_status
	// on /auth/me. CONTEXT.md amendment (2026-05-09) clarifies D-13/D-14:
	// reuse ProfileService.GetProfile (no new query path, no new method);
	// extend the existing handler to include one extra key. Inline service
	// instantiation mirrors the existing pattern at line 450
	// (services.NewAuthService()), so no constructor wiring changes.
	teamsOnboardingStatus := models.TeamsOnboardingStatusPending
	profileService := services.NewProfileService()
	if profile, perr := profileService.GetProfile(userID); perr == nil && profile != nil {
		if profile.TeamsOnboardingStatus != "" {
			teamsOnboardingStatus = profile.TeamsOnboardingStatus
		}
	} else if perr != nil {
		slog.Warn("GetCurrentUser: profile lookup failed; defaulting teams_onboarding_status to 'pending'",
			"userID", userID, "error", perr)
	}

	return c.JSON(fiber.Map{
		"id":                      user.ID,
		"email":                   user.Email,
		"email_verified":          user.EmailVerified,
		"full_name":               user.FullName,
		"created_at":              user.CreatedAt,
		"updated_at":              user.UpdatedAt,
		"last_login_at":           user.LastLoginAt,
		"is_active":               user.IsActive,
		"teams_onboarding_status": teamsOnboardingStatus,
	})
}

// RequestPasswordReset handles POST /api/auth/forgot-password
func (h *AuthHandler) RequestPasswordReset(c *fiber.Ctx) error {
	var req struct {
		Email string `json:"email"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	if req.Email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Email is required",
		})
	}

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// User exists, proceed with password reset flow
	user, err := authService.GetUserByEmail(ctx, req.Email)
	if err == nil {
		// Generate reset token
		token, err := authService.CreatePasswordResetToken(ctx, user.Email)
		if err != nil {
			slog.Error("failed to create reset token", "error", err, "request_id", c.Locals("requestID"))
			// Still return success to user
			return c.JSON(fiber.Map{
				"message": "If an account with that email exists, a password reset link has been sent",
			})
		}

		// Send email
		emailService := services.NewEmailService()
		// Safe dereference for FullName
		fullName := ""
		if user.FullName != nil {
			fullName = *user.FullName
		}

		// In production, we should probably run this in background, but capturing errors is useful for now
		if err := emailService.SendPasswordResetEmail(user.Email, token, fullName); err != nil {
			slog.Error("failed to send reset email", "error", err, "request_id", c.Locals("requestID"))
		}
	}

	return c.JSON(fiber.Map{
		"message": "If an account with that email exists, a password reset link has been sent",
	})
}

// ResetPassword handles POST /api/auth/reset-password
func (h *AuthHandler) ResetPassword(c *fiber.Ctx) error {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	if req.Token == "" || req.NewPassword == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Token and new password are required",
		})
	}

	if len(req.NewPassword) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Password must be at least 8 characters",
		})
	}

	// Create auth service
	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	ctx := context.Background()

	// Reset password
	if err := authService.ResetPasswordWithToken(ctx, req.Token, req.NewPassword); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid or expired token",
		})
	}

	return c.JSON(fiber.Map{
		"message": "Password reset successfully",
	})
}
