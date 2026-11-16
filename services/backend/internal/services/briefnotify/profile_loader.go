package briefnotify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DBProfilePreferenceLoader reads brief-delivery toggles directly from
// the profiles table. Kept in this package (instead of reusing
// services.ProfileService) so the dispatcher stays free of the heavy
// ProfileService dependency graph — the only fields we actually care
// about are three booleans plus a timestamp.
type DBProfilePreferenceLoader struct{}

// NewDBProfilePreferenceLoader returns a loader backed by the package-
// global repository.DB pool. Callers must have already invoked
// repository.Connect().
func NewDBProfilePreferenceLoader() *DBProfilePreferenceLoader {
	return &DBProfilePreferenceLoader{}
}

// LoadBriefDeliveryPreferences fetches just the three columns needed
// for channel routing. A missing profiles row is treated as "no
// preferences set" — both channels off — rather than an error, so the
// dispatcher can safely call us for freshly-created users.
func (l *DBProfilePreferenceLoader) LoadBriefDeliveryPreferences(ctx context.Context, userID uuid.UUID) (BriefDeliveryPreferences, error) {
	if userID == uuid.Nil {
		return BriefDeliveryPreferences{}, fmt.Errorf("userID cannot be nil")
	}
	if repository.DB == nil {
		return BriefDeliveryPreferences{}, fmt.Errorf("repository.DB is not initialised")
	}

	var (
		emailEnabled  bool
		slackEnabled  bool
		unsubscribedAt *time.Time
	)
	err := repository.DB.QueryRow(ctx, `
		SELECT COALESCE(brief_deliver_via_email, false),
		       COALESCE(brief_deliver_via_slack, false),
		       email_unsubscribed_at
		FROM profiles
		WHERE id = $1
	`, userID).Scan(&emailEnabled, &slackEnabled, &unsubscribedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BriefDeliveryPreferences{}, nil
		}
		return BriefDeliveryPreferences{}, fmt.Errorf("failed to load delivery preferences: %w", err)
	}

	return BriefDeliveryPreferences{
		EmailEnabled:        emailEnabled,
		SlackEnabled:        slackEnabled,
		EmailUnsubscribedAt: unsubscribedAt,
	}, nil
}
