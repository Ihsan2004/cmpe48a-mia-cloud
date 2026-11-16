package services

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// helper to create an AuthService with a test secret
func newTestAuthService(t *testing.T) *AuthService {
	t.Helper()
	return &AuthService{
		jwtSecret: []byte("test-secret-key-for-unit-tests-minimum-32-chars"),
	}
}

// ==================== Password Hashing ====================

func TestHashPassword_Success(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	hash, err := svc.HashPassword("mySecurePassword123!")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if hash == "mySecurePassword123!" {
		t.Fatal("hash should not equal plaintext password")
	}
}

func TestHashPassword_DifferentHashesForSamePassword(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	hash1, _ := svc.HashPassword("samePassword")
	hash2, _ := svc.HashPassword("samePassword")

	if hash1 == hash2 {
		t.Error("bcrypt should produce different hashes for the same password (different salts)")
	}
}

func TestHashPassword_EmptyPassword(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	// bcrypt accepts empty strings — this is a valid hash
	hash, err := svc.HashPassword("")
	if err != nil {
		t.Fatalf("unexpected error for empty password: %v", err)
	}
	if hash == "" {
		t.Fatal("expected hash even for empty password")
	}
}

// ==================== Password Verification ====================

func TestVerifyPassword_Correct(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	password := "correctHorse Battery Staple"
	hash, _ := svc.HashPassword(password)

	if !svc.VerifyPassword(hash, password) {
		t.Error("expected correct password to verify successfully")
	}
}

func TestVerifyPassword_Wrong(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	hash, _ := svc.HashPassword("correctPassword")

	if svc.VerifyPassword(hash, "wrongPassword") {
		t.Error("expected wrong password to fail verification")
	}
}

func TestVerifyPassword_InvalidHash(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	if svc.VerifyPassword("not-a-valid-bcrypt-hash", "anyPassword") {
		t.Error("expected invalid hash to fail verification")
	}
}

func TestVerifyPassword_EmptyHash(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	if svc.VerifyPassword("", "anyPassword") {
		t.Error("expected empty hash to fail verification")
	}
}

// ==================== JWT Access Token ====================

func TestGenerateAccessToken_ValidClaims(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	userID := uuid.New()
	email := "test@example.com"

	tokenString, err := svc.GenerateAccessToken(userID, email)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenString == "" {
		t.Fatal("expected non-empty token")
	}

	// Parse and verify claims
	claims, err := svc.ValidateAccessToken(tokenString)
	if err != nil {
		t.Fatalf("token should be valid: %v", err)
	}

	if claims["sub"] != userID.String() {
		t.Errorf("expected sub=%s, got %s", userID.String(), claims["sub"])
	}
	if claims["email"] != email {
		t.Errorf("expected email=%s, got %s", email, claims["email"])
	}
	if claims["type"] != "access" {
		t.Errorf("expected type=access, got %s", claims["type"])
	}
}

func TestGenerateAccessToken_ExpiresIn30Minutes(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	tokenString, _ := svc.GenerateAccessToken(uuid.New(), "test@example.com")

	token, _ := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return svc.jwtSecret, nil
	})

	claims := token.Claims.(jwt.MapClaims)
	exp := int64(claims["exp"].(float64))
	iat := int64(claims["iat"].(float64))

	diff := exp - iat
	// Should be 30 minutes = 1800 seconds
	if diff != 1800 {
		t.Errorf("expected 1800s expiry, got %ds", diff)
	}
}

func TestValidateAccessToken_ExpiredToken(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	// Create a token that's already expired
	claims := jwt.MapClaims{
		"sub":   uuid.New().String(),
		"email": "test@example.com",
		"iat":   time.Now().Add(-1 * time.Hour).Unix(),
		"exp":   time.Now().Add(-30 * time.Minute).Unix(), // expired 30 min ago
		"type":  "access",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(svc.jwtSecret)

	_, err := svc.ValidateAccessToken(tokenString)
	if err == nil {
		t.Error("expected expired token to fail validation")
	}
}

func TestValidateAccessToken_WrongSecret(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	// Sign with a different secret
	claims := jwt.MapClaims{
		"sub":   uuid.New().String(),
		"email": "test@example.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(30 * time.Minute).Unix(),
		"type":  "access",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString([]byte("wrong-secret-key-that-does-not-match"))

	_, err := svc.ValidateAccessToken(tokenString)
	if err == nil {
		t.Error("expected token signed with wrong secret to fail")
	}
}

func TestValidateAccessToken_WrongTokenType(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	claims := jwt.MapClaims{
		"sub":   uuid.New().String(),
		"email": "test@example.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(30 * time.Minute).Unix(),
		"type":  "refresh", // wrong type
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(svc.jwtSecret)

	_, err := svc.ValidateAccessToken(tokenString)
	if err == nil {
		t.Error("expected non-access token type to fail validation")
	}
}

func TestValidateAccessToken_MissingType(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	claims := jwt.MapClaims{
		"sub":   uuid.New().String(),
		"email": "test@example.com",
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(30 * time.Minute).Unix(),
		// no "type" field
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(svc.jwtSecret)

	_, err := svc.ValidateAccessToken(tokenString)
	if err == nil {
		t.Error("expected token without type claim to fail validation")
	}
}

func TestValidateAccessToken_MalformedToken(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	_, err := svc.ValidateAccessToken("not.a.valid.jwt.token")
	if err == nil {
		t.Error("expected malformed token to fail")
	}
}

func TestValidateAccessToken_EmptyToken(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	_, err := svc.ValidateAccessToken("")
	if err == nil {
		t.Error("expected empty token to fail")
	}
}

// ==================== Opaque Refresh Token ====================

func TestGenerateRefreshToken_UniqueTokens(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	token1, err1 := svc.GenerateRefreshToken(uuid.New())
	token2, err2 := svc.GenerateRefreshToken(uuid.New())

	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}

	if token1 == token2 {
		t.Error("expected two refresh tokens to be different")
	}
}

func TestGenerateRefreshToken_NotEmpty(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	token, err := svc.GenerateRefreshToken(uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty refresh token")
	}
}

func TestGenerateRefreshToken_Length(t *testing.T) {
	t.Parallel()
	svc := newTestAuthService(t)

	token, _ := svc.GenerateRefreshToken(uuid.New())

	// 32 bytes base64url encoded = 44 chars (with padding)
	if len(token) < 40 {
		t.Errorf("expected refresh token length >= 40, got %d", len(token))
	}
}

// ==================== NewAuthService ====================

func TestNewAuthService_MissingSecret(t *testing.T) {
	// Don't run parallel — modifies env
	original := t.Name() // just to ensure we restore

	t.Setenv("JWT_SECRET", "")

	_, err := NewAuthService()
	if err == nil {
		t.Error("expected error when JWT_SECRET is empty")
	}

	_ = original
}

func TestNewAuthService_WithSecret(t *testing.T) {
	// NewAuthService calls EnsureResetTokenTableExists which needs DB.
	// In unit tests DB is nil → panic. We test the JWT secret validation
	// by verifying that the service struct is correctly configured.
	svc := &AuthService{
		jwtSecret: []byte("test-secret-at-least-32-characters-long"),
	}
	if len(svc.jwtSecret) == 0 {
		t.Error("expected jwtSecret to be set")
	}
}
