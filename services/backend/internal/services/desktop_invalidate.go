// Package services — desktop_invalidate.go publishes lightweight
// `invalidate` messages over the desktop WebSocket so the recorder
// refreshes its candidate queue without us having to mint a fresh
// `start_recording` command.
//
// Phase 2.4 B7: when the user changes recording_method (profile-level)
// or recording_method_override (per-meeting), the server's view of
// effective_method has shifted but no scheduling boundary fired. The
// recorder needs to re-fetch /api/calendar/upcoming to honor the new
// resolution. We push:
//
//	{"type":"invalidate","scope":"calendar"}
//
// The recorder treats this as a "rerun your poll now" hint — it's NEVER
// a substitute for a real start_recording; the Phase 2.4 R7 client
// dispatches it to the calendar refresh path only.

package services

import (
	"errors"
	"log/slog"

	"github.com/google/uuid"
)

// invalidateScopeCalendar identifies the recorder-side subsystem that
// owns the cache being invalidated. The recorder's WS client switches
// on this value; profile changes that affect arming all map to
// "calendar" because that's where the candidate queue lives.
const invalidateScopeCalendar = "calendar"

// invalidatePayload is the wire shape published over /ws/desktop.
type invalidatePayload struct {
	Type  string `json:"type"`
	Scope string `json:"scope"`
}

// PublishCalendarInvalidate fires the invalidate hint at the user's
// active desktop socket. No-ops cleanly when the user is offline —
// the recorder will pick up the new resolution on its next normal
// poll tick (15 min). When `hub` is nil this is a no-op so callers
// (handlers, services) don't have to guard the optional dependency.
func PublishCalendarInvalidate(hub *DesktopHub, userID uuid.UUID) {
	if hub == nil {
		return
	}
	payload := invalidatePayload{
		Type:  "invalidate",
		Scope: invalidateScopeCalendar,
	}
	if err := hub.Send(userID, payload); err != nil {
		// Offline is the expected case for the vast majority of users
		// (recorder is a tray app, not always running). Log at debug
		// so it doesn't pollute production logs but stays available
		// for diagnosis.
		if errors.Is(err, ErrUserNotConnected) {
			slog.Debug("invalidate: user offline; recorder will pick up on next poll",
				"user_id", userID)
			return
		}
		slog.Warn("invalidate: send failed",
			"user_id", userID, "error", err)
	}
}
