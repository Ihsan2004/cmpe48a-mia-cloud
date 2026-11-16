package middleware

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RequestLogger logs every HTTP request in a structured format.
func RequestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		startedAt := time.Now()

		reqID := strings.TrimSpace(c.Get("X-Request-ID"))
		if reqID == "" {
			reqID = uuid.NewString()
		}
		c.Set("X-Request-ID", reqID)
		c.Locals("requestID", reqID)

		err := c.Next()

		status := c.Response().StatusCode()
		if err != nil {
			var fiberErr *fiber.Error
			if errors.As(err, &fiberErr) {
				status = fiberErr.Code
			} else {
				status = fiber.StatusInternalServerError
			}
		}

		fields := []any{
			"request_id", reqID,
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"ip", c.IP(),
		}
		if userID, ok := c.Locals("userID").(string); ok && strings.TrimSpace(userID) != "" {
			fields = append(fields, "user_id", userID)
		}

		switch {
		case status >= 500:
			slog.Error("http_request", fields...)
		case status >= 400:
			slog.Warn("http_request", fields...)
		default:
			slog.Info("http_request", fields...)
		}

		return err
	}
}
