package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	jwtSecret []byte
}

func NewAuthService() (*AuthService, error) {
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable is not set")
	}

	service := &AuthService{
		jwtSecret: []byte(jwtSecret),
	}

	// Ensure password reset table exists
	if err := service.EnsureResetTokenTableExists(context.Background()); err != nil {
		slog.Warn("failed to ensure password reset table exists", "error", err)
	}

	return service, nil
}

// HashPassword hashes a password using bcrypt
func (s *AuthService) HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword verifies a password against a hash
func (s *AuthService) VerifyPassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}

// CreateUser creates a new user in the database
func (s *AuthService) CreateUser(ctx context.Context, email, password, fullName string) (*models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	// Hash password
	passwordHash, err := s.HashPassword(password)
	if err != nil {
		return nil, err
	}

	// Generate UUID for user
	userID := uuid.New()

	// Insert user
	query := `
		INSERT INTO public.users (id, email, password_hash, full_name, email_verified)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, email, email_verified, full_name, created_at, updated_at, last_login_at, is_active
	`

	var user models.User
	var fullNamePtr *string
	if fullName != "" {
		fullNamePtr = &fullName
	}

	err = repository.DB.QueryRow(ctx, query, userID, email, passwordHash, fullNamePtr, false).Scan(
		&user.ID,
		&user.Email,
		&user.EmailVerified,
		&user.FullName,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastLoginAt,
		&user.IsActive,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}



	return &user, nil
}

// DeleteUser hard-deletes a user row by id. Intended for signup
// rollback: when consent persistence fails right after CreateUser, we
// remove the half-finished account so the email isn't permanently
// blocked. Cascade FKs handle dependent rows (profiles, consents, etc.).
func (s *AuthService) DeleteUser(ctx context.Context, userID uuid.UUID) error {
	_, err := repository.DB.Exec(ctx, `DELETE FROM public.users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	return nil
}

// GetUserByEmail retrieves a user by email
func (s *AuthService) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	// Join with profiles to get the most up-to-date name/avatar
	query := `
		SELECT u.id, u.email, u.password_hash, u.email_verified, 
		       COALESCE(p.full_name, u.full_name), 
		       u.created_at, u.updated_at, u.last_login_at, u.is_active,
		       u.failed_login_attempts, u.locked_until
		FROM public.users u
		LEFT JOIN public.profiles p ON u.id = p.id
		WHERE u.email = $1
	`

	var user models.User
	err := repository.DB.QueryRow(ctx, query, email).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.EmailVerified,
		&user.FullName,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastLoginAt,
		&user.IsActive,
		&user.FailedLoginAttempts,
		&user.LockedUntil,
	)

	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	return &user, nil
}

// GetUserByID retrieves a user by ID
func (s *AuthService) GetUserByID(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	// Join with profiles to get the most up-to-date name/avatar
	query := `
		SELECT u.id, u.email, u.password_hash, u.email_verified, 
		       COALESCE(p.full_name, u.full_name), 
		       u.created_at, u.updated_at, u.last_login_at, u.is_active,
		       u.failed_login_attempts, u.locked_until
		FROM public.users u
		LEFT JOIN public.profiles p ON u.id = p.id
		WHERE u.id = $1
	`

	var user models.User
	err := repository.DB.QueryRow(ctx, query, userID).Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.EmailVerified,
		&user.FullName,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastLoginAt,
		&user.IsActive,
		&user.FailedLoginAttempts,
		&user.LockedUntil,
	)

	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	return &user, nil
}

// GenerateAccessToken generates a JWT access token
func (s *AuthService) GenerateAccessToken(userID uuid.UUID, email string) (string, error) {
	now := time.Now()
	expiresAt := now.Add(30 * time.Minute) // Access token expires in 30 minutes

	claims := jwt.MapClaims{
		"sub":   userID.String(),
		"email": email,
		"iat":   now.Unix(),
		"exp":   expiresAt.Unix(),
		"type":  "access",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

// GenerateRefreshToken generates a refresh token (long-lived, stored in DB)
func (s *AuthService) GenerateRefreshToken(userID uuid.UUID) (string, error) {
	// Generate a secure random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate refresh token: %w", err)
	}

	return base64.URLEncoding.EncodeToString(tokenBytes), nil
}

// StoreRefreshToken stores a refresh token in the database
func (s *AuthService) StoreRefreshToken(ctx context.Context, userID uuid.UUID, token string) error {
	expiresAt := time.Now().Add(30 * 24 * time.Hour) // 30 days

	query := `
		INSERT INTO public.refresh_tokens (user_id, token, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (token) DO UPDATE SET expires_at = $3, created_at = NOW()
	`

	_, err := repository.DB.Exec(ctx, query, userID, token, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to store refresh token: %w", err)
	}

	return nil
}

// ValidateRefreshToken validates and returns user ID from refresh token
func (s *AuthService) ValidateRefreshToken(ctx context.Context, token string) (*uuid.UUID, error) {
	query := `
		SELECT user_id
		FROM public.refresh_tokens
		WHERE token = $1 
		  AND expires_at > NOW() 
		  AND revoked = FALSE
	`

	var userID uuid.UUID
	err := repository.DB.QueryRow(ctx, query, token).Scan(&userID)
	if err != nil {
		return nil, fmt.Errorf("invalid or expired refresh token")
	}

	return &userID, nil
}

// RotateRefreshToken invalidates the old refresh token and issues a new one
func (s *AuthService) RotateRefreshToken(ctx context.Context, oldToken string) (*models.User, string, string, error) {
	// 1. Validate the old token first
	userID, err := s.ValidateRefreshToken(ctx, oldToken)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid token for rotation: %w", err)
	}

	// 2. Get User details (to ensure user is still active)
	user, err := s.GetUserByID(ctx, *userID)
	if err != nil {
		return nil, "", "", fmt.Errorf("user not found: %w", err)
	}

	if !user.IsActive {
		return nil, "", "", fmt.Errorf("user account is deactivated")
	}

	// 3. Generate new tokens
	newAccessToken, err := s.GenerateAccessToken(user.ID, user.Email)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to generate access token: %w", err)
	}

	newRefreshToken, err := s.GenerateRefreshToken(user.ID)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// 4. Store new refresh token BEFORE revoking old one.
	// If store fails, old token remains valid — user can retry.
	// If we revoked first and store failed, user would be locked out.
	if err := s.StoreRefreshToken(ctx, user.ID, newRefreshToken); err != nil {
		return nil, "", "", fmt.Errorf("failed to store new refresh token: %w", err)
	}

	// 5. Revoke old token (non-fatal: new token is already stored safely)
	if err := s.RevokeRefreshToken(ctx, oldToken); err != nil {
		// Log but don't fail — old token will expire naturally (30 days)
		slog.Warn("failed to revoke old refresh token", "error", err)
	}

	return user, newAccessToken, newRefreshToken, nil
}

// RevokeRefreshToken revokes a refresh token
func (s *AuthService) RevokeRefreshToken(ctx context.Context, token string) error {
	query := `
		UPDATE public.refresh_tokens
		SET revoked = TRUE
		WHERE token = $1
	`

	_, err := repository.DB.Exec(ctx, query, token)
	return err
}

// RevokeAllUserTokens revokes all refresh tokens for a user
func (s *AuthService) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error {
	query := `
		UPDATE public.refresh_tokens
		SET revoked = TRUE
		WHERE user_id = $1
	`

	_, err := repository.DB.Exec(ctx, query, userID)
	return err
}

// UpdateLastLogin updates the user's last login timestamp
func (s *AuthService) UpdateLastLogin(ctx context.Context, userID uuid.UUID) error {
	query := `
		UPDATE public.users
		SET last_login_at = NOW()
		WHERE id = $1
	`

	_, err := repository.DB.Exec(ctx, query, userID)
	return err
}

// ValidateAccessToken validates a JWT access token and returns claims
func (s *AuthService) ValidateAccessToken(tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Verify token type
	if tokenType, ok := claims["type"].(string); !ok || tokenType != "access" {
		return nil, fmt.Errorf("invalid token type")
	}

	return claims, nil
}

// EnsureResetTokenTableExists ensures the password reset tokens table exists
func (s *AuthService) EnsureResetTokenTableExists(ctx context.Context) error {
	query := `
		CREATE TABLE IF NOT EXISTS public.password_reset_tokens (
			token TEXT PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
			expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user_id ON public.password_reset_tokens(user_id);
	`
	_, err := repository.DB.Exec(ctx, query)
	return err
}

// CreatePasswordResetToken generates a reset token for a user
func (s *AuthService) CreatePasswordResetToken(ctx context.Context, email string) (string, error) {
	// 1. Find user by email
	user, err := s.GetUserByEmail(ctx, email)
	if err != nil {
		return "", fmt.Errorf("user not found")
	}

	// 2. Generate random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)

	// 3. Store in DB (expires in 1 hour)
	expiresAt := time.Now().Add(1 * time.Hour)
	query := `
		INSERT INTO public.password_reset_tokens (token, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	_, err = repository.DB.Exec(ctx, query, token, user.ID, expiresAt)
	if err != nil {
		return "", fmt.Errorf("failed to store token: %w", err)
	}

	return token, nil
}

// ResetPasswordWithToken resets the user's password using a valid token
func (s *AuthService) ResetPasswordWithToken(ctx context.Context, token, newPassword string) error {
	// 1. Verify token
	query := `
		SELECT user_id 
		FROM public.password_reset_tokens 
		WHERE token = $1 AND expires_at > NOW()
	`
	var userID uuid.UUID
	err := repository.DB.QueryRow(ctx, query, token).Scan(&userID)
	if err != nil {
		return fmt.Errorf("invalid or expired token")
	}

	// 2. Hash new password
	passwordHash, err := s.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// 3. Update user password
	updateQuery := `
		UPDATE public.users 
		SET password_hash = $1, updated_at = NOW() 
		WHERE id = $2
	`
	_, err = repository.DB.Exec(ctx, updateQuery, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	// 4. Delete the used token (and any other tokens for this user, optional but safer)
	deleteQuery := `
		DELETE FROM public.password_reset_tokens 
		WHERE user_id = $1
	`
	_, _ = repository.DB.Exec(ctx, deleteQuery, userID)

	return nil
}

// IncrementFailedLoginAttempts increments the failed login count and locks the account if necessary
func (s *AuthService) IncrementFailedLoginAttempts(ctx context.Context, userID uuid.UUID) error {
	// Logic: Increment count. If count >= 5, set locked_until = NOW() + 15m.
	// We use 5 attempts as the threshold and 15 minutes as lock duration.
	query := `
		UPDATE public.users 
		SET failed_login_attempts = failed_login_attempts + 1,
			locked_until = CASE 
				WHEN failed_login_attempts + 1 >= 5 THEN NOW() + INTERVAL '15 minutes' 
				ELSE locked_until 
			END
		WHERE id = $1
	`
	_, err := repository.DB.Exec(ctx, query, userID)
	return err
}

// ResetFailedLoginAttempts resets failed attempts on successful login
func (s *AuthService) ResetFailedLoginAttempts(ctx context.Context, userID uuid.UUID) error {
	query := "UPDATE public.users SET failed_login_attempts = 0, locked_until = NULL WHERE id = $1"
	_, err := repository.DB.Exec(ctx, query, userID)
	return err
}
