// Package handlers — desktop_ws.go wires the Phase 6 WebSocket endpoint.
//
// Route: GET /ws/desktop?token=<jwt>
// Auth : JWT in the query string (same signing key as the HTTP JWT middleware;
//        the upgrade request can't carry custom headers cross-origin so a
//        query param is the only workable channel).
// Body : JSON messages keyed by "type" — ping / command_ack / recorder_state.
//        Backend sends start_recording / stop_recording / force_stop / pong /
//        connected.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// heartbeatTimeout — if the client doesn't ping within this window we close
// the socket. 3x the client's expected 30s cadence, per roadmap 6.1.
const heartbeatTimeout = 90 * time.Second

// DesktopWSHandler owns the hub + repo pair used by the WebSocket endpoint.
type DesktopWSHandler struct {
	hub  *services.DesktopHub
	repo repository.DesktopRepository
	auth desktopAuthValidator
}

// desktopAuthValidator is the minimal slice of services.AuthService we use
// to validate the handshake JWT. Exposed as an interface so tests can stub
// the JWT path without setting up the real AuthService (which requires a
// JWT_SECRET env var at construction time).
type desktopAuthValidator interface {
	ValidateAccessToken(tokenString string) (map[string]interface{}, error)
}

// authServiceAdapter bridges *services.AuthService (which returns
// jwt.MapClaims) to our map[string]interface{} interface so we don't have
// to import jwt-go here.
type authServiceAdapter struct {
	svc *services.AuthService
}

func (a *authServiceAdapter) ValidateAccessToken(tokenString string) (map[string]interface{}, error) {
	claims, err := a.svc.ValidateAccessToken(tokenString)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}(claims), nil
}

// NewDesktopWSHandler constructs the handler. The auth service is resolved
// from env on first call; if JWT_SECRET is unset, handshakes fail closed
// with 500 (matches the HTTP middleware behaviour).
func NewDesktopWSHandler(hub *services.DesktopHub, repo repository.DesktopRepository) *DesktopWSHandler {
	return &DesktopWSHandler{hub: hub, repo: repo}
}

// Hub returns the underlying DesktopHub so other handlers (profile
// settings, per-meeting override) can publish `invalidate` messages
// after mutations that change effective_method (Phase 2.4 B7).
// Returns nil when the WS subsystem isn't wired (test harnesses).
func (h *DesktopWSHandler) Hub() *services.DesktopHub {
	if h == nil {
		return nil
	}
	return h.hub
}

// newDesktopWSHandlerForTest wires a stub auth validator for unit tests.
// Not exported outside the package.
func newDesktopWSHandlerForTest(
	hub *services.DesktopHub,
	repo repository.DesktopRepository,
	auth desktopAuthValidator,
) *DesktopWSHandler {
	return &DesktopWSHandler{hub: hub, repo: repo, auth: auth}
}

// UpgradeMiddleware gates the websocket.New handler behind an upgrade check.
// Non-WebSocket traffic gets 426 Upgrade Required rather than falling into
// the handshake, which would otherwise return a confusing 400.
func (h *DesktopWSHandler) UpgradeMiddleware(c *fiber.Ctx) error {
	if websocket.IsWebSocketUpgrade(c) {
		c.Locals("allowed", true)
		return c.Next()
	}
	return fiber.ErrUpgradeRequired
}

// Handle is the fiber.Handler wired into routes.go. It validates the JWT
// from the query string *before* upgrading — a rejected handshake returns
// 401 with no socket opened.
//
// Auth sources, first non-empty wins (Phase 6 fix 2026-04-20):
//   - ?token=<jwt>               browser WS cross-origin (required for
//                                XHR-style handshakes; this is the
//                                primary channel per roadmap 6.1)
//   - Authorization: Bearer <jwt> native desktop client path documented
//                                in the roadmap — added here so clients
//                                following that contract don't hit 401
func (h *DesktopWSHandler) Handle(c *fiber.Ctx) error {
	token := c.Query("token")
	if token == "" {
		if auth := c.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
			token = auth[7:]
		}
	}
	if token == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "missing token",
			"code":  "TOKEN_MISSING",
		})
	}

	auth, err := h.resolveAuth()
	if err != nil {
		slog.Error("desktop ws: auth service unavailable", "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "auth service unavailable",
			"code":  "SERVER_ERROR",
		})
	}

	claims, err := auth.ValidateAccessToken(token)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid token",
			"code":  "TOKEN_INVALID",
		})
	}

	userIDStr, ok := claims["sub"].(string)
	if !ok || userIDStr == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "missing sub",
			"code":  "TOKEN_INVALID_CLAIMS",
		})
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid user id",
			"code":  "TOKEN_INVALID_CLAIMS",
		})
	}

	clientVersion := c.Query("client_version", "")

	// Stash the verified identity on Locals so the inner websocket.New
	// handler can read it — websocket.Conn retains the original fiber
	// Locals map.
	c.Locals("desktopUserID", userID)
	c.Locals("desktopClientVersion", clientVersion)

	wsHandler := websocket.New(h.wsHandler)
	return wsHandler(c)
}

// resolveAuth returns the configured auth validator, falling back to a
// lazy services.AuthService. Wrapping the construction in a method lets
// tests inject a fake without going through env.
func (h *DesktopWSHandler) resolveAuth() (desktopAuthValidator, error) {
	if h.auth != nil {
		return h.auth, nil
	}
	svc, err := services.NewAuthService()
	if err != nil {
		return nil, err
	}
	return &authServiceAdapter{svc: svc}, nil
}

// wsHandler runs per live socket. It's invoked by websocket.New only after
// the upgrade succeeds, so we know the JWT was already validated and the
// Locals fields are populated.
//
// Control flow:
//  1. Create session in repo, register with hub, send "connected".
//  2. Spawn a heartbeat watchdog goroutine that closes the socket after 90s
//     of silence.
//  3. Read loop dispatches by message type.
//  4. On exit (any reason), unregister + close session in repo.
func (h *DesktopWSHandler) wsHandler(c *websocket.Conn) {
	userID, ok := c.Locals("desktopUserID").(uuid.UUID)
	if !ok {
		slog.Error("desktop ws: missing userID in locals")
		_ = c.Close()
		return
	}
	clientVersion, _ := c.Locals("desktopClientVersion").(string)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session, err := h.repo.CreateSession(ctx, userID, clientVersion)
	if err != nil {
		slog.Error("desktop ws: create session failed", "user_id", userID, "error", err)
		_ = c.Close()
		return
	}

	h.hub.Register(userID, c)

	// Send the handshake ack so the client can start its ping cadence.
	ack, _ := json.Marshal(map[string]string{
		"type":       "connected",
		"session_id": session.ID.String(),
	})
	if err := c.WriteMessage(websocket.TextMessage, ack); err != nil {
		slog.Warn("desktop ws: send connected failed", "user_id", userID, "error", err)
		h.hub.Unregister(userID, c)
		_ = h.repo.CloseSession(ctx, session.ID)
		return
	}

	// Heartbeat watchdog — lastPingAt is updated on each ping; the watcher
	// closes the socket if we miss the 90s window. Using a sync.Mutex
	// (not atomic) because time.Time is 24 bytes on amd64 and writes must
	// be torn-free.
	var (
		pingMu     sync.Mutex
		lastPingAt = time.Now()
	)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingMu.Lock()
				last := lastPingAt
				pingMu.Unlock()
				if time.Since(last) > heartbeatTimeout {
					slog.Info("desktop ws: heartbeat timeout",
						"user_id", userID, "session_id", session.ID)
					_ = c.WriteMessage(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(
							websocket.CloseNormalClosure,
							"heartbeat_timeout",
						),
					)
					_ = c.Close()
					return
				}
			}
		}
	}()

	defer func() {
		h.hub.Unregister(userID, c)
		if err := h.repo.CloseSession(ctx, session.ID); err != nil {
			slog.Warn("desktop ws: close session failed",
				"session_id", session.ID, "error", err)
		}
	}()

	for {
		mt, raw, err := c.ReadMessage()
		if err != nil {
			if !errors.Is(err, websocket.ErrCloseSent) {
				slog.Info("desktop ws: read loop exit",
					"user_id", userID, "error", err)
			}
			return
		}
		if mt != websocket.TextMessage {
			// We only speak JSON; ignore binary / pings at the TCP frame
			// layer.
			continue
		}

		var envelope struct {
			Type      string `json:"type"`
			Command   string `json:"command"`
			MeetingID string `json:"meeting_id"`
			Status    string `json:"status"`
			State     string `json:"state"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			slog.Warn("desktop ws: bad json", "user_id", userID, "error", err)
			continue
		}

		switch envelope.Type {
		case "ping":
			pingMu.Lock()
			lastPingAt = time.Now()
			pingMu.Unlock()
			pong, _ := json.Marshal(map[string]string{"type": "pong"})
			if err := c.WriteMessage(websocket.TextMessage, pong); err != nil {
				slog.Warn("desktop ws: pong write failed",
					"user_id", userID, "error", err)
				return
			}
			if err := h.repo.UpdateHeartbeat(ctx, session.ID); err != nil {
				slog.Warn("desktop ws: heartbeat persist failed",
					"session_id", session.ID, "error", err)
			}

		case "command_ack":
			if envelope.MeetingID == "" || envelope.Command == "" {
				slog.Warn("desktop ws: incomplete command_ack",
					"user_id", userID, "raw", string(raw))
				continue
			}
			mID, err := uuid.Parse(envelope.MeetingID)
			if err != nil {
				slog.Warn("desktop ws: bad meeting_id on ack",
					"user_id", userID, "meeting_id", envelope.MeetingID)
				continue
			}
			// Phase 6 fix 2026-04-20 — validate enum values locally before
			// persisting. Unknown command_type/status would otherwise reach
			// the DB (and silently fail the CHECK constraint, or succeed
			// on environments without CHECKs). We accept the "no status"
			// shorthand from older clients as "accepted".
			if !repository.IsValidDesktopCommandType(envelope.Command) {
				slog.Warn("desktop ws: unknown command_type on ack",
					"user_id", userID, "command", envelope.Command)
				continue
			}
			status := envelope.Status
			if status == "" {
				status = "accepted"
			}
			if !repository.IsValidDesktopAckStatus(status) {
				slog.Warn("desktop ws: unknown ack status",
					"user_id", userID, "status", status)
				continue
			}
			// Scoped by user_id so session auth carries through to the
			// row update; the handshake already validated this userID.
			if err := h.repo.MarkCommandAck(ctx, userID, mID, envelope.Command, status); err != nil {
				slog.Warn("desktop ws: mark ack failed",
					"meeting_id", mID, "error", err)
			}

		case "recorder_state":
			// MVP: observability only. Full persistence can come later
			// once we wire a recorder_state table.
			slog.Info("desktop ws: recorder state",
				"user_id", userID,
				"state", envelope.State,
				"meeting_id", envelope.MeetingID,
			)

		default:
			slog.Debug("desktop ws: unknown message type",
				"user_id", userID, "type", envelope.Type)
		}
	}
}
