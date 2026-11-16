package models

import (
	"time"

	"github.com/google/uuid"
)

// Channel identifiers used across brief_deliveries, dispatcher routing,
// and metric labels. Keep values in sync with the CHECK constraint in
// sql/migrations/007_brief_delivery.sql.
const (
	BriefDeliveryChannelEmail = "email"
	BriefDeliveryChannelSlack = "slack"
)

// Delivery lifecycle states. pending -> (sent|failed|skipped).
// 'skipped' is terminal and used when the user unsubscribed / the
// integration is revoked or pending-email — the worker records the
// reason in last_error but does not retry.
const (
	BriefDeliveryStatusPending = "pending"
	BriefDeliveryStatusSent    = "sent"
	BriefDeliveryStatusFailed  = "failed"
	BriefDeliveryStatusSkipped = "skipped"
)

// BriefDelivery is the audit + idempotency record for a single channel
// attempt of a single meeting brief. UNIQUE (meeting_id, channel) in the
// DB prevents retries/cron restarts from creating duplicates.
type BriefDelivery struct {
	ID                 uuid.UUID  `json:"id"`
	MeetingID          uuid.UUID  `json:"meeting_id"`
	UserID             uuid.UUID  `json:"user_id"`
	Channel            string     `json:"channel"`
	Status             string     `json:"status"`
	Attempts           int        `json:"attempts"`
	LastError          *string    `json:"last_error,omitempty"`
	ProviderMessageID  *string    `json:"provider_message_id,omitempty"`
	ScheduledFor       time.Time  `json:"scheduled_for"`
	SentAt             *time.Time `json:"sent_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// SlackIntegration represents one OAuth install (one user × one workspace).
// bot_token_encrypted is AES-GCM ciphertext (base64) — only decrypt at the
// point of use, never store the plaintext anywhere. revoked_at != NULL is
// the "app uninstalled or token invalidated" signal; the dispatcher skips
// deliveries once set.
type SlackIntegration struct {
	ID                uuid.UUID  `json:"id"`
	UserID            uuid.UUID  `json:"user_id"`
	TeamID            string     `json:"team_id"`
	TeamName          *string    `json:"team_name,omitempty"`
	BotUserID         string     `json:"bot_user_id"`
	BotTokenEncrypted string     `json:"-"`
	DMChannelID       *string    `json:"dm_channel_id,omitempty"`
	MatchedEmail      *string    `json:"matched_email,omitempty"`
	Scope             string     `json:"scope"`
	InstalledAt       time.Time  `json:"installed_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
}

// NotifyBriefPayload is the background_jobs payload for a delivery job.
// The delivery row in brief_deliveries carries all other state — we keep
// this tiny so payloads don't drift out of sync with the audit table.
type NotifyBriefPayload struct {
	DeliveryID uuid.UUID `json:"delivery_id"`
}
