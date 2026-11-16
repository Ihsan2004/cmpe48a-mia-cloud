package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrConsentNotFound is returned by GetLatestByType / Revoke when no
// matching row exists. Callers can distinguish "user never consented"
// (return Active=false) from "infrastructure error" (propagate).
var ErrConsentNotFound = errors.New("consent not found")

type UserConsentRepository interface {
	// Create inserts a single consent action. Use this for one-off
	// updates like marketing opt-in from the settings page.
	Create(ctx context.Context, consent *models.UserConsent) (*models.UserConsent, error)

	// CreateBatch inserts multiple consents in a single transaction.
	// All inserted rows share the same event_group_id if the caller
	// sets one — used at signup to group the mandatory bundle.
	CreateBatch(ctx context.Context, consents []*models.UserConsent) ([]*models.UserConsent, error)

	// GetLatestByType returns the most recent consent row for a
	// (user, type) regardless of granted/revoked state. Returns
	// ErrConsentNotFound when the user has never recorded this type.
	GetLatestByType(ctx context.Context, userID uuid.UUID, consentType string) (*models.UserConsent, error)

	// ListByUser returns every consent row for a user, newest first.
	// Used by /settings/privacy and the admin audit endpoint.
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*models.UserConsent, error)

	// Revoke marks the latest ACTIVE consent (granted=true,
	// revoked_at IS NULL) of the given type as revoked. Returns
	// ErrConsentNotFound when there is nothing active to revoke.
	Revoke(ctx context.Context, userID uuid.UUID, consentType, reason string) (*models.UserConsent, error)
}

type postgresUserConsentRepository struct{}

func NewUserConsentRepository() UserConsentRepository {
	return &postgresUserConsentRepository{}
}

// ip_address is INET in Postgres but pgx v5 won't scan its binary
// representation into a *string — `cannot scan inet (OID 869) in binary
// format into **string`. host() casts it to TEXT (stripping the /32
// CIDR suffix that the audit log doesn't need anyway), which scans into
// *string cleanly.
const userConsentColumns = `id, user_id, consent_type, document_version, document_url,
		granted, accepted_at, revoked_at, revoke_reason,
		host(ip_address) AS ip_address, user_agent, event_group_id, metadata, created_at`

func scanUserConsent(row pgx.Row) (*models.UserConsent, error) {
	var c models.UserConsent
	// metadata JSONB → []byte → json.RawMessage via assignment below.
	var metadata []byte
	err := row.Scan(
		&c.ID, &c.UserID, &c.ConsentType, &c.DocumentVersion, &c.DocumentURL,
		&c.Granted, &c.AcceptedAt, &c.RevokedAt, &c.RevokeReason,
		&c.IPAddress, &c.UserAgent, &c.EventGroupID, &metadata, &c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if len(metadata) > 0 {
		c.Metadata = json.RawMessage(metadata)
	}
	return &c, nil
}

func (r *postgresUserConsentRepository) Create(ctx context.Context, consent *models.UserConsent) (*models.UserConsent, error) {
	if !models.IsValidConsentType(consent.ConsentType) {
		return nil, fmt.Errorf("invalid consent_type: %q", consent.ConsentType)
	}
	if consent.DocumentVersion == "" {
		return nil, fmt.Errorf("document_version is required")
	}

	metadata := consent.Metadata
	if metadata == nil {
		metadata = json.RawMessage("{}")
	}

	row := DB.QueryRow(ctx, `
		INSERT INTO user_consents (
			user_id, consent_type, document_version, document_url,
			granted, ip_address, user_agent, event_group_id, metadata
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+userConsentColumns+`
	`,
		consent.UserID, consent.ConsentType, consent.DocumentVersion, consent.DocumentURL,
		consent.Granted, consent.IPAddress, consent.UserAgent, consent.EventGroupID, []byte(metadata),
	)
	return scanUserConsent(row)
}

func (r *postgresUserConsentRepository) CreateBatch(ctx context.Context, consents []*models.UserConsent) ([]*models.UserConsent, error) {
	if len(consents) == 0 {
		return nil, nil
	}
	for _, c := range consents {
		if !models.IsValidConsentType(c.ConsentType) {
			return nil, fmt.Errorf("invalid consent_type in batch: %q", c.ConsentType)
		}
		if c.DocumentVersion == "" {
			return nil, fmt.Errorf("document_version is required for type %q", c.ConsentType)
		}
	}

	tx, err := DB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	inserted := make([]*models.UserConsent, 0, len(consents))
	for _, c := range consents {
		metadata := c.Metadata
		if metadata == nil {
			metadata = json.RawMessage("{}")
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO user_consents (
				user_id, consent_type, document_version, document_url,
				granted, ip_address, user_agent, event_group_id, metadata
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING `+userConsentColumns+`
		`,
			c.UserID, c.ConsentType, c.DocumentVersion, c.DocumentURL,
			c.Granted, c.IPAddress, c.UserAgent, c.EventGroupID, []byte(metadata),
		)
		created, err := scanUserConsent(row)
		if err != nil {
			return nil, fmt.Errorf("insert consent %q: %w", c.ConsentType, err)
		}
		inserted = append(inserted, created)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit consent batch: %w", err)
	}
	return inserted, nil
}

func (r *postgresUserConsentRepository) GetLatestByType(ctx context.Context, userID uuid.UUID, consentType string) (*models.UserConsent, error) {
	if !models.IsValidConsentType(consentType) {
		return nil, fmt.Errorf("invalid consent_type: %q", consentType)
	}
	row := DB.QueryRow(ctx, `
		SELECT `+userConsentColumns+`
		FROM user_consents
		WHERE user_id = $1 AND consent_type = $2
		ORDER BY accepted_at DESC
		LIMIT 1
	`, userID, consentType)

	consent, err := scanUserConsent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConsentNotFound
		}
		return nil, err
	}
	return consent, nil
}

func (r *postgresUserConsentRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]*models.UserConsent, error) {
	rows, err := DB.Query(ctx, `
		SELECT `+userConsentColumns+`
		FROM user_consents
		WHERE user_id = $1
		ORDER BY accepted_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list consents: %w", err)
	}
	defer rows.Close()

	consents := make([]*models.UserConsent, 0)
	for rows.Next() {
		consent, err := scanUserConsent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan consent row: %w", err)
		}
		consents = append(consents, consent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate consent rows: %w", err)
	}
	return consents, nil
}

func (r *postgresUserConsentRepository) Revoke(ctx context.Context, userID uuid.UUID, consentType, reason string) (*models.UserConsent, error) {
	if !models.IsValidConsentType(consentType) {
		return nil, fmt.Errorf("invalid consent_type: %q", consentType)
	}
	// UPDATE the single most-recent ACTIVE row (granted=true,
	// revoked_at IS NULL). The subquery + LIMIT 1 prevents revoking
	// older rows if duplicates somehow exist.
	row := DB.QueryRow(ctx, `
		UPDATE user_consents
		SET revoked_at = NOW(),
		    revoke_reason = $3
		WHERE id = (
			SELECT id FROM user_consents
			WHERE user_id = $1 AND consent_type = $2
			  AND granted = TRUE AND revoked_at IS NULL
			ORDER BY accepted_at DESC
			LIMIT 1
		)
		RETURNING `+userConsentColumns,
		userID, consentType, reason,
	)

	consent, err := scanUserConsent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConsentNotFound
		}
		return nil, err
	}
	return consent, nil
}
