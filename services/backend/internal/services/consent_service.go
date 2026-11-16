package services

import (
	"context"
	"errors"
	"fmt"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ConsentStatus describes whether a user has fulfilled all mandatory
// consents at their current versions. Drives the post-OAuth gate and
// the version-bump re-prompt flow.
type ConsentStatus struct {
	// Satisfied is true when there is nothing in Missing, Outdated,
	// or Revoked. Frontend can treat this as the green-light flag.
	Satisfied bool `json:"satisfied"`
	// Missing — mandatory types the user has never recorded.
	Missing []string `json:"missing,omitempty"`
	// Outdated — mandatory types where the latest record uses an
	// older document version than CurrentVersion.
	Outdated []OutdatedConsent `json:"outdated,omitempty"`
	// Revoked — mandatory types where the latest record is either
	// granted=false or has a non-null revoked_at.
	Revoked []string `json:"revoked,omitempty"`
}

type OutdatedConsent struct {
	Type            string `json:"type"`
	AcceptedVersion string `json:"accepted_version"`
	CurrentVersion  string `json:"current_version"`
}

// ErrCannotRevokeMandatory — user tried to revoke a consent that's
// mandatory to use the service. Surface as 400.
var ErrCannotRevokeMandatory = errors.New("cannot revoke mandatory consent")

// Current document versions. Bump these in lock-step with the
// "Sürüm: X.Y" line in the corresponding frontend page. The service
// rejects signup payloads that don't match — this prevents a stale
// client (e.g. a cached tab) from silently accepting old wording.
const (
	CurrentTermsVersion           = "1.0"
	CurrentPrivacyVersion         = "1.0"
	CurrentKVKKVersion            = "1.0"
	CurrentCrossBorderVersion     = "1.0"
	CurrentAIProcessingVersion    = "1.0"
	CurrentPilotSozlesmesiVersion = "1.0"
	CurrentMarketingVersion       = "1.0"
)

// Document URLs the user landed on when consenting — stored alongside
// each row so we can show "you accepted version X at URL Y" in audit.
var consentDocumentURLs = map[string]string{
	models.ConsentTypeTermsOfService:      "/terms-of-service",
	models.ConsentTypePrivacyPolicy:       "/privacy-policy",
	models.ConsentTypeKVKKAydinlatma:      "/kvkk",
	models.ConsentTypeCrossBorderTransfer: "/kvkk",
	models.ConsentTypeAIProcessing:        "/privacy-policy",
	models.ConsentTypePilotSozlesmesi:     "/pilot-sozlesmesi",
	models.ConsentTypeMarketing:           "/privacy-policy",
}

// ErrMissingMandatoryConsent — at least one consent in
// MandatoryConsentTypes is absent or granted=false. Surface as 400.
var ErrMissingMandatoryConsent = errors.New("missing mandatory consent")

// ErrConsentVersionMismatch — client submitted a stale version.
// Surface as 409 (Conflict) — frontend should reload and re-prompt.
var ErrConsentVersionMismatch = errors.New("consent version mismatch")

// ErrInvalidConsentType — client posted a type outside the enum.
var ErrInvalidConsentType = errors.New("invalid consent type")

type ConsentService struct {
	repo repository.UserConsentRepository
}

func NewConsentService() *ConsentService {
	return &ConsentService{
		repo: repository.NewUserConsentRepository(),
	}
}

// CurrentVersion returns the expected version for a consent type, or
// an empty string for unknown types (caller is expected to validate
// the type via models.IsValidConsentType first).
func CurrentVersion(consentType string) string {
	switch consentType {
	case models.ConsentTypeTermsOfService:
		return CurrentTermsVersion
	case models.ConsentTypePrivacyPolicy:
		return CurrentPrivacyVersion
	case models.ConsentTypeKVKKAydinlatma:
		return CurrentKVKKVersion
	case models.ConsentTypeCrossBorderTransfer:
		return CurrentCrossBorderVersion
	case models.ConsentTypeAIProcessing:
		return CurrentAIProcessingVersion
	case models.ConsentTypePilotSozlesmesi:
		return CurrentPilotSozlesmesiVersion
	case models.ConsentTypeMarketing:
		return CurrentMarketingVersion
	}
	return ""
}

// ValidateSignupConsents checks the payload meets the signup contract:
// every type in MandatoryConsentTypes is present with granted=true and
// the current version. Marketing is optional. Unknown types reject.
//
// Returns a sentinel error so the handler can map to HTTP status.
func (s *ConsentService) ValidateSignupConsents(inputs []models.SignupConsentInput) error {
	seen := make(map[string]models.SignupConsentInput, len(inputs))
	for _, in := range inputs {
		if !models.IsValidConsentType(in.Type) {
			return fmt.Errorf("%w: %q", ErrInvalidConsentType, in.Type)
		}
		if _, dup := seen[in.Type]; dup {
			return fmt.Errorf("duplicate consent type in payload: %q", in.Type)
		}
		seen[in.Type] = in
	}

	for _, required := range models.MandatoryConsentTypes {
		in, ok := seen[required]
		if !ok {
			return fmt.Errorf("%w: %s", ErrMissingMandatoryConsent, required)
		}
		if !in.Granted {
			return fmt.Errorf("%w: %s (granted=false)", ErrMissingMandatoryConsent, required)
		}
		expected := CurrentVersion(required)
		if in.Version != expected {
			return fmt.Errorf("%w: %s (got %q, expected %q)", ErrConsentVersionMismatch, required, in.Version, expected)
		}
	}

	return nil
}

// RecordSignupConsents persists the validated payload as
// user_consents rows sharing a single event_group_id. Caller MUST
// invoke ValidateSignupConsents first; this method only checks
// versions, not mandatory-presence (validation already did that).
//
// ipAddress and userAgent are best-effort audit metadata.
func (s *ConsentService) RecordSignupConsents(
	ctx context.Context,
	userID uuid.UUID,
	inputs []models.SignupConsentInput,
	ipAddress, userAgent *string,
) ([]*models.UserConsent, error) {
	groupID := uuid.New()
	consents := make([]*models.UserConsent, 0, len(inputs))

	for _, in := range inputs {
		if !models.IsValidConsentType(in.Type) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidConsentType, in.Type)
		}
		url := consentDocumentURLs[in.Type]
		consents = append(consents, &models.UserConsent{
			UserID:          userID,
			ConsentType:     in.Type,
			DocumentVersion: in.Version,
			DocumentURL:     stringPtrOrNil(url),
			Granted:         in.Granted,
			IPAddress:       ipAddress,
			UserAgent:       userAgent,
			EventGroupID:    &groupID,
		})
	}

	return s.repo.CreateBatch(ctx, consents)
}

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RecordConsents persists arbitrary consent inputs for an already-
// authenticated user. Unlike RecordSignupConsents, it does NOT require
// all MandatoryConsentTypes to be present — useful for post-OAuth
// recording, version-bump re-acceptance, or standalone marketing
// opt-in. Validates each type and version; rejects duplicates.
//
// Returns ErrInvalidConsentType / ErrConsentVersionMismatch (sentinel)
// for client errors so handlers can map to HTTP status.
func (s *ConsentService) RecordConsents(
	ctx context.Context,
	userID uuid.UUID,
	inputs []models.SignupConsentInput,
	ipAddress, userAgent *string,
) ([]*models.UserConsent, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("at least one consent is required")
	}

	seen := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		if !models.IsValidConsentType(in.Type) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidConsentType, in.Type)
		}
		if seen[in.Type] {
			return nil, fmt.Errorf("duplicate consent type in payload: %q", in.Type)
		}
		seen[in.Type] = true
		expected := CurrentVersion(in.Type)
		if in.Version != expected {
			return nil, fmt.Errorf("%w: %s (got %q, expected %q)", ErrConsentVersionMismatch, in.Type, in.Version, expected)
		}
	}

	groupID := uuid.New()
	consents := make([]*models.UserConsent, 0, len(inputs))
	for _, in := range inputs {
		url := consentDocumentURLs[in.Type]
		consents = append(consents, &models.UserConsent{
			UserID:          userID,
			ConsentType:     in.Type,
			DocumentVersion: in.Version,
			DocumentURL:     stringPtrOrNil(url),
			Granted:         in.Granted,
			IPAddress:       ipAddress,
			UserAgent:       userAgent,
			EventGroupID:    &groupID,
		})
	}

	return s.repo.CreateBatch(ctx, consents)
}

// ListUserConsents returns every consent row for a user, newest
// first. Powers the Settings page audit list and the admin endpoint.
func (s *ConsentService) ListUserConsents(ctx context.Context, userID uuid.UUID) ([]*models.UserConsent, error) {
	return s.repo.ListByUser(ctx, userID)
}

// GetConsentStatus computes the gate state for a user: are all
// mandatory consents present, current, and granted? Missing/Outdated/
// Revoked slices tell the frontend what to re-prompt for.
//
// Walks MandatoryConsentTypes deterministically so the output order
// is stable across calls (helps with caching and assertions).
func (s *ConsentService) GetConsentStatus(ctx context.Context, userID uuid.UUID) (*ConsentStatus, error) {
	status := &ConsentStatus{
		Missing:  []string{},
		Outdated: []OutdatedConsent{},
		Revoked:  []string{},
	}

	for _, required := range models.MandatoryConsentTypes {
		latest, err := s.repo.GetLatestByType(ctx, userID, required)
		if err != nil {
			if errors.Is(err, repository.ErrConsentNotFound) {
				status.Missing = append(status.Missing, required)
				continue
			}
			return nil, fmt.Errorf("check consent %s: %w", required, err)
		}

		// Revoked or explicitly denied → re-prompt.
		if !latest.IsActive() {
			status.Revoked = append(status.Revoked, required)
			continue
		}

		// Older version on file → re-prompt at new version.
		if expected := CurrentVersion(required); latest.DocumentVersion != expected {
			status.Outdated = append(status.Outdated, OutdatedConsent{
				Type:            required,
				AcceptedVersion: latest.DocumentVersion,
				CurrentVersion:  expected,
			})
		}
	}

	status.Satisfied = len(status.Missing) == 0 && len(status.Outdated) == 0 && len(status.Revoked) == 0
	return status, nil
}

// RevokeConsent withdraws the user's currently-active consent of the
// given type. Disallowed for MandatoryConsentTypes — those can only
// be lifted by deleting the account. Returns ErrCannotRevokeMandatory
// (sentinel) for that case; repository.ErrConsentNotFound when there
// is nothing active to revoke.
func (s *ConsentService) RevokeConsent(ctx context.Context, userID uuid.UUID, consentType, reason string) (*models.UserConsent, error) {
	if !models.IsValidConsentType(consentType) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidConsentType, consentType)
	}
	for _, mandatory := range models.MandatoryConsentTypes {
		if consentType == mandatory {
			return nil, fmt.Errorf("%w: %s (delete the account instead)", ErrCannotRevokeMandatory, consentType)
		}
	}
	return s.repo.Revoke(ctx, userID, consentType, reason)
}
