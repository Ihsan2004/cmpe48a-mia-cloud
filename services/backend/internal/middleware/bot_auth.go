package middleware

import (
	"crypto/subtle"
	"os"

	"github.com/gofiber/fiber/v2"
)

// BotAPIKeyMiddleware validates the X-Bot-Api-Key header against BOT_API_KEY.
//
// Phase 7 fix 2026-04-20 — also accept MIA_BOT_API_KEY as an alias. The bot
// repo's config + env example + ACA deployment workflow historically
// used MIA_BOT_API_KEY, while the backend middleware read only
// BOT_API_KEY. Without a manual duplicate in Container App secrets,
// every bot upload/heartbeat/event was rejected with BOT_AUTH_UNCONFIGURED
// or BOT_AUTH_INVALID. Preferring BOT_API_KEY keeps the roadmap contract
// intact; MIA_BOT_API_KEY falls through when BOT_API_KEY is unset.
//
// Rejects requests when:
//   - Neither BOT_API_KEY nor MIA_BOT_API_KEY is configured (refuses to
//     accept unauthenticated traffic rather than silently running open)
//   - Header is missing
//   - Header does not match (constant-time compare to avoid timing leaks)
func BotAPIKeyMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		expected := os.Getenv("BOT_API_KEY")
		if expected == "" {
			expected = os.Getenv("MIA_BOT_API_KEY")
		}
		if expected == "" {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "Bot API key not configured",
				"code":  "BOT_AUTH_UNCONFIGURED",
			})
		}

		provided := c.Get("X-Bot-Api-Key")
		if provided == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Missing X-Bot-Api-Key header",
				"code":  "BOT_AUTH_MISSING",
			})
		}

		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid bot API key",
				"code":  "BOT_AUTH_INVALID",
			})
		}

		return c.Next()
	}
}
