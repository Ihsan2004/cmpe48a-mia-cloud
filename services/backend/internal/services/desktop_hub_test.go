package services

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// ==================== Fake conn ====================

// fakeDesktopConn is the minimal stub of *websocket.Conn used in hub
// tests. It records all writes and tracks Close calls so we can assert
// eviction behaviour.
type fakeDesktopConn struct {
	mu       sync.Mutex
	writes   [][]byte
	closed   atomic.Bool
	writeErr error
}

func newFakeConn() *fakeDesktopConn { return &fakeDesktopConn{} }

func (f *fakeDesktopConn) WriteMessage(_ int, data []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	buf := make([]byte, len(data))
	copy(buf, data)
	f.writes = append(f.writes, buf)
	return nil
}

func (f *fakeDesktopConn) Close() error {
	f.closed.Store(true)
	return nil
}

func (f *fakeDesktopConn) lastWrite() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return nil
	}
	return f.writes[len(f.writes)-1]
}

// ==================== Tests ====================

func TestDesktopHub_RegisterAndSend(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	user := uuid.New()
	conn := newFakeConn()

	hub.Register(user, conn)

	if !hub.IsConnected(user) {
		t.Fatal("expected IsConnected=true after Register")
	}

	if err := hub.Send(user, map[string]string{"type": "ping"}); err != nil {
		t.Fatalf("Send returned: %v", err)
	}

	got := conn.lastWrite()
	var parsed map[string]string
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, got)
	}
	if parsed["type"] != "ping" {
		t.Errorf("type = %q, want ping", parsed["type"])
	}
}

func TestDesktopHub_SendToUnknownUserReturnsErrUserNotConnected(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	err := hub.Send(uuid.New(), map[string]string{"type": "start_recording"})
	if !errors.Is(err, ErrUserNotConnected) {
		t.Errorf("expected ErrUserNotConnected, got %v", err)
	}
}

func TestDesktopHub_UnregisterClosesConn(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	user := uuid.New()
	conn := newFakeConn()

	hub.Register(user, conn)
	hub.Unregister(user, conn)

	if hub.IsConnected(user) {
		t.Error("IsConnected still true after Unregister")
	}
	if !conn.closed.Load() {
		t.Error("Close was not called on Unregister")
	}
}

func TestDesktopHub_UnregisterStaleConnIgnored(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	user := uuid.New()

	first := newFakeConn()
	second := newFakeConn()

	hub.Register(user, first)
	hub.Register(user, second) // evicts first (Close on first)

	if !first.closed.Load() {
		t.Error("previous conn should be closed on re-Register")
	}

	// Now Unregister with the stale conn — must not touch the current one.
	hub.Unregister(user, first)

	if !hub.IsConnected(user) {
		t.Error("stale Unregister removed the current conn")
	}
	if second.closed.Load() {
		t.Error("stale Unregister closed the current conn")
	}
}

func TestDesktopHub_RegisterEvictsPrevious(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	user := uuid.New()

	first := newFakeConn()
	second := newFakeConn()

	hub.Register(user, first)
	hub.Register(user, second)

	if !first.closed.Load() {
		t.Error("first conn should be closed when second registers")
	}
	if second.closed.Load() {
		t.Error("second conn should stay open")
	}

	if err := hub.Send(user, map[string]string{"type": "pong"}); err != nil {
		t.Fatalf("Send after eviction: %v", err)
	}
	if got := second.lastWrite(); got == nil {
		t.Error("Send routed to wrong conn after eviction")
	}
}

func TestDesktopHub_SendWriteErrorPropagates(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	user := uuid.New()
	conn := &fakeDesktopConn{writeErr: errors.New("pipe closed")}

	hub.Register(user, conn)
	err := hub.Send(user, map[string]string{"type": "x"})
	if err == nil {
		t.Fatal("expected write error to propagate")
	}
	if errors.Is(err, ErrUserNotConnected) {
		t.Error("should not be ErrUserNotConnected — user is registered")
	}
}

// TestDesktopHub_ConcurrentRegisterAndSend stresses the mutex boundary —
// if we regressed to RUnlock-after-mutate the -race detector catches it.
func TestDesktopHub_ConcurrentRegisterAndSend(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	const workers = 16
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				user := uuid.New()
				hub.Register(user, newFakeConn())
				_ = hub.Send(user, map[string]int{"n": j})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = hub.Send(uuid.New(), map[string]int{"n": j})
			}
		}()
	}

	wg.Wait()
}

// TestDesktopHub_MarshalFailureReturnsError — unmarshalable payload (a
// channel) should fail before touching the socket.
func TestDesktopHub_MarshalFailureReturnsError(t *testing.T) {
	t.Parallel()

	hub := NewDesktopHub()
	user := uuid.New()
	conn := newFakeConn()
	hub.Register(user, conn)

	ch := make(chan int) // channels can't be JSON-marshalled
	err := hub.Send(user, ch)
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if conn.lastWrite() != nil {
		t.Error("WriteMessage called despite marshal failure")
	}
}
