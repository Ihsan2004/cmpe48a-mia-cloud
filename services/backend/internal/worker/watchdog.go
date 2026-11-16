package worker

import (
	"context"
	"log/slog"
	"time"

	"mia-website/backend/internal/repository"
)

type WatchdogService struct {
	checkInterval time.Duration
	timeout       time.Duration
}

func NewWatchdogService() *WatchdogService {
	return &WatchdogService{
		checkInterval: 1 * time.Minute,
		timeout:       3 * time.Minute,
	}
}

// Start begins the watchdog service
func (s *WatchdogService) Start(ctx context.Context) {
	slog.Info("Starting Bot Watchdog", "check_interval", s.checkInterval, "timeout", s.timeout)
	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Check immediately on start
	go s.checkDeadBots()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Bot Watchdog stopped")
			return
		case <-ticker.C:
			s.checkDeadBots()
		}
	}
}

func (s *WatchdogService) checkDeadBots() {
	// Logic to update meetings
	// Find meetings that are 'recording' but haven't sent a heartbeat for > 3 minutes
	// Hybrid Watchdog Logic:
	// 1. If Meeting is OVER (end_time <= NOW): Be impatient (3 mins timeout)
	// 2. If Meeting is ONGOING (end_time > NOW): Be patient (15 mins timeout) to allow for temporary disconnects
	query := `
		UPDATE meetings
		SET bot_status = 'completed',
		    end_time = NOW()
		WHERE bot_status = 'recording'
		  AND (
			-- Fast timeout for ended meetings
			(end_time <= NOW() AND last_heartbeat_at < NOW() - INTERVAL '3 minutes')
			OR
			-- Slow timeout for ongoing meetings (10 min, synced with scheduler)
			(end_time > NOW() AND last_heartbeat_at < NOW() - INTERVAL '10 minutes')
		  )
		RETURNING id, title, COALESCE(meeting_url, '')
	`
	rows, err := repository.DB.Query(context.Background(), query)
	if err != nil {
		slog.Error("Watchdog query failed", "error", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id string
		var title string
		var meetingURL string
		if err := rows.Scan(&id, &title, &meetingURL); err == nil {
			slog.Warn("Watchdog detected dead bot", "meeting_title", title, "meeting_id", id)
			count++

			// Phase 8 fix 2026-04-20 — do NOT enqueue post-meeting here.
			// Watchdog's job is to flip stuck bots to completed so ops
			// dashboards show accurate state; the post-meeting pipeline
			// must only trigger from the ElevenLabs webhook (batch
			// transcription) to avoid racing ahead of segment ingest.
			// The webhook path + the scheduler's reconciler (gated on
			// transcription_status='completed') together cover the
			// recovery cases the old inline enqueue tried to solve.
			_ = id
			_ = meetingURL
		}
	}

	if count > 0 {
		slog.Info("Watchdog cleaned up dead bots", "count", count)
	}
}
