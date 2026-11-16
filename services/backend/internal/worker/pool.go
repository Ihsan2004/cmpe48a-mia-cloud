package worker

import (
	"log/slog"
	"sync"
	"time"
)

// Task represents a function that should be executed asynchronously
type Task func()

// Pool manages a pool of workers to execute tasks
type Pool struct {
	tasks      chan Task
	wg         sync.WaitGroup
	maxWorkers int
	mu         sync.RWMutex
	stopped    bool
}

// Global pool instance (singleton-ish for simplicity in this transition)
var GlobalPool *Pool
var once sync.Once

// InitializeGlobalPool creates the global worker pool
func InitializeGlobalPool(maxWorkers int, bufferSize int) {
	once.Do(func() {
		GlobalPool = NewPool(maxWorkers, bufferSize)
		GlobalPool.Start()
	})
}

// NewPool creates a new worker pool
func NewPool(maxWorkers int, bufferSize int) *Pool {
	return &Pool{
		tasks:      make(chan Task, bufferSize),
		maxWorkers: maxWorkers,
	}
}

// Start spawns the workers
func (p *Pool) Start() {
	for i := 0; i < p.maxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	slog.Info("Worker pool started", "max_workers", p.maxWorkers)
}

// worker listens for tasks and executes them
func (p *Pool) worker(id int) {
	defer p.wg.Done()
	for task := range p.tasks {
		// Execute task with panic recovery
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("Panic in worker", "worker_id", id, "panic", r)
				}
			}()
			task()
		}()
	}
}

// Submit adds a task to the pool
// Returns true if queued, false if buffer is full (non-blocking)
func (p *Pool) Submit(task Task) bool {
	if p.isStopped() {
		slog.Warn("Worker pool is stopped, rejecting task")
		return false
	}

	select {
	case p.tasks <- task:
		return true
	default:
		slog.Warn("Worker pool buffer full, dropping task")
		return false
	}
}

// SubmitBlocking adds a task, waiting if necessary
func (p *Pool) SubmitBlocking(task Task) bool {
	if p.isStopped() {
		slog.Warn("Worker pool is stopped, rejecting blocking task")
		return false
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Warn("Worker pool rejected task after shutdown", "panic", r)
		}
	}()

	p.tasks <- task
	return true
}

// SubmitWithTimeout tries to enqueue within timeout duration.
func (p *Pool) SubmitWithTimeout(task Task, timeout time.Duration) bool {
	if p.isStopped() {
		slog.Warn("Worker pool is stopped, rejecting timed task")
		return false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case p.tasks <- task:
		return true
	case <-timer.C:
		slog.Warn("Worker pool enqueue timed out", "timeout", timeout)
		return false
	}
}

// Stop gracefully shuts down the pool
func (p *Pool) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.tasks)
	p.mu.Unlock()

	p.wg.Wait()
	slog.Info("Worker pool stopped")
}

func (p *Pool) isStopped() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopped
}
