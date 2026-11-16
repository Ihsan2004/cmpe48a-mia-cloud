package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// ==================== Pool Tests ====================

func TestPool_SubmitAndExecute(t *testing.T) {
	t.Parallel()

	pool := NewPool(2, 10)
	pool.Start()
	defer pool.Stop()

	var counter int32
	for i := 0; i < 5; i++ {
		pool.Submit(func() {
			atomic.AddInt32(&counter, 1)
		})
	}

	// Wait for tasks to complete
	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&counter) != 5 {
		t.Errorf("expected 5 tasks executed, got %d", atomic.LoadInt32(&counter))
	}
}

func TestPool_Submit_Stopped_Rejected(t *testing.T) {
	t.Parallel()

	pool := NewPool(1, 1)
	pool.Start()
	pool.Stop()

	ok := pool.Submit(func() {})
	if ok {
		t.Error("expected submit to fail after pool stopped")
	}
}

func TestPool_Submit_BufferFull_Rejected(t *testing.T) {
	t.Parallel()

	pool := NewPool(1, 1)
	// Don't start — workers won't consume
	// Fill the buffer
	pool.tasks <- func() {}

	ok := pool.Submit(func() {})
	if ok {
		t.Error("expected submit to fail when buffer is full")
	}

	// Drain and clean up
	<-pool.tasks
}

func TestPool_SubmitWithTimeout_Success(t *testing.T) {
	t.Parallel()

	pool := NewPool(2, 10)
	pool.Start()
	defer pool.Stop()

	ok := pool.SubmitWithTimeout(func() {}, 100*time.Millisecond)
	if !ok {
		t.Error("expected submit with timeout to succeed")
	}
}

func TestPool_SubmitWithTimeout_Expired(t *testing.T) {
	t.Parallel()

	pool := NewPool(1, 1)
	// Don't start, fill buffer
	pool.tasks <- func() {}

	ok := pool.SubmitWithTimeout(func() {}, 10*time.Millisecond)
	if ok {
		t.Error("expected submit with timeout to fail when buffer full and timeout expires")
	}

	<-pool.tasks
}

func TestPool_PanicRecovery(t *testing.T) {
	t.Parallel()

	pool := NewPool(1, 10)
	pool.Start()
	defer pool.Stop()

	// Submit a panicking task
	pool.Submit(func() {
		panic("test panic")
	})

	// Submit a normal task after — pool should still work
	var executed int32
	pool.Submit(func() {
		atomic.AddInt32(&executed, 1)
	})

	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&executed) != 1 {
		t.Error("pool should recover from panic and continue processing")
	}
}

// ==================== Backoff Tests ====================

func TestComputeRetryBackoff_FirstAttempt(t *testing.T) {
	t.Parallel()

	delay := computeRetryBackoff(0)
	if delay != 15*time.Second {
		t.Errorf("expected 15s for attempt 0, got %v", delay)
	}
}

func TestComputeRetryBackoff_ExponentialGrowth(t *testing.T) {
	t.Parallel()

	expected := []time.Duration{
		15 * time.Second,  // attempt 1
		30 * time.Second,  // attempt 2
		60 * time.Second,  // attempt 3
		120 * time.Second, // attempt 4
	}

	for i, exp := range expected {
		delay := computeRetryBackoff(i + 1)
		if delay != exp {
			t.Errorf("attempt %d: expected %v, got %v", i+1, exp, delay)
		}
	}
}

func TestComputeRetryBackoff_CappedAtMax(t *testing.T) {
	t.Parallel()

	// attempt 7: 15s * 64 = 960s = 16min > maxBackoffDelay (10min)
	delay := computeRetryBackoff(7)
	if delay != maxBackoffDelay {
		t.Errorf("expected max backoff %v, got %v", maxBackoffDelay, delay)
	}
}

// ==================== readEnvInt Tests ====================

func TestReadEnvInt_Default(t *testing.T) {
	result := readEnvInt("TEST_NONEXISTENT_KEY_12345", 42)
	if result != 42 {
		t.Errorf("expected 42 for missing env, got %d", result)
	}
}

func TestReadEnvInt_ValidValue(t *testing.T) {
	t.Setenv("TEST_WORKER_COUNT", "8")
	result := readEnvInt("TEST_WORKER_COUNT", 4)
	if result != 8 {
		t.Errorf("expected 8, got %d", result)
	}
}

func TestReadEnvInt_InvalidValue(t *testing.T) {
	t.Setenv("TEST_WORKER_INVALID", "abc")
	result := readEnvInt("TEST_WORKER_INVALID", 4)
	if result != 4 {
		t.Errorf("expected fallback 4 for invalid value, got %d", result)
	}
}

// ==================== sleepOrDone Tests ====================

func TestSleepOrDone_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result := sleepOrDone(ctx, 10*time.Second)
	if result {
		t.Error("expected false when context is cancelled")
	}
}

func TestSleepOrDone_TimerExpires(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	result := sleepOrDone(ctx, 10*time.Millisecond)
	if !result {
		t.Error("expected true when timer expires normally")
	}
}
