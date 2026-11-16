package briefnotify

import (
	"context"
	"errors"
	"fmt"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DBContextLoader resolves BriefDeliverContext directly from the
// database. Lives in this package (alongside the dispatcher) so the
// worker only has to wire a single briefnotify.* dependency. Callers
// must pass the same repository interfaces the dispatcher already owns.
type DBContextLoader struct {
	briefRepo   repository.BriefRepository
	meetingRepo repository.MeetingRepository
}

// NewDBContextLoader wires the loader against the production repo
// implementations. Unit tests should satisfy ContextLoader directly with
// a stub rather than calling this constructor.
func NewDBContextLoader(briefRepo repository.BriefRepository, meetingRepo repository.MeetingRepository) *DBContextLoader {
	return &DBContextLoader{briefRepo: briefRepo, meetingRepo: meetingRepo}
}

// Load fetches every piece of data a deliverer needs: meeting, brief,
// user row (email + name), and profile (timezone + language + delivery
// preferences). All lookups are individual queries — the volume here is
// at most one per delivery job, so no batching is needed.
func (l *DBContextLoader) Load(ctx context.Context, delivery *models.BriefDelivery) (BriefDeliverContext, error) {
	if delivery == nil {
		return BriefDeliverContext{}, fmt.Errorf("delivery cannot be nil")
	}
	if l.meetingRepo == nil || l.briefRepo == nil {
		return BriefDeliverContext{}, fmt.Errorf("DBContextLoader missing repository dependency")
	}

	meeting, err := l.meetingRepo.GetMeetingByIDSystem(ctx, delivery.MeetingID)
	if err != nil {
		return BriefDeliverContext{}, fmt.Errorf("failed to load meeting: %w", err)
	}
	if meeting == nil {
		return BriefDeliverContext{}, fmt.Errorf("meeting not found: %s", delivery.MeetingID)
	}

	brief, err := l.briefRepo.GetBriefByMeetingAndUser(ctx, delivery.MeetingID, delivery.UserID)
	if err != nil {
		return BriefDeliverContext{}, fmt.Errorf("failed to load brief: %w", err)
	}
	if brief == nil {
		return BriefDeliverContext{}, fmt.Errorf("brief not found for meeting %s user %s", delivery.MeetingID, delivery.UserID)
	}

	user, err := loadUser(ctx, delivery.UserID)
	if err != nil {
		return BriefDeliverContext{}, err
	}
	profile, err := loadProfile(ctx, delivery.UserID)
	if err != nil {
		return BriefDeliverContext{}, err
	}

	return BriefDeliverContext{
		Delivery: delivery,
		Meeting:  meeting,
		Brief:    brief,
		User:     user,
		Profile:  profile,
	}, nil
}

// loadUser fetches just the fields a delivery needs (email + name).
// Kept here instead of introducing a UserRepository interface because
// Phase 1 is the only consumer; if a second caller appears later the
// helper can be promoted.
func loadUser(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	if repository.DB == nil {
		return nil, fmt.Errorf("repository.DB not initialised")
	}
	var u models.User
	err := repository.DB.QueryRow(ctx, `
		SELECT id, email, full_name
		FROM users
		WHERE id = $1
	`, userID).Scan(&u.ID, &u.Email, &u.FullName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("user not found: %s", userID)
		}
		return nil, fmt.Errorf("failed to load user: %w", err)
	}
	return &u, nil
}

// loadProfile fetches the profile fields a deliverer may use — timezone,
// languages, and the preference flags. Missing profile row returns a
// zero-value Profile so template rendering can still fall back to
// defaults (bot_name etc.) without crashing.
func loadProfile(ctx context.Context, userID uuid.UUID) (*models.Profile, error) {
	if repository.DB == nil {
		return nil, fmt.Errorf("repository.DB not initialised")
	}
	var p models.Profile
	err := repository.DB.QueryRow(ctx, `
		SELECT id,
		       COALESCE(timezone, 'UTC') AS timezone,
		       COALESCE(interface_language, 'tr') AS interface_language,
		       COALESCE(brief_lead_time_minutes, 10) AS brief_lead_time_minutes,
		       COALESCE(brief_deliver_via_email, false) AS brief_deliver_via_email,
		       COALESCE(brief_deliver_via_slack, false) AS brief_deliver_via_slack,
		       email_unsubscribed_at
		FROM profiles
		WHERE id = $1
	`, userID).Scan(
		&p.ID, &p.Timezone, &p.InterfaceLanguage, &p.BriefLeadTimeMinutes,
		&p.BriefDeliverViaEmail, &p.BriefDeliverViaSlack, &p.EmailUnsubscribedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			p.ID = userID
			p.Timezone = "UTC"
			p.InterfaceLanguage = "tr"
			p.BriefLeadTimeMinutes = 10
			return &p, nil
		}
		return nil, fmt.Errorf("failed to load profile: %w", err)
	}
	return &p, nil
}
