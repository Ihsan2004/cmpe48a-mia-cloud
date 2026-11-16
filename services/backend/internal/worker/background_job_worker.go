package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/services/briefnotify"

	"github.com/google/uuid"
)

const (
	defaultBackgroundWorkers        = 4
	defaultBackgroundPollIntervalMs = 1000
	defaultBackgroundMaxAttempts    = 5
	maxBackoffDelay                 = 10 * time.Minute
)

type BackgroundJobWorker struct {
	repo                 repository.BackgroundJobRepository
	postMeetingService   *services.PostMeetingService
	transcriptionService *services.TranscriptionService // nil when ElevenLabs disabled
	briefDispatcher      *briefnotify.Dispatcher        // nil when notification pipeline not wired
	briefContextLoader   briefnotify.ContextLoader      // paired with briefDispatcher
	workers              int
	pollInterval         time.Duration
	wg                   sync.WaitGroup
}

func NewBackgroundJobWorker(
	postMeetingService *services.PostMeetingService,
	transcriptionService *services.TranscriptionService,
	briefDispatcher *briefnotify.Dispatcher,
	briefContextLoader briefnotify.ContextLoader,
) *BackgroundJobWorker {
	workers := readEnvInt("BACKGROUND_JOB_WORKERS", defaultBackgroundWorkers)
	pollMs := readEnvInt("BACKGROUND_JOB_POLL_INTERVAL_MS", defaultBackgroundPollIntervalMs)
	if pollMs <= 0 {
		pollMs = defaultBackgroundPollIntervalMs
	}

	return &BackgroundJobWorker{
		repo:                 repository.NewBackgroundJobRepository(),
		postMeetingService:   postMeetingService,
		transcriptionService: transcriptionService,
		briefDispatcher:      briefDispatcher,
		briefContextLoader:   briefContextLoader,
		workers:              workers,
		pollInterval:         time.Duration(pollMs) * time.Millisecond,
	}
}

func (w *BackgroundJobWorker) Start(ctx context.Context) {
	if w.postMeetingService == nil {
		slog.Warn("Background job worker disabled: postMeetingService is nil")
		return
	}

	if w.workers <= 0 {
		slog.Warn("Background job worker disabled: workers <= 0")
		return
	}

	slog.Info("Background job worker started", "workers", w.workers, "poll_interval", w.pollInterval)

	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.runWorker(ctx, i)
	}

	<-ctx.Done()
	w.wg.Wait()
	slog.Info("Background job worker stopped")
}

func (w *BackgroundJobWorker) runWorker(ctx context.Context, index int) {
	defer w.wg.Done()
	workerID := fmt.Sprintf("bg-worker-%d", index+1)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := w.repo.ClaimNextPending(ctx, workerID)
		if err != nil {
			slog.Error("Failed to claim background job", "worker_id", workerID, "error", err)
			if !sleepOrDone(ctx, w.pollInterval) {
				return
			}
			continue
		}

		if job == nil {
			if !sleepOrDone(ctx, w.pollInterval) {
				return
			}
			continue
		}

		if err := w.handleJob(ctx, job); err != nil {
			w.handleJobFailure(ctx, workerID, job, err)
			continue
		}

		if err := w.repo.MarkCompleted(ctx, job.ID); err != nil {
			slog.Error("Failed to mark job completed", "worker_id", workerID, "job_id", job.ID, "error", err)
		}
	}
}

func (w *BackgroundJobWorker) handleJob(ctx context.Context, job *models.BackgroundJob) error {
	if job == nil {
		return fmt.Errorf("job is nil")
	}

	switch job.JobType {
	case models.BackgroundJobTypePostMeetingCompletion:
		var payload models.PostMeetingCompletionJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("invalid post_meeting_completion payload: %w", err)
		}
		if payload.MeetingID == uuid.Nil {
			return fmt.Errorf("invalid post_meeting_completion payload: meeting_id is empty")
		}

		err := w.postMeetingService.ProcessMeetingCompletion(&payload.MeetingID, payload.MeetingURL)
		if err != nil {
			if errors.Is(err, services.ErrPostMeetingAlreadyProcessing) {
				// Another actor already owns this meeting processing lock.
				return nil
			}
			return err
		}
		return nil

	case models.BackgroundJobTypeTranscribe:
		if w.transcriptionService == nil {
			return fmt.Errorf("transcribe job: TranscriptionService not configured")
		}
		var payload models.TranscribeJobPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("invalid transcribe payload: %w", err)
		}
		if payload.MeetingID == uuid.Nil {
			return fmt.Errorf("invalid transcribe payload: meeting_id is empty")
		}
		if err := w.transcriptionService.SubmitForMeeting(ctx, payload.MeetingID); err != nil {
			metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageSubmit).Inc()
			return err
		}
		return nil

	case models.BackgroundJobTypeNotifyBrief:
		if w.briefDispatcher == nil {
			// Safe default: if the notification pipeline isn't wired
			// (e.g. Phase 1 deployment without deliverers), treat the
			// job as no-op rather than crashing. The row stays in
			// brief_deliveries for post-hoc audit.
			slog.Warn("notify_brief job received but dispatcher not wired; skipping",
				"job_id", job.ID)
			return nil
		}
		var payload models.NotifyBriefPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return fmt.Errorf("invalid notify_brief payload: %w", err)
		}
		if payload.DeliveryID == uuid.Nil {
			return fmt.Errorf("invalid notify_brief payload: delivery_id is empty")
		}
		return w.briefDispatcher.Execute(ctx, payload.DeliveryID, w.briefContextLoader)

	default:
		return fmt.Errorf("unsupported background job type: %s", job.JobType)
	}
}

func (w *BackgroundJobWorker) handleJobFailure(ctx context.Context, workerID string, job *models.BackgroundJob, err error) {
	if job == nil {
		return
	}

	errMessage := err.Error()
	if job.Attempts >= job.MaxAttempts {
		if dlqErr := w.repo.MoveToDLQ(ctx, job, errMessage); dlqErr != nil {
			slog.Error("Failed to move job to DLQ", "worker_id", workerID, "job_id", job.ID, "error", dlqErr)
		} else {
			slog.Warn("Job moved to DLQ", "worker_id", workerID, "job_id", job.ID, "attempts", job.Attempts, "error", err)
		}
		// Transcribe jobs that exhaust retries flip the meeting into
		// transcription_status=failed so the UI can surface a friendly
		// message instead of silently stalling at "processing".
		if job.JobType == models.BackgroundJobTypeTranscribe && w.transcriptionService != nil {
			metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageRetryExhausted).Inc()
			var payload models.TranscribeJobPayload
			if unmarshalErr := json.Unmarshal(job.Payload, &payload); unmarshalErr == nil && payload.MeetingID != uuid.Nil {
				if markErr := w.transcriptionService.MarkTranscriptionFailed(ctx, payload.MeetingID, errMessage); markErr != nil {
					slog.Error("Failed to mark meeting transcription failed",
						"meeting_id", payload.MeetingID, "error", markErr)
				}
			}
		}
		return
	}

	backoff := computeBackoffForJob(job)
	nextRunAt := time.Now().UTC().Add(backoff)
	if retryErr := w.repo.MarkForRetry(ctx, job.ID, nextRunAt, errMessage); retryErr != nil {
		slog.Error("Failed to schedule retry for job", "worker_id", workerID, "job_id", job.ID, "error", retryErr)
		return
	}

	slog.Info("Job retry scheduled", "worker_id", workerID, "job_id", job.ID, "attempt", job.Attempts, "max_attempts", job.MaxAttempts, "next_in", backoff, "error", err)
}

func EnqueuePostMeetingCompletionJob(ctx context.Context, meetingID uuid.UUID, meetingURL string) (uuid.UUID, error) {
	repo := repository.NewBackgroundJobRepository()
	return repo.EnqueuePostMeetingCompletion(ctx, meetingID, meetingURL, defaultBackgroundMaxAttempts)
}

func computeRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 15 * time.Second
	}

	// 15s, 30s, 60s, 120s...
	delay := 15 * time.Second * time.Duration(1<<(attempt-1))
	if delay > maxBackoffDelay {
		return maxBackoffDelay
	}
	return delay
}

// computeBackoffForJob picks a per-job-type backoff schedule.
// Transcribe jobs retry faster (1s/4s/16s per roadmap karar #6) because
// most ElevenLabs failures are transient API issues — waiting 15-120s
// would keep the user in "processing" state unnecessarily long.
//
// notify_brief uses 30s / 2min / 10min (brief delivery roadmap Karar
// #14): longer than transcribe because SMTP greylisting + Slack 429
// both benefit from a slower cadence, but short enough that all three
// attempts still complete well within the user's 15-minute lead time.
func computeBackoffForJob(job *models.BackgroundJob) time.Duration {
	if job == nil {
		return 15 * time.Second
	}
	switch job.JobType {
	case models.BackgroundJobTypeTranscribe:
		return computeTranscribeBackoff(job.Attempts)
	case models.BackgroundJobTypeNotifyBrief:
		return computeNotifyBriefBackoff(job.Attempts)
	}
	return computeRetryBackoff(job.Attempts)
}

// computeNotifyBriefBackoff: 30s (attempt 1 → 2), 2min (2 → 3),
// 10min (3 → DLQ would be next but MaxAttempts caps us).
// Capped at maxBackoffDelay to guard against unexpected high attempts.
func computeNotifyBriefBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 30 * time.Second
	case attempt == 2:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// computeTranscribeBackoff yields 1s, 4s, 16s (4^n seconds) for attempts
// 1-3. Matches the roadmap Phase 4 retry schedule. Caps early to prevent
// int64 overflow at very high attempt numbers.
func computeTranscribeBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return time.Second
	}
	// 1s * 4^(attempt-1). Cap the exponent before the multiplication so
	// we never overflow time.Duration (int64 ns). 4^30 seconds already
	// exceeds maxBackoffDelay so capping at 30 is safe.
	exp := attempt - 1
	if exp > 30 {
		exp = 30
	}
	mult := int64(1)
	for i := 0; i < exp; i++ {
		mult *= 4
	}
	delay := time.Duration(mult) * time.Second
	if delay > maxBackoffDelay || delay <= 0 {
		return maxBackoffDelay
	}
	return delay
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func readEnvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
