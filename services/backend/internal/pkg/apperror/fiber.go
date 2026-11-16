package apperror

import (
	"log/slog"

	"github.com/gofiber/fiber/v2"
)

// Send writes an AppError as a JSON response and logs the internal error.
// Usage in handlers:
//
//	if err != nil {
//	    return apperror.Send(c, apperror.ClassifyDBError(err, "Kaydetme başarısız."), "create_meeting", requestID)
//	}
func Send(c *fiber.Ctx, appErr *AppError, operation string, requestID ...string) error {
	if appErr == nil {
		return nil
	}

	// Log the internal (technical) error for debugging
	attrs := []any{
		"operation", operation,
		"status", appErr.Status,
	}
	if len(requestID) > 0 && requestID[0] != "" {
		attrs = append(attrs, "request_id", requestID[0])
	}
	if appErr.Internal != nil {
		attrs = append(attrs, "error", appErr.Internal)
	}
	if appErr.Code != "" {
		attrs = append(attrs, "code", appErr.Code)
	}
	if appErr.Field != "" {
		attrs = append(attrs, "field", appErr.Field)
	}

	if appErr.Status >= 500 {
		slog.Error(appErr.Message, attrs...)
	} else {
		slog.Warn(appErr.Message, attrs...)
	}

	// Build the response — only user-safe fields
	resp := fiber.Map{
		"error": appErr.Message,
	}
	if appErr.Code != "" {
		resp["code"] = appErr.Code
	}
	if appErr.Field != "" {
		resp["field"] = appErr.Field
	}

	return c.Status(appErr.Status).JSON(resp)
}

// SendDBError is a convenience wrapper: classify + send in one call.
func SendDBError(c *fiber.Ctx, err error, fallbackMsg, operation string, requestID ...string) error {
	return Send(c, ClassifyDBError(err, fallbackMsg), operation, requestID...)
}

// SendServiceError classifies a service-layer error and sends the response.
// Business-logic errors (validation, permissions) are passed through as-is.
// Internal errors (DB failures with "failed to" prefix) use the fallback message.
// defaultStatus is used for business-logic errors (typically 400 or 403).
func SendServiceError(c *fiber.Ctx, err error, fallbackMsg string, defaultStatus int, operation string, requestID ...string) error {
	return Send(c, ClassifyServiceError(err, fallbackMsg, defaultStatus), operation, requestID...)
}
