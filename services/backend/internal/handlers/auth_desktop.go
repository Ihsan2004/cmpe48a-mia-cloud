package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"mia-website/backend/internal/pkg/apperror"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

type googleDesktopSignInRequest struct {
	GoogleIDToken      string `json:"google_id_token"`
	GoogleAccessToken  string `json:"google_access_token"`
	GoogleRefreshToken string `json:"google_refresh_token"`
}

// googleTokenInfo matches the relevant subset of Google's tokeninfo response.
// Fields are shared between the id_token and access_token variants.
type googleTokenInfo struct {
	Aud           string `json:"aud"`
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	Sub           string `json:"sub"`
	Name          string `json:"name"`
	ExpiresIn     string `json:"expires_in"`
	Exp           string `json:"exp"`
	Scope         string `json:"scope"`
}

// GoogleDesktopSignIn handles POST /api/auth/google/desktop
// The desktop recorder app completes the Google OAuth flow locally and posts
// the resulting tokens here to exchange them for a Mia JWT pair.
func (h *AuthHandler) GoogleDesktopSignIn(c *fiber.Ctx) error {
	requestID, _ := c.Locals("requestID").(string)

	var req googleDesktopSignInRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	if req.GoogleIDToken == "" && req.GoogleAccessToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "google_id_token or google_access_token is required",
		})
	}

	desktopClientID := os.Getenv("GOOGLE_DESKTOP_CLIENT_ID")
	if desktopClientID == "" {
		desktopClientID = os.Getenv("GOOGLE_CLIENT_ID")
	}
	if desktopClientID == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Google OAuth desktop client not configured",
		})
	}

	ctx := context.Background()

	email, fullName, googleSub, err := resolveGoogleIdentity(ctx, req, desktopClientID)
	if err != nil {
		return apperror.Send(c, apperror.BadGateway("Google kimliği doğrulanamadı.", err), "google_desktop_verify", requestID)
	}
	if email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Email claim missing from Google token",
		})
	}

	authService, err := services.NewAuthService()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Authentication service not available",
		})
	}

	user, err := authService.GetUserByEmail(ctx, email)
	if err != nil {
		randomPassword := fmt.Sprintf("oauth_desktop_%s_%d", googleSub, time.Now().UnixNano())
		user, err = authService.CreateUser(ctx, email, randomPassword, fullName)
		if err != nil {
			return apperror.SendDBError(c, err, "Kullanıcı oluşturulamadı. Lütfen tekrar deneyin.", "desktop_create_user", requestID)
		}
	}

	if !user.IsActive {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "Account is deactivated",
		})
	}

	_, _ = repository.DB.Exec(ctx, "UPDATE public.users SET email_verified = TRUE WHERE id = $1", user.ID)

	if req.GoogleRefreshToken != "" {
		profileService := services.NewProfileService()
		if err := profileService.SaveGoogleConnection(user.ID, req.GoogleRefreshToken, email); err != nil {
			slog.Warn("failed to save desktop google refresh token", "request_id", requestID, "error", err)
		}
	}

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

	return c.JSON(fiber.Map{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_in":    30 * 60,
		"user": fiber.Map{
			"id":    user.ID,
			"email": user.Email,
		},
	})
}

// resolveGoogleIdentity verifies the supplied Google credential via Google's
// tokeninfo endpoint. The endpoint validates the signature for id_tokens and
// the liveness + audience for access_tokens. id_token is preferred because it
// carries the email and name claims without an extra userinfo call.
func resolveGoogleIdentity(ctx context.Context, req googleDesktopSignInRequest, expectedAudience string) (email, fullName, sub string, err error) {
	var info *googleTokenInfo

	if req.GoogleIDToken != "" {
		info, err = fetchTokenInfo(ctx, "id_token", req.GoogleIDToken)
	} else {
		info, err = fetchTokenInfo(ctx, "access_token", req.GoogleAccessToken)
	}
	if err != nil {
		return "", "", "", err
	}

	if info.Aud != expectedAudience {
		return "", "", "", fmt.Errorf("token audience mismatch: got %q", info.Aud)
	}

	return info.Email, info.Name, info.Sub, nil
}

func fetchTokenInfo(ctx context.Context, kind, token string) (*googleTokenInfo, error) {
	endpoint := "https://www.googleapis.com/oauth2/v3/tokeninfo?" + kind + "=" + url.QueryEscape(token)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build tokeninfo request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call tokeninfo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tokeninfo returned status %d", resp.StatusCode)
	}

	var info googleTokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode tokeninfo: %w", err)
	}
	return &info, nil
}
