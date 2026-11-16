package models

import (
	"time"

	"github.com/google/uuid"
)

// DesktopSession mirrors the desktop_sessions table (migration 006).
// One row per open WebSocket handshake. disconnected_at stays NULL while the
// socket is live; the hub nulls/fills it on Register / Unregister.
type DesktopSession struct {
	ID              uuid.UUID  `json:"id"`
	UserID          uuid.UUID  `json:"user_id"`
	ConnectedAt     time.Time  `json:"connected_at"`
	LastHeartbeatAt time.Time  `json:"last_heartbeat_at"`
	DisconnectedAt  *time.Time `json:"disconnected_at,omitempty"`
	ClientVersion   *string    `json:"client_version,omitempty"`
}
