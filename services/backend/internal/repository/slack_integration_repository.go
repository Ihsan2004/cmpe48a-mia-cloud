package repository

import (
	"context"
	"errors"
	"fmt"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SlackIntegrationRepository persists per-user Slack workspace bot tokens.
// The encrypted bot_token is treated as an opaque string here — encryption
// is the caller's responsibility (briefnotify encryption helper in Phase 4).
type SlackIntegrationRepository interface {
	Upsert(ctx context.Context, integration *models.SlackIntegration) error
	GetActiveForUser(ctx context.Context, userID uuid.UUID) (*models.SlackIntegration, error)
	GetByUserAndTeam(ctx context.Context, userID uuid.UUID, teamID string) (*models.SlackIntegration, error)
	Revoke(ctx context.Context, id uuid.UUID) error
	UpdateDMChannel(ctx context.Context, id uuid.UUID, dmChannelID string, matchedEmail string) error
}

type postgresSlackIntegrationRepository struct{}

func NewSlackIntegrationRepository() SlackIntegrationRepository {
	return &postgresSlackIntegrationRepository{}
}

// Upsert inserts or updates on UNIQUE (user_id, team_id). A re-install
// into the same workspace clears revoked_at so the integration becomes
// active again without having to delete the row.
func (r *postgresSlackIntegrationRepository) Upsert(ctx context.Context, integration *models.SlackIntegration) error {
	if integration == nil {
		return fmt.Errorf("integration cannot be nil")
	}
	if integration.UserID == uuid.Nil {
		return fmt.Errorf("integration.UserID cannot be nil")
	}
	if integration.TeamID == "" {
		return fmt.Errorf("integration.TeamID cannot be empty")
	}
	if integration.BotUserID == "" {
		return fmt.Errorf("integration.BotUserID cannot be empty")
	}
	if integration.BotTokenEncrypted == "" {
		return fmt.Errorf("integration.BotTokenEncrypted cannot be empty")
	}
	if integration.ID == uuid.Nil {
		integration.ID = uuid.New()
	}

	query := `
		INSERT INTO slack_integrations (
			id, user_id, team_id, team_name, bot_user_id,
			bot_token_encrypted, dm_channel_id, matched_email, scope, installed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (user_id, team_id) DO UPDATE SET
		    team_name = EXCLUDED.team_name,
		    bot_user_id = EXCLUDED.bot_user_id,
		    bot_token_encrypted = EXCLUDED.bot_token_encrypted,
		    dm_channel_id = EXCLUDED.dm_channel_id,
		    matched_email = EXCLUDED.matched_email,
		    scope = EXCLUDED.scope,
		    installed_at = NOW(),
		    revoked_at = NULL
		RETURNING id, installed_at
	`
	err := DB.QueryRow(ctx, query,
		integration.ID, integration.UserID, integration.TeamID, integration.TeamName,
		integration.BotUserID, integration.BotTokenEncrypted, integration.DMChannelID,
		integration.MatchedEmail, integration.Scope,
	).Scan(&integration.ID, &integration.InstalledAt)
	if err != nil {
		return fmt.Errorf("failed to upsert slack_integrations: %w", err)
	}
	integration.RevokedAt = nil
	return nil
}

func (r *postgresSlackIntegrationRepository) GetActiveForUser(ctx context.Context, userID uuid.UUID) (*models.SlackIntegration, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("userID cannot be nil")
	}
	return r.queryOne(ctx, `
		SELECT id, user_id, team_id, team_name, bot_user_id, bot_token_encrypted,
		       dm_channel_id, matched_email, scope, installed_at, revoked_at
		FROM slack_integrations
		WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY installed_at DESC
		LIMIT 1
	`, userID)
}

func (r *postgresSlackIntegrationRepository) GetByUserAndTeam(ctx context.Context, userID uuid.UUID, teamID string) (*models.SlackIntegration, error) {
	if userID == uuid.Nil || teamID == "" {
		return nil, fmt.Errorf("userID + teamID required")
	}
	return r.queryOne(ctx, `
		SELECT id, user_id, team_id, team_name, bot_user_id, bot_token_encrypted,
		       dm_channel_id, matched_email, scope, installed_at, revoked_at
		FROM slack_integrations
		WHERE user_id = $1 AND team_id = $2
	`, userID, teamID)
}

func (r *postgresSlackIntegrationRepository) queryOne(ctx context.Context, sql string, args ...any) (*models.SlackIntegration, error) {
	var s models.SlackIntegration
	err := DB.QueryRow(ctx, sql, args...).Scan(
		&s.ID, &s.UserID, &s.TeamID, &s.TeamName, &s.BotUserID, &s.BotTokenEncrypted,
		&s.DMChannelID, &s.MatchedEmail, &s.Scope, &s.InstalledAt, &s.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read slack_integrations row: %w", err)
	}
	return &s, nil
}

// Revoke sets revoked_at=NOW(). Called both from the explicit disconnect
// handler and automatically when Slack returns invalid_auth (Karar #18).
func (r *postgresSlackIntegrationRepository) Revoke(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return fmt.Errorf("id cannot be nil")
	}
	if _, err := DB.Exec(ctx, `
		UPDATE slack_integrations
		SET revoked_at = NOW()
		WHERE id = $1 AND revoked_at IS NULL
	`, id); err != nil {
		return fmt.Errorf("failed to revoke slack_integration: %w", err)
	}
	return nil
}

// UpdateDMChannel caches the resolved DM channel id + matched email used
// to resolve it. Called during OAuth callback (Phase 4) and alternative
// email flow — once set, every delivery reuses these cached values.
func (r *postgresSlackIntegrationRepository) UpdateDMChannel(ctx context.Context, id uuid.UUID, dmChannelID string, matchedEmail string) error {
	if id == uuid.Nil {
		return fmt.Errorf("id cannot be nil")
	}
	var dm, me *string
	if dmChannelID != "" {
		dm = &dmChannelID
	}
	if matchedEmail != "" {
		me = &matchedEmail
	}
	if _, err := DB.Exec(ctx, `
		UPDATE slack_integrations
		SET dm_channel_id = COALESCE($2, dm_channel_id),
		    matched_email = COALESCE($3, matched_email)
		WHERE id = $1
	`, id, dm, me); err != nil {
		return fmt.Errorf("failed to update slack_integration dm/email: %w", err)
	}
	return nil
}
