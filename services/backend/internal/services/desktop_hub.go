// Package services — desktop_hub.go is the in-memory connection registry
// for the Phase 6 desktop WebSocket channel.
//
// Scope: single backend replica. Multi-replica routing (pub/sub, Redis, etc.)
// is tracked for post-MVP — the POC explicitly runs min-replicas=1 so this
// map is the whole story. See main.go for the note.
package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

// ErrUserNotConnected is returned from Send when the target user has no
// live WebSocket. Callers (the DesktopMeetingScheduler) translate this into
// a no_ack log entry.
var ErrUserNotConnected = errors.New("desktop hub: user not connected")

// desktopConn is the slice of *websocket.Conn the hub actually touches.
// Narrowing lets us feed a stub in tests without opening real sockets.
type desktopConn interface {
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// textMessage is the WebSocket message type for text frames (1).
// Duplicated here so hub tests don't need to import the websocket package.
const textMessage = 1

// DesktopHub owns the userID -> *websocket.Conn map. A user can only have
// one live connection; a new Register for an existing user evicts the
// previous socket (client must have reconnected).
type DesktopHub struct {
	mu      sync.RWMutex
	conns   map[uuid.UUID]desktopConn
	logger  *slog.Logger
}

// NewDesktopHub returns an empty hub using the default slog logger.
func NewDesktopHub() *DesktopHub {
	return &DesktopHub{
		conns:  make(map[uuid.UUID]desktopConn),
		logger: slog.Default(),
	}
}

// Register associates userID with conn. If the user already has a
// connection, the previous one is closed first — clients must treat their
// previous socket as dead after a reconnect handshake.
func (h *DesktopHub) Register(userID uuid.UUID, conn desktopConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, exists := h.conns[userID]; exists && old != conn {
		// Best-effort close; ignore error, socket may already be gone.
		_ = old.Close()
		h.logger.Info("desktop hub: evicting previous connection", "user_id", userID)
	}
	h.conns[userID] = conn
}

// Unregister removes userID's connection and closes it. Safe to call with
// a stale conn (no-op if the map already holds a different socket).
func (h *DesktopHub) Unregister(userID uuid.UUID, conn desktopConn) {
	h.mu.Lock()
	cur, exists := h.conns[userID]
	if !exists {
		h.mu.Unlock()
		return
	}
	// Only delete+close if the caller's conn is still the current one — a
	// racing Register from a reconnect may have already replaced it.
	if conn != nil && cur != conn {
		h.mu.Unlock()
		return
	}
	delete(h.conns, userID)
	h.mu.Unlock()

	if cur != nil {
		_ = cur.Close()
	}
}

// Send marshals payload and writes it as a text frame to userID's socket.
// Returns ErrUserNotConnected if the user is offline — the scheduler uses
// this to drop a no_ack log entry without treating it as a hard failure.
func (h *DesktopHub) Send(userID uuid.UUID, payload interface{}) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("desktop hub: marshal payload: %w", err)
	}

	h.mu.RLock()
	conn, ok := h.conns[userID]
	h.mu.RUnlock()

	if !ok {
		return ErrUserNotConnected
	}

	if err := conn.WriteMessage(textMessage, raw); err != nil {
		return fmt.Errorf("desktop hub: write: %w", err)
	}
	return nil
}

// IsConnected is a lightweight readiness check used by the scheduler and
// tests. Uses the read lock so it doesn't contend with Sends.
func (h *DesktopHub) IsConnected(userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[userID]
	return ok
}
