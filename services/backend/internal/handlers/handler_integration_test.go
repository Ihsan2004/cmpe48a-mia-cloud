package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"mia-website/backend/internal/middleware"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// validSignupBody builds a signup request payload that satisfies the
// consent gate added with KVKK / GDPR rollout (migration 020 +
// ValidateSignupConsents in consent_service.go). Without all
// MandatoryConsentTypes present at the current version with granted=true,
// signup returns 400 "missing mandatory consent" which cascades into
// every downstream protected-route test.
//
// Versions are pinned to "1.0" — same value as the
// CurrentXxxVersion constants in consent_service.go. Bump these here
// in lock-step when the document version is rolled.
func validSignupBody(email, password string) []byte {
	body := map[string]interface{}{
		"email":     email,
		"password":  password,
		"full_name": "Integration Test User",
		"consents": []map[string]interface{}{
			{"type": "terms_of_service", "version": "1.0", "granted": true},
			{"type": "privacy_policy", "version": "1.0", "granted": true},
			{"type": "kvkk_aydinlatma", "version": "1.0", "granted": true},
			{"type": "cross_border_transfer", "version": "1.0", "granted": true},
			{"type": "ai_processing", "version": "1.0", "granted": true},
		},
	}
	out, _ := json.Marshal(body)
	return out
}

func skipIfNoIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION_TEST") != "true" {
		t.Skip("skipping integration test: set INTEGRATION_TEST=true")
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("skipping integration test: DATABASE_URL not set")
	}
	if os.Getenv("JWT_SECRET") == "" {
		t.Skip("skipping integration test: JWT_SECRET not set")
	}
}

func setupTestApp(t *testing.T) *fiber.App {
	t.Helper()

	if err := repository.Connect(); err != nil {
		t.Fatalf("failed to connect to test DB: %v", err)
	}
	t.Cleanup(func() {
		repository.Close()
	})

	app := fiber.New()
	api := app.Group("/api")

	authHandler := NewAuthHandler()
	meetingHandler := NewMeetingHandler()
	profileHandler := NewProfileHandler()
	webhookHandler := NewWebhookHandler()

	// Public auth routes
	api.Post("/auth/signup", authHandler.SignUp)
	api.Post("/auth/signin", authHandler.SignIn)
	api.Post("/auth/refresh", authHandler.RefreshToken)

	// Webhook
	api.Post("/webhooks/bot-completion", webhookHandler.BotCompletionWebhook)

	// Protected routes
	protected := api.Group("", middleware.AuthMiddleware())
	protected.Get("/auth/me", authHandler.GetCurrentUser)
	protected.Get("/meetings", meetingHandler.GetMeetings)
	protected.Get("/profile", profileHandler.GetProfile)

	return app
}

// signupTestUser creates a user and returns access + refresh tokens
func signupTestUser(t *testing.T, app *fiber.App) (userID, accessToken, refreshToken string) {
	t.Helper()

	email := fmt.Sprintf("test_%s@integration.test", uuid.New().String()[:8])
	jsonBody := validSignupBody(email, "TestPassword123!")
	req, _ := http.NewRequest("POST", "/api/auth/signup", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("signup request failed: %v", err)
	}

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 201 {
		t.Fatalf("signup returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	json.Unmarshal(respBody, &result)

	accessToken = result["access_token"].(string)
	refreshToken = result["refresh_token"].(string)

	user := result["user"].(map[string]interface{})
	userID = user["id"].(string)

	// Cleanup user after test
	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", userID)
	})

	return userID, accessToken, refreshToken
}

// ==================== Auth Handler Tests ====================

func TestIntegration_Signup_Success(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	email := fmt.Sprintf("test_%s@integration.test", uuid.New().String()[:8])
	body := validSignupBody(email, "TestPassword123!")

	req, _ := http.NewRequest("POST", "/api/auth/signup", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	respBody, _ := io.ReadAll(resp.Body)
	json.Unmarshal(respBody, &result)

	if result["access_token"] == nil {
		t.Error("expected access_token in response")
	}
	if result["refresh_token"] == nil {
		t.Error("expected refresh_token in response")
	}

	// Cleanup
	if user, ok := result["user"].(map[string]interface{}); ok {
		repository.DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", user["id"])
	}
}

func TestIntegration_Signup_DuplicateEmail(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	_, _, _ = signupTestUser(t, app)

	// Try to signup with same email — but we need the email
	// Use a fresh signup then try again
	email := fmt.Sprintf("test_dup_%s@integration.test", uuid.New().String()[:8])
	body := validSignupBody(email, "TestPassword123!")

	req1, _ := http.NewRequest("POST", "/api/auth/signup", bytes.NewBuffer(body))
	req1.Header.Set("Content-Type", "application/json")
	resp1, _ := app.Test(req1, -1)
	if resp1.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp1.Body)
		t.Fatalf("first signup failed: %d %s", resp1.StatusCode, string(respBody))
	}

	// Cleanup first user
	var result1 map[string]interface{}
	respBody1, _ := io.ReadAll(resp1.Body)
	json.Unmarshal(respBody1, &result1)
	if user, ok := result1["user"].(map[string]interface{}); ok {
		t.Cleanup(func() {
			repository.DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", user["id"])
		})
	}

	// Second signup with same email
	body2 := validSignupBody(email, "AnotherPassword!")
	req2, _ := http.NewRequest("POST", "/api/auth/signup", bytes.NewBuffer(body2))
	req2.Header.Set("Content-Type", "application/json")
	resp2, _ := app.Test(req2, -1)

	if resp2.StatusCode == 201 {
		t.Error("expected duplicate email to fail")
	}
}

func TestIntegration_Signin_Success(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	// First signup
	email := fmt.Sprintf("test_signin_%s@integration.test", uuid.New().String()[:8])
	signupBody := validSignupBody(email, "TestPassword123!")

	signupReq, _ := http.NewRequest("POST", "/api/auth/signup", bytes.NewBuffer(signupBody))
	signupReq.Header.Set("Content-Type", "application/json")
	signupResp, _ := app.Test(signupReq, -1)

	var signupResult map[string]interface{}
	signupBody2, _ := io.ReadAll(signupResp.Body)
	json.Unmarshal(signupBody2, &signupResult)
	if user, ok := signupResult["user"].(map[string]interface{}); ok {
		t.Cleanup(func() {
			repository.DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", user["id"])
		})
	}

	// Then signin
	signinBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": "TestPassword123!",
	})

	signinReq, _ := http.NewRequest("POST", "/api/auth/signin", bytes.NewBuffer(signinBody))
	signinReq.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(signinReq, -1)

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestIntegration_Signin_WrongPassword(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	email := fmt.Sprintf("test_wrongpw_%s@integration.test", uuid.New().String()[:8])
	signupBody := validSignupBody(email, "CorrectPassword123!")
	signupReq, _ := http.NewRequest("POST", "/api/auth/signup", bytes.NewBuffer(signupBody))
	signupReq.Header.Set("Content-Type", "application/json")
	signupResp, _ := app.Test(signupReq, -1)

	var signupResult map[string]interface{}
	signupRespBody, _ := io.ReadAll(signupResp.Body)
	json.Unmarshal(signupRespBody, &signupResult)
	if user, ok := signupResult["user"].(map[string]interface{}); ok {
		t.Cleanup(func() {
			repository.DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", user["id"])
		})
	}

	// Wrong password
	signinBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": "WrongPassword!",
	})
	signinReq, _ := http.NewRequest("POST", "/api/auth/signin", bytes.NewBuffer(signinBody))
	signinReq.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(signinReq, -1)

	if resp.StatusCode == 200 {
		t.Error("expected wrong password to fail signin")
	}
}

// ==================== Protected Route Tests ====================

func TestIntegration_GetMe_WithValidToken(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	_, accessToken, _ := signupTestUser(t, app)

	req, _ := http.NewRequest("GET", "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}
}

func TestIntegration_GetMe_NoToken(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/auth/me", nil)
	resp, _ := app.Test(req, -1)

	if resp.StatusCode != 401 {
		t.Errorf("expected 401 without token, got %d", resp.StatusCode)
	}
}

func TestIntegration_GetMe_InvalidToken(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	req, _ := http.NewRequest("GET", "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-abc123")

	resp, _ := app.Test(req, -1)

	if resp.StatusCode != 401 {
		t.Errorf("expected 401 with invalid token, got %d", resp.StatusCode)
	}
}

func TestIntegration_GetMeetings_EmptyList(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	_, accessToken, _ := signupTestUser(t, app)

	req, _ := http.NewRequest("GET", "/api/meetings", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, _ := app.Test(req, -1)

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	respBody, _ := io.ReadAll(resp.Body)
	json.Unmarshal(respBody, &result)

	meetings, ok := result["meetings"].([]interface{})
	if !ok {
		t.Fatal("expected meetings array in response")
	}
	if len(meetings) != 0 {
		t.Errorf("expected 0 meetings for new user, got %d", len(meetings))
	}
}

// ==================== Webhook Tests ====================

func TestIntegration_BotCompletionWebhook_InvalidMeeting(t *testing.T) {
	skipIfNoIntegration(t)
	app := setupTestApp(t)

	body, _ := json.Marshal(map[string]interface{}{
		"meeting_id": uuid.New().String(),
		"status":     "completed",
		"duration":   3600,
	})

	req, _ := http.NewRequest("POST", "/api/webhooks/bot-completion", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	resp, _ := app.Test(req, -1)

	// Should not crash — may return 200 (fire-and-forget) or 404
	if resp.StatusCode >= 500 {
		t.Errorf("expected non-500 status for invalid meeting webhook, got %d", resp.StatusCode)
	}
}

// Suppress unused import warnings
var _ = services.NewAuthService
