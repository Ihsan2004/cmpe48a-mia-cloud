package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/google/uuid"
)

const (
	summaryGraceAfterEnd        = 90 * time.Second
	summaryIngestionQuietPeriod = 45 * time.Second
)

// SchedulerService handles automated bot joining and auto-summarization for meetings
type SchedulerService struct {
	botAPIURL               string
	nlpServiceURL           string
	checkInterval           time.Duration
	joinBeforeMinutes       int
	streamingInterval       time.Duration
	streamingWorkers        int
	streamingSemaphore      chan struct{}
	client                  *http.Client
	profileService          *ProfileService
	correctionService       *CorrectionService
	postMeetingService      *PostMeetingService
	recordingMethodResolver RecordingMethodResolver // Phase 5: skip bot dispatch when desktop is selected
	botDispatcher           BotDispatcher           // Phase 7.10: ACA Jobs dispatch (falls back to HTTP)
	quotaService            quota.Service           // issue #39: pre-dispatch soft-cap gate (nil ⇒ disabled, e.g. tests)
	streamingAttempts       sync.Map                // Cache to throttle incremental streaming updates per meeting
}

// NewSchedulerService creates a new scheduler service
func NewSchedulerService(botAPIURL string, profileService *ProfileService) *SchedulerService {
	nlpURL := os.Getenv("NLP_SERVICE_URL")
	if nlpURL == "" {
		nlpURL = "http://mia-search:8000"
	}

	streamingWorkers := 2
	if rawWorkers := os.Getenv("STREAMING_CORRECTION_WORKERS"); rawWorkers != "" {
		if parsed, err := strconv.Atoi(rawWorkers); err == nil && parsed > 0 {
			streamingWorkers = parsed
		}
	}

	// Phase 7.10 — pick ACA dispatcher when AZURE_SUBSCRIPTION_ID is set,
	// otherwise keep calling the legacy HTTP bot orchestrator. A failure
	// to build the ACA client here is non-fatal; we log and fall back to
	// HTTP so a misconfigured subscription doesn't break boot.
	dispatcher, err := NewBotDispatcher(botAPIURL)
	if err != nil {
		slog.Warn("Failed to build ACA bot dispatcher, falling back to HTTP", "error", err)
		dispatcher = NewHTTPBotDispatcher(botAPIURL)
	}

	return &SchedulerService{
		botAPIURL:               botAPIURL,
		nlpServiceURL:           nlpURL,
		checkInterval:           1 * time.Minute, // Check every minute
		joinBeforeMinutes:       1,               // Join 1 minute before meeting starts
		streamingInterval:       30 * time.Second,
		streamingWorkers:        streamingWorkers,
		streamingSemaphore:      make(chan struct{}, streamingWorkers),
		client: &http.Client{
			Timeout: 5 * time.Minute, // Longer timeout for summarization
		},
		profileService:          profileService,
		correctionService:       NewCorrectionService(),
		postMeetingService:      NewPostMeetingService(),
		recordingMethodResolver: NewRecordingMethodService(),
		botDispatcher:           dispatcher,
	}
}

// SetBotDispatcher swaps the bot dispatcher — used by tests to inject a
// fake implementation without touching env.
func (s *SchedulerService) SetBotDispatcher(d BotDispatcher) {
	s.botDispatcher = d
}

// SetRecordingMethodResolver lets tests inject a fake resolver without
// going through DB. Production uses the default DB-backed implementation
// wired in NewSchedulerService.
func (s *SchedulerService) SetRecordingMethodResolver(r RecordingMethodResolver) {
	s.recordingMethodResolver = r
}

// SetQuotaService wires the per-user soft-cap gate. Nil disables the
// check (kept tolerant so legacy scheduler tests don't need a fake).
func (s *SchedulerService) SetQuotaService(q quota.Service) {
	s.quotaService = q
}

// BotJoinRequest represents the request to external bot API
type BotJoinRequest struct {
	MeetingURL string `json:"meeting_url"`
	BotName    string `json:"bot_name"`
	MeetingID  string `json:"meeting_id"`
	Language   string `json:"language,omitempty"`
}

// Start begins the scheduler service.
//
// Phase 7.10 fix 2026-04-20 — dispatch gating moved from `botAPIURL != ""`
// to `botDispatcher != nil`. In an ACA-only deployment the legacy HTTP
// URL is intentionally unset; the ACA BotDispatcher handles dispatch
// without it. The old check meant a correctly-configured ACA deployment
// would log "Bot scheduler disabled" and silently never start bot jobs.
func (s *SchedulerService) Start(ctx context.Context) {
	dispatchReady := s.botDispatcher != nil
	if !dispatchReady {
		slog.Warn("Bot dispatcher not configured, bot scheduler disabled")
	} else {
		slog.Info("Starting Meeting Bot Scheduler", "check_interval", s.checkInterval)
		slog.Info("Bot will join before meeting starts", "join_before_minutes", s.joinBeforeMinutes)
		// Log the dispatcher flavour rather than the (possibly empty) legacy URL.
		switch s.botDispatcher.(type) {
		case *ACABotDispatcher:
			slog.Info("Bot dispatcher: Azure Container Apps Jobs")
		case *HTTPBotDispatcher:
			slog.Info("Bot dispatcher: legacy HTTP", "bot_api_url", s.botAPIURL)
		default:
			slog.Info("Bot dispatcher: custom")
		}
	}

	slog.Info("Auto-summarization enabled", "nlp_service_url", s.nlpServiceURL)
	slog.Info("Streaming transcript updates enabled", "interval", s.streamingInterval, "workers", s.streamingWorkers)

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Check immediately on start
	if dispatchReady {
		s.checkAndJoinMeetings()
	}
	s.checkAndRefreshStreamingBlocks()
	s.checkAndSummarizeMeetings()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping Meeting Bot Scheduler")
			return
		case <-ticker.C:
			if dispatchReady {
				s.checkAndJoinMeetings()
			}
			s.checkAndRefreshStreamingBlocks()
			s.cleanupStaleMeetings()
			s.checkAndSummarizeMeetings()
		}
	}
}

// checkAndJoinMeetings finds meetings that should have bot join
func (s *SchedulerService) checkAndJoinMeetings() {
	now := time.Now().UTC()
	joinWindow := now.Add(time.Duration(s.joinBeforeMinutes) * time.Minute)

	// Query for meetings that:
	// 1. Start within the next N minutes OR have already started
	// 2. Have NOT ended yet (end_time > NOW)
	// 3. Have a meeting URL
	// 4. Auto bot is enabled (auto_bot_enabled = true)
	// 5. Bot hasn't joined yet — OR was previously quota-blocked
	//
	// Issue #39 — quota_exceeded is intentionally re-pickable: if the
	// user's cycle reset since the last attempt (cap rolled over, admin
	// PATCH bumped the override, …), the meeting should get a fresh
	// dispatch. The downstream gate re-checks quota and re-flags if
	// still over. All other terminal statuses (joining/connecting/
	// recording/completed/failed) stay excluded.
	query := `
		SELECT m.id, m.user_id, m.title, m.start_time, m.end_time, m.meeting_url
		FROM meetings m
		WHERE m.meeting_url IS NOT NULL
		  AND (m.auto_bot_enabled IS NULL OR m.auto_bot_enabled = true)
		  AND m.start_time <= $1
		  AND m.end_time > $2
		  AND (m.bot_status IS NULL OR m.bot_status = 'quota_exceeded')
		ORDER BY m.start_time ASC
	`

	slog.Debug("Checking meetings for bot join", "join_window", joinWindow.Format("15:04:05"), "now", now.Format("15:04:05"))

	rows, err := repository.DB.Query(context.Background(), query, joinWindow, now)
	if err != nil {
		slog.Error("Error querying meetings", "error", err)
		return
	}
	defer rows.Close()

	meetingsToJoin := []models.Meeting{}
	for rows.Next() {
		var meeting models.Meeting
		err := rows.Scan(
			&meeting.ID,
			&meeting.UserID,
			&meeting.Title,
			&meeting.StartTime,
			&meeting.EndTime,
			&meeting.MeetingURL,
		)
		if err != nil {
			slog.Error("Error scanning meeting", "error", err)
			continue
		}
		meetingsToJoin = append(meetingsToJoin, meeting)
	}

	if len(meetingsToJoin) == 0 {
		return
	}

	slog.Info("Found meetings to join", "count", len(meetingsToJoin))

	// Process each meeting
	for _, meeting := range meetingsToJoin {
		// Phase 5: honour per-meeting / per-profile recording method.
		// Desktop-selected meetings are handled by the desktop scheduler
		// (Phase 6), so the bot must not jump in here.
		if s.recordingMethodResolver != nil {
			method, err := s.recordingMethodResolver.GetEffectiveRecordingMethod(
				context.Background(), meeting.ID)
			if err != nil {
				slog.Warn("recording method lookup failed, defaulting to bot",
					"meeting_id", meeting.ID, "error", err)
			} else if method == models.RecordingMethodDesktop {
				slog.Info("Skipping bot dispatch; user selected desktop capture",
					"meeting_id", meeting.ID)
				continue
			}
		}
		slog.Info("Dispatching bot for meeting", "meeting_title", meeting.Title, "meeting_id", meeting.ID, "start_time", meeting.StartTime.Format("15:04:05"), "end_time", meeting.EndTime.Format("15:04:05"))
		s.joinMeetingWithBot(meeting)
	}
}

// joinMeetingWithBot sends bot to the meeting via the configured
// dispatcher (ACA Jobs in production, legacy HTTP otherwise).
func (s *SchedulerService) joinMeetingWithBot(meeting models.Meeting) {
	if meeting.MeetingURL == nil || *meeting.MeetingURL == "" {
		slog.Warn("Meeting has no URL, skipping", "meeting_id", meeting.ID)
		return
	}

	// Phase 7.10 concurrent cap — count meetings that have an in-flight
	// bot execution. Skipping here leaves bot_status unchanged so the
	// next scheduler tick retries; no state pollution.
	if activeCount, err := s.countActiveBotMeetings(); err != nil {
		slog.Warn("Failed to count active bot meetings, proceeding", "error", err)
	} else if activeCount >= MaxConcurrentBotJobs() {
		slog.Info("Max concurrent bot executions reached — deferring",
			"active", activeCount,
			"max", MaxConcurrentBotJobs(),
			"meeting_id", meeting.ID)
		return
	}

	// Issue #39 — quota gate. Block bot dispatch when the user has
	// blown the monthly soft cap. We write bot_status='quota_exceeded'
	// so the UI surfaces the reason instead of a silent skip; subsequent
	// scheduler ticks see the terminal status and won't retry.
	// Fail-open on infra errors (matches the HTTP middleware policy).
	if s.quotaService != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		status, err := s.quotaService.Check(ctx, meeting.UserID)
		cancel()
		if err != nil {
			slog.Error("Quota check failed before bot dispatch — proceeding (fail-open)",
				"meeting_id", meeting.ID, "user_id", meeting.UserID, "error", err)
		} else if status.Exceeded {
			metrics.QuotaRejections.WithLabelValues(status.Tier, "scheduler:bot").Inc()
			slog.Info("Quota exceeded — skipping bot dispatch",
				"meeting_id", meeting.ID,
				"user_id", meeting.UserID,
				"tier", status.Tier,
				"cycle_resets_at", status.CycleEndAt)
			if err := s.updateBotStatus(meeting.ID, "quota_exceeded"); err != nil {
				slog.Error("Failed to set bot_status=quota_exceeded",
					"meeting_id", meeting.ID, "error", err)
			}
			return
		}
	}

	slog.Info("Joining meeting", "meeting_title", meeting.Title, "meeting_id", meeting.ID, "start_time", meeting.StartTime.Format("15:04"))

	// Mark as attempting to join (to prevent duplicates)
	if err := s.updateBotStatus(meeting.ID, "joining"); err != nil {
		slog.Error("Failed to update bot status", "meeting_id", meeting.ID, "error", err)
		return
	}

	// Get user's custom bot name and language from profile
	botName := "MIA Assistant" // Default
	botLanguage := ""
	if s.profileService != nil && meeting.UserID != uuid.Nil {
		profile, err := s.profileService.GetProfile(meeting.UserID)
		if err == nil {
			if profile.BotName != "" {
				botName = profile.BotName
			}
			botLanguage = profile.BotLanguage
		}
	}

	// Build dispatch request — omit language when "auto" so the bot
	// falls back to ElevenLabs auto-detection.
	req := BotDispatchRequest{
		MeetingID:  meeting.ID,
		MeetingURL: *meeting.MeetingURL,
		BotName:    botName,
	}
	if botLanguage != "" && botLanguage != "auto" {
		req.Language = botLanguage
	}

	// Guard: dispatcher must be wired (default is set in
	// NewSchedulerService; tests may inject a fake).
	if s.botDispatcher == nil {
		slog.Error("Bot dispatcher not configured", "meeting_id", meeting.ID)
		s.updateBotStatus(meeting.ID, "failed")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.botDispatcher.DispatchBot(ctx, req); err != nil {
		slog.Error("DispatchBot failed", "meeting_title", meeting.Title, "meeting_id", meeting.ID, "error", err)
		s.updateBotStatus(meeting.ID, "failed")
		return
	}

	slog.Info("Bot dispatched for meeting", "meeting_title", meeting.Title, "meeting_id", meeting.ID)
	s.updateBotStatus(meeting.ID, "connecting")
}

// countActiveBotMeetings returns how many meetings currently have a bot
// in flight. Counts bot_status IN ('joining','connecting','recording')
// to cap concurrent ACA Job executions (Phase 7.10 MAX_CONCURRENT_BOT_JOBS).
func (s *SchedulerService) countActiveBotMeetings() (int, error) {
	repo := repository.NewMeetingRepository()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return repo.CountActiveBotMeetings(ctx)
}

// updateBotStatus updates the bot_status field in the database
// Note: bot_joined_at is intentionally NOT set here. It's set by HandleBotEvent when 'joined' event is received.
func (s *SchedulerService) updateBotStatus(meetingID uuid.UUID, status string) error {
	query := `UPDATE meetings SET bot_status = $1 WHERE id = $2`
	_, err := repository.DB.Exec(context.Background(), query, status, meetingID)
	if err != nil {
		return fmt.Errorf("failed to update bot status: %w", err)
	}
	return nil
}

// cleanupStaleMeetings marks meetings as 'completed' if:
// 1. end_time has passed AND no heartbeat for 3 minutes
// 2. OR no heartbeat for 10 minutes (regardless of end_time - bot crashed mid-meeting)
// This prevents "zombie" meetings from getting stuck indefinitely.
func (s *SchedulerService) cleanupStaleMeetings() {
	now := time.Now().UTC()

	// Query for stale meetings:
	// - bot_status is 'joined' or 'recording' (active state)
	// - Either: (end_time passed AND heartbeat > 3 min ago) OR (heartbeat > 10 min ago)
	query := `
		UPDATE meetings 
		SET bot_status = 'completed'
		WHERE bot_status IN ('joined', 'recording', 'connecting')
		  AND (
		      -- Rule 1: Meeting ended + 3 min heartbeat timeout
		      (end_time < $1 AND (last_heartbeat_at IS NULL OR last_heartbeat_at < $1 - INTERVAL '3 minutes'))
		      OR
		      -- Rule 2: 10 min heartbeat timeout (bot crashed mid-meeting)
		      (last_heartbeat_at IS NOT NULL AND last_heartbeat_at < $1 - INTERVAL '10 minutes')
		  )
		RETURNING id, title
	`

	rows, err := repository.DB.Query(context.Background(), query, now)
	if err != nil {
		slog.Error("Error cleaning up stale meetings", "error", err)
		return
	}
	defer rows.Close()

	var cleaned int
	for rows.Next() {
		var id uuid.UUID
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			continue
		}
		cleaned++
		slog.Warn("Auto-completed stale meeting", "meeting_title", title, "meeting_id", id)
	}

	if cleaned > 0 {
		slog.Info("Zombie Meeting Cleaner completed", "count", cleaned)
	}
}

type meetingForStreamingUpdate struct {
	ID    uuid.UUID
	Title string
}

// checkAndRefreshStreamingBlocks incrementally updates transcript chat blocks for active meetings.
// This keeps chat_blocks nearly real-time and reduces finalization workload at meeting end.
func (s *SchedulerService) checkAndRefreshStreamingBlocks() {
	now := time.Now().UTC()

	query := `
		SELECT m.id, m.title
		FROM meetings m
		WHERE m.start_time < $1
		  AND m.end_time > $1
		  AND m.bot_status IN ('joined', 'recording')
		ORDER BY m.start_time ASC
		LIMIT 20
	`

	rows, err := repository.DB.Query(context.Background(), query, now)
	if err != nil {
		slog.Error("Error querying meetings for streaming block refresh", "error", err)
		return
	}
	defer rows.Close()

	var meetings []meetingForStreamingUpdate
	for rows.Next() {
		var m meetingForStreamingUpdate
		if err := rows.Scan(&m.ID, &m.Title); err != nil {
			slog.Error("Error scanning streaming meeting", "error", err)
			continue
		}
		meetings = append(meetings, m)
	}

	if len(meetings) == 0 {
		return
	}

	for _, meeting := range meetings {
		if lastAttempt, ok := s.streamingAttempts.Load(meeting.ID); ok {
			if lastAt, valid := lastAttempt.(time.Time); valid && time.Since(lastAt) < s.streamingInterval {
				continue
			}
		}

		s.streamingAttempts.Store(meeting.ID, now)

		go func(m meetingForStreamingUpdate) {
			s.streamingSemaphore <- struct{}{}
			defer func() { <-s.streamingSemaphore }()

			if err := s.correctionService.ProcessMeetingTranscriptIncremental(m.ID); err != nil {
				slog.Warn("Streaming transcript update failed", "meeting_id", m.ID, "error", err)
			}
		}(meeting)
	}
}

// meetingForSummarization identifies a meeting that needs post-meeting processing
type meetingForSummarization struct {
	ID         uuid.UUID
	Title      string
	MeetingURL *string
}

// checkAndSummarizeMeetings is a **reconciler** safety net. The canonical
// post-meeting trigger (Phase 3.5) is the ElevenLabs webhook, which calls
// ProcessMeetingCompletion *after* transcript_segments are persisted.
// This reconciler exists only to catch meetings where the webhook enqueue
// failed or the background job was dropped — it explicitly requires
// transcription_status='completed' so it can never race ahead of
// ingestion and produce a no_transcript summary.
//
// Phase 8 fix 2026-04-20 — previously gated on bot_status only, which
// left the old race wide open (bot reports completed → we try to
// summarize → transcript_segments still empty → summary = "no_transcript"
// even though ElevenLabs later succeeds). Now the query requires
// transcription_status='completed' as a hard precondition.
func (s *SchedulerService) checkAndSummarizeMeetings() {
	// Fetch meetings that ended within 24h AND:
	// 1. Transcription COMPLETED (audio has been transcribed by ElevenLabs)
	// 2. Summary status for 'final_detailed' is not a terminal success state
	//    (completed/completed_with_gaps) nor currently processing
	// 3. Retry count is < 3
	// bot_status is NOT part of the gate anymore — the desktop capture
	// path and the webhook-only completion path both drive bot_status
	// differently, but both honour transcription_status.
	query := `
		SELECT m.id, m.title, m.meeting_url
		FROM meetings m
		LEFT JOIN meeting_summaries ms ON m.id = ms.meeting_id AND ms.summary_type = 'final_detailed'
		WHERE m.end_time < $1
		  AND m.end_time > $1 - INTERVAL '24 hours'
		  AND m.transcription_status = 'completed'
		  AND (
		      ms.id IS NULL
		      OR (
		          ms.processing_status NOT IN ('completed', 'completed_with_gaps', 'processing')
		          AND COALESCE(ms.retry_count, 0) < 3
		      )
		  )
		ORDER BY m.end_time DESC
		LIMIT 5
	`

	rows, err := repository.DB.Query(context.Background(), query, time.Now().UTC())
	if err != nil {
		slog.Error("Error querying meetings for summarization", "error", err)
		return
	}
	defer rows.Close()

	var meetings []meetingForSummarization
	for rows.Next() {
		var m meetingForSummarization
		if err := rows.Scan(&m.ID, &m.Title, &m.MeetingURL); err != nil {
			slog.Error("Error scanning meeting for summarization", "error", err)
			continue
		}
		meetings = append(meetings, m)
	}

	if len(meetings) == 0 {
		return
	}

	slog.Info("Found meetings to auto-summarize", "count", len(meetings))

	for _, meeting := range meetings {
		s.autoSummarizeMeeting(meeting)
	}
}

// autoSummarizeMeeting delegates post-meeting processing to the unified PostMeetingService pipeline.
// This ensures a single code path for validation, lock, correction, summary generation, save, and email.
func (s *SchedulerService) autoSummarizeMeeting(meeting meetingForSummarization) {
	slog.Info("Auto-summarizing meeting", "meeting_title", meeting.Title, "meeting_id", meeting.ID)
	meetingURL := ""
	if meeting.MeetingURL != nil {
		meetingURL = *meeting.MeetingURL
	}
	if err := s.postMeetingService.ProcessMeetingCompletion(&meeting.ID, meetingURL); err != nil {
		if errors.Is(err, ErrPostMeetingDeferred) || errors.Is(err, ErrPostMeetingAlreadyProcessing) {
			slog.Info("Auto-summarize deferred for meeting", "meeting_id", meeting.ID, "reason", err)
			return
		}
		slog.Warn("Auto-summarize failed for meeting", "meeting_id", meeting.ID, "error", err)
	}
}


