package services

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// captureWriteConn is a desktopConn fake that stores every payload the
// hub writes to it so tests can assert the exact frame.
type captureWriteConn struct {
	mu       sync.Mutex
	frames   [][]byte
	failNext error
	closed   bool
}

func (c *captureWriteConn) WriteMessage(_ int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext != nil {
		err := c.failNext
		c.failNext = nil
		return err
	}
	dup := make([]byte, len(data))
	copy(dup, data)
	c.frames = append(c.frames, dup)
	return nil
}

func (c *captureWriteConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *captureWriteConn) lastFrame() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.frames) == 0 {
		return nil
	}
	return c.frames[len(c.frames)-1]
}

func TestPublishCalendarInvalidate_NilHubIsNoop(t *testing.T) {
	t.Parallel()
	// Must not panic; nothing to assert beyond that.
	PublishCalendarInvalidate(nil, uuid.New())
}

func TestPublishCalendarInvalidate_OfflineUserSilent(t *testing.T) {
	t.Parallel()
	hub := NewDesktopHub()
	// No Register call — user is offline. Hub.Send returns
	// ErrUserNotConnected; the helper must swallow it without panicking.
	PublishCalendarInvalidate(hub, uuid.New())
}

func TestPublishCalendarInvalidate_PublishesExpectedShape(t *testing.T) {
	t.Parallel()
	hub := NewDesktopHub()
	userID := uuid.New()
	conn := &captureWriteConn{}
	hub.Register(userID, conn)

	PublishCalendarInvalidate(hub, userID)

	frame := conn.lastFrame()
	if frame == nil {
		t.Fatal("expected one frame written to the hub")
	}
	var got map[string]string
	if err := json.Unmarshal(frame, &got); err != nil {
		t.Fatalf("frame is not JSON: %v (raw=%q)", err, string(frame))
	}
	if got["type"] != "invalidate" {
		t.Errorf("type=%q, want %q", got["type"], "invalidate")
	}
	if got["scope"] != "calendar" {
		t.Errorf("scope=%q, want %q", got["scope"], "calendar")
	}
}

func TestPublishCalendarInvalidate_WriteFailureIsSwallowed(t *testing.T) {
	t.Parallel()
	hub := NewDesktopHub()
	userID := uuid.New()
	conn := &captureWriteConn{failNext: errors.New("simulated write failure")}
	hub.Register(userID, conn)

	// Must not panic. The helper logs and returns.
	PublishCalendarInvalidate(hub, userID)
}
