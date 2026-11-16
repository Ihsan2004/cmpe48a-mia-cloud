package middleware

import (
	"strings"

	"mia-website/backend/internal/services"

	"github.com/gofiber/fiber/v2"
)

// AuthMiddleware
func AuthMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Skip auth for OPTIONS requests (CORS preflight)
		if c.Method() == "OPTIONS" {
			return c.Next()
		}

		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Missing authorization header",
				"code":  "TOKEN_MISSING",
			})
		}

		// Extract token from "Bearer <token>"
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid authorization header format",
				"code":  "TOKEN_INVALID_FORMAT",
			})
		}

		tokenString := parts[1]

		// Create auth service
		authService, err := services.NewAuthService()
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Authentication service not available",
				"code":  "SERVER_ERROR",
			})
		}

		// Validate access token
		claims, err := authService.ValidateAccessToken(tokenString)
		if err != nil {
			// Check if it's an expiration error vs invalid signature
			errorMsg := err.Error()
			errorCode := "TOKEN_INVALID"

			if strings.Contains(strings.ToLower(errorMsg), "expired") {
				errorCode = "TOKEN_EXPIRED"
			}

			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error":   "Invalid or expired token",
				"code":    errorCode,
				"details": errorMsg,
			})
		}

		// Get user ID from claims
		userID, ok := claims["sub"].(string)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "User ID not found in token",
				"code":  "TOKEN_INVALID_CLAIMS",
			})
		}

		// Try to capture email for attendee matching (may be absent)
		if email, ok := claims["email"].(string); ok && email != "" {
			c.Locals("userEmail", email)
		}

		// Store user ID in context
		c.Locals("userID", userID)

		return c.Next()
	}
}
