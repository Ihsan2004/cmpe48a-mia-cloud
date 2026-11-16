package models

import (
	"time"

	"github.com/google/uuid"
)

// DesktopCommand mirrors the desktop_command_log table (migration 006).
// UNIQUE (meeting_id, command_type) is the scheduler's idempotency primitive —
// a duplicate InsertCommandIfMissing call becomes a no-op.
type DesktopCommand struct {
	ID              uuid.UUID  `json:"id"`
	MeetingID       uuid.UUID  `json:"meeting_id"`
	UserID          uuid.UUID  `json:"user_id"`
	CommandType     string     `json:"command_type"`
	SentAt          time.Time  `json:"sent_at"`
	ClientAckStatus *string    `json:"client_ack_status,omitempty"`
	ClientAckAt     *time.Time `json:"client_ack_at,omitempty"`
}

// Allowed command_type values. Must stay in sync with the desktop client.
const (
	DesktopCommandTypeStartRecording = "start_recording"
	DesktopCommandTypeStopRecording  = "stop_recording"
	DesktopCommandTypeForceStop      = "force_stop"
)

// Allowed client_ack_status values.
const (
	DesktopCommandAckAccepted = "accepted"
	DesktopCommandAckDeclined = "declined"
	DesktopCommandAckNoAck    = "no_ack"
)
