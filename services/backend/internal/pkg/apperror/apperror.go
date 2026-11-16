// Package apperror provides structured, user-friendly error handling.
// It classifies common database and service errors into appropriate
// HTTP status codes with Turkish user-facing messages, while keeping
// technical details in structured logs only.
package apperror

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// AppError represents a user-facing error with HTTP status and optional code.
type AppError struct {
	// Status is the HTTP status code to return.
	Status int
	// Message is the user-facing message (Turkish).
	Message string
	// Code is an optional machine-readable error code for frontend handling.
	Code string
	// Field is the optional field name that caused the error.
	Field string
	// Internal is the original error for logging (never sent to client).
	Internal error
}

func (e *AppError) Error() string {
	if e.Internal != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Internal)
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Internal
}

// --- Constructors ---

func BadRequest(msg string) *AppError {
	return &AppError{Status: http.StatusBadRequest, Message: msg}
}

func BadRequestWithField(msg, field string) *AppError {
	return &AppError{Status: http.StatusBadRequest, Message: msg, Field: field}
}

func Unauthorized(msg string) *AppError {
	return &AppError{Status: http.StatusUnauthorized, Message: msg}
}

func Forbidden(msg string) *AppError {
	return &AppError{Status: http.StatusForbidden, Message: msg}
}

func NotFound(msg string) *AppError {
	return &AppError{Status: http.StatusNotFound, Message: msg}
}

func Conflict(msg string) *AppError {
	return &AppError{Status: http.StatusConflict, Message: msg}
}

func ConflictWithCode(msg, code string) *AppError {
	return &AppError{Status: http.StatusConflict, Message: msg, Code: code}
}

func Internal(msg string, err error) *AppError {
	return &AppError{Status: http.StatusInternalServerError, Message: msg, Internal: err}
}

func BadGateway(msg string, err error) *AppError {
	return &AppError{Status: http.StatusBadGateway, Message: msg, Internal: err}
}

func ServiceUnavailable(msg string) *AppError {
	return &AppError{Status: http.StatusServiceUnavailable, Message: msg}
}

func TooManyRequests(msg string) *AppError {
	return &AppError{Status: http.StatusTooManyRequests, Message: msg}
}

// --- PostgreSQL error classification ---

// PostgreSQL error codes
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgNotNullViolation    = "23502"
	pgCheckViolation      = "23514"
	pgStringTooLong       = "22001"
)

// Well-known constraint-to-field mappings for user-friendly messages.
var constraintMessages = map[string]struct {
	Message string
	Field   string
}{
	"users_email_key":           {"Bu e-posta adresi zaten kayıtlı.", "email"},
	"teams_name_key":            {"Bu isimde bir takım zaten mevcut.", "name"},
	"team_members_pkey":         {"Bu kullanıcı zaten takımın üyesi.", ""},
	"action_items_jira_issue_id": {"Bu aksiyon kalemi zaten bir Jira issue'suna bağlı.", ""},
	"meetings_calendar_event_id": {"Bu takvim etkinliği zaten bir toplantıya bağlı.", ""},
}

// ClassifyDBError examines a database error and returns an appropriate AppError.
// If the error is not a recognized DB error, it returns a generic internal error
// with the provided fallback message.
func ClassifyDBError(err error, fallbackMsg string) *AppError {
	if err == nil {
		return nil
	}

	// Row not found
	if errors.Is(err, pgx.ErrNoRows) {
		return &AppError{
			Status:   http.StatusNotFound,
			Message:  "Kayıt bulunamadı.",
			Internal: err,
		}
	}

	// PostgreSQL specific errors
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return classifyPgError(pgErr, fallbackMsg)
	}

	// Connection/timeout errors
	errMsg := err.Error()
	if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "context deadline exceeded") {
		return &AppError{
			Status:   http.StatusGatewayTimeout,
			Message:  "İşlem zaman aşımına uğradı. Lütfen tekrar deneyin.",
			Internal: err,
		}
	}
	if strings.Contains(errMsg, "connection refused") || strings.Contains(errMsg, "connection reset") {
		return &AppError{
			Status:   http.StatusServiceUnavailable,
			Message:  "Veritabanı bağlantısı kurulamadı. Lütfen daha sonra tekrar deneyin.",
			Internal: err,
		}
	}

	// Generic fallback
	return Internal(fallbackMsg, err)
}

// ClassifyServiceError examines an error from the service layer.
// Service errors fall into two categories:
//  1. Business-logic errors (validation, permissions) — user-safe, passed through
//  2. Internal errors (DB failures wrapped with %w) — replaced with fallback message
//
// Internal errors typically contain "failed to" prefix from fmt.Errorf("failed to ...: %w", err).
func ClassifyServiceError(err error, fallbackMsg string, status int) *AppError {
	if err == nil {
		return nil
	}

	// If it's already an AppError, use it directly
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}

	// Try DB error classification first
	dbErr := ClassifyDBError(err, fallbackMsg)
	// If ClassifyDBError returned something other than a generic Internal error, use it
	if dbErr.Status != http.StatusInternalServerError {
		return dbErr
	}

	msg := err.Error()

	// Heuristic: errors starting with "failed to" are internal (wrapped DB/service errors)
	if strings.HasPrefix(msg, "failed to") {
		return &AppError{
			Status:   http.StatusInternalServerError,
			Message:  fallbackMsg,
			Internal: err,
		}
	}

	// Otherwise treat as a user-facing business error
	if status == 0 {
		status = http.StatusBadRequest
	}
	return &AppError{
		Status:  status,
		Message: msg,
	}
}

func classifyPgError(pgErr *pgconn.PgError, fallbackMsg string) *AppError {
	switch pgErr.Code {
	case pgUniqueViolation:
		// Check known constraints
		if cm, ok := constraintMessages[pgErr.ConstraintName]; ok {
			return &AppError{
				Status:   http.StatusConflict,
				Message:  cm.Message,
				Field:    cm.Field,
				Code:     "DUPLICATE",
				Internal: pgErr,
			}
		}
		// Unknown constraint — still return conflict with generic message
		return &AppError{
			Status:   http.StatusConflict,
			Message:  "Bu kayıt zaten mevcut. Lütfen bilgileri kontrol edip tekrar deneyin.",
			Code:     "DUPLICATE",
			Internal: pgErr,
		}

	case pgForeignKeyViolation:
		return &AppError{
			Status:   http.StatusBadRequest,
			Message:  "İlişkili kayıt bulunamadı. Referans edilen veri silinmiş veya mevcut değil.",
			Code:     "REFERENCE_NOT_FOUND",
			Internal: pgErr,
		}

	case pgNotNullViolation:
		field := pgErr.ColumnName
		return &AppError{
			Status:  http.StatusBadRequest,
			Message: fmt.Sprintf("Zorunlu alan boş bırakılamaz: %s", field),
			Field:   field,
			Code:    "REQUIRED_FIELD",
			Internal: pgErr,
		}

	case pgCheckViolation:
		return &AppError{
			Status:   http.StatusBadRequest,
			Message:  "Girilen değer geçerli aralıkta değil. Lütfen kontrol edip tekrar deneyin.",
			Code:     "VALIDATION_FAILED",
			Internal: pgErr,
		}

	case pgStringTooLong:
		return &AppError{
			Status:   http.StatusBadRequest,
			Message:  "Girilen metin çok uzun. Lütfen kısaltıp tekrar deneyin.",
			Code:     "VALUE_TOO_LONG",
			Internal: pgErr,
		}

	default:
		return Internal(fallbackMsg, pgErr)
	}
}
