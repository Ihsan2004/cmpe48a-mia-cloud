// Package services — transcription_service.go ties the batch STT pieces
// together. Two entry points:
//
//   SubmitForMeeting      — called by the transcribe background job worker.
//                           Resolves audio path, signs URL, POSTs to
//                           ElevenLabs, records request_id on meeting.
//   IngestWebhookResult   — called by handlers/webhook_elevenlabs.go
//                           after HMAC verification. Writes segments,
//                           flips meeting to completed, enqueues the
//                           post-meeting pipeline.
//
// Retry + attempt-count tracking is handled by the worker layer; this
// service stays deterministic so it's trivial to test.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"mia-website/backend/internal/models"
	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/storage"

	"github.com/google/uuid"
)

// TranscriptionService wires ElevenLabs client, storage, DB repositories.
type TranscriptionService struct {
	elevenLabs    *ElevenLabsBatchService
	storage       *storage.Client
	meetingRepo   repository.MeetingRepository
	transcript    repository.TranscriptRepository
	jobs          repository.BackgroundJobRepository
	cleanup       *AudioCleanupService // optional; set via SetAudioCleanup
	usageRecorder *UsageRecorder       // issue #39: STT cost accounting; nil disables recording

	// signedURLExpirySec is how long the ElevenLabs-facing signed URL stays
	// valid. 15 minutes is the roadmap default.
	signedURLExpirySec int

	// submitTimes tracks "when was this request_id submitted" so the
	// webhook can observe batch_transcription_duration_seconds (Phase 8.3).
	// Entries are deleted on ingest so the map never exceeds the count of
	// in-flight submissions (typically a handful). Process-local — a
	// restart loses the sample and the histogram just skips that
	// observation.
	submitTimes sync.Map // map[string]time.Time keyed by ElevenLabs request_id
}

// SetAudioCleanup wires the cleanup service so successful transcriptions
// immediately delete their audio (Phase 4 success path). Without this the
// audio lingers until the daily cron sweeps it.
func (s *TranscriptionService) SetAudioCleanup(c *AudioCleanupService) {
	s.cleanup = c
}

// SetUsageRecorder wires the issue #39 STT cost recorder. Optional —
// when unset (e.g. legacy tests, dev env with no DB) the webhook ingest
// path skips usage logging.
func (s *TranscriptionService) SetUsageRecorder(r *UsageRecorder) {
	s.usageRecorder = r
}

// NewTranscriptionService wires the default dependencies. elevenLabs may
// be nil (key not configured) — in that case SubmitForMeeting is a no-op
// that returns an error the worker surfaces as "disabled".
func NewTranscriptionService(el *ElevenLabsBatchService, store *storage.Client) *TranscriptionService {
	return &TranscriptionService{
		elevenLabs:         el,
		storage:            store,
		meetingRepo:        repository.NewMeetingRepository(),
		transcript:         repository.NewTranscriptRepository(),
		jobs:               repository.NewBackgroundJobRepository(),
		usageRecorder:      NewUsageRecorder(),
		signedURLExpirySec: 900,
	}
}

// NewTranscriptionServiceWithDeps is for tests that want to inject fakes.
func NewTranscriptionServiceWithDeps(
	el *ElevenLabsBatchService,
	store *storage.Client,
	meetingRepo repository.MeetingRepository,
	transcript repository.TranscriptRepository,
	jobs repository.BackgroundJobRepository,
) *TranscriptionService {
	return &TranscriptionService{
		elevenLabs:         el,
		storage:            store,
		meetingRepo:        meetingRepo,
		transcript:         transcript,
		jobs:               jobs,
		signedURLExpirySec: 900,
	}
}

// ErrTranscriptionDisabled is returned when the ElevenLabs client is nil
// (e.g. key missing in env). The worker turns this into a permanent-fail
// without wasting retries.
var ErrTranscriptionDisabled = errors.New("transcription disabled: ElevenLabs not configured")

// SubmitForMeeting issues a batch STT job for the given meeting.
// Preconditions: meeting has audio_storage_path, audio hasn't been deleted.
//
// Side effects on success:
//   - meeting.transcription_provider_job_id = returned request_id
//   - meeting.transcription_status = "processing"
//   - meeting.transcription_attempt_count += 1
//
// On failure the worker (not this service) is responsible for bumping the
// attempt count and scheduling a retry.
func (s *TranscriptionService) SubmitForMeeting(ctx context.Context, meetingID uuid.UUID) error {
	if s.elevenLabs == nil {
		return ErrTranscriptionDisabled
	}
	if s.storage == nil {
		return fmt.Errorf("transcription: storage client is nil")
	}

	meeting, err := s.meetingRepo.GetMeetingByIDSystem(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("transcription: load meeting: %w", err)
	}
	if meeting.AudioStoragePath == nil || *meeting.AudioStoragePath == "" {
		return fmt.Errorf("transcription: meeting %s has no audio path", meetingID)
	}
	if meeting.AudioDeletedAt != nil {
		return fmt.Errorf("transcription: meeting %s audio already deleted", meetingID)
	}

	// Phase 4 fix 2026-04-20 — counter bumps on EVERY submit attempt, not
	// just successful ones. Previously the meeting row could show
	// transcription_attempt_count=0 while background_jobs.attempts=3 and
	// transcription_status=failed after retry exhaust, which confused ops
	// diagnostics. The atomic increment is done before the external call
	// so even if Submit panics/times-out we've recorded the attempt.
	if err := s.meetingRepo.IncrementTranscriptionAttemptCount(ctx, meetingID); err != nil {
		// Non-fatal: the attempt still proceeds; we just lost a counter bump.
		slog.Warn("transcription: attempt counter bump failed",
			"meeting_id", meetingID, "error", err)
	}

	signed, err := s.storage.CreateSignedURL(ctx, *meeting.AudioStoragePath, s.signedURLExpirySec)
	if err != nil {
		return fmt.Errorf("transcription: sign audio url: %w", err)
	}

	resp, err := s.elevenLabs.Submit(ctx, SubmitOptions{
		AudioURL: signed.URL,
		Diarize:  false,
	})
	if err != nil {
		return err // preserve *ElevenLabsAPIError so worker can check IsRetryable()
	}

	// Persist the provider job id + flip to processing. Attempt counter
	// already bumped above (success path reuses the earlier increment).
	if err := s.meetingRepo.UpdateMeetingFields(ctx, meetingID, map[string]any{
		"transcription_provider_job_id": resp.RequestID,
		"transcription_status":          "processing",
	}); err != nil {
		return fmt.Errorf("transcription: update meeting post-submit: %w", err)
	}
	// Phase 8.3 — record submit time so IngestWebhookResult can emit
	// batch_transcription_duration_seconds when the webhook lands.
	s.submitTimes.Store(resp.RequestID, time.Now())

	// Issue #39 (review fix 2026-05): record STT usage HERE, not on the
	// webhook success path. ElevenLabs bills the moment they accept the
	// audio for processing — webhook errors, lost callbacks, or provider
	// retries do NOT undo that charge. Recording at submit means:
	//   - happy path: usage logged once
	//   - webhook arrives with error: usage stays (matches real billing)
	//   - webhook never arrives: usage stays (matches real billing)
	//   - submit fails outright (above): nothing logged ✓
	// audio_duration_seconds is populated at upload-finalize time, well
	// before this service runs. nil/zero → fail-soft + slog.Warn so the
	// anomaly is observable; drift cron can't paper over it because the
	// counter and usage_logs would both be missing the entry.
	if s.usageRecorder != nil {
		audioSeconds := 0.0
		if meeting.AudioDurationSeconds != nil && *meeting.AudioDurationSeconds > 0 {
			audioSeconds = float64(*meeting.AudioDurationSeconds)
		}
		if audioSeconds > 0 {
			s.usageRecorder.Record(
				meeting.UserID, nil, &meeting.ID,
				"stt", "elevenlabs", "scribe_v2",
				0, 0, audioSeconds,
				map[string]any{"request_id": resp.RequestID},
			)
		} else {
			slog.Warn("transcription: STT usage skipped — audio_duration_seconds missing at submit",
				"meeting_id", meetingID, "request_id", resp.RequestID)
		}
	}

	slog.Info("transcription submitted",
		"meeting_id", meetingID,
		"request_id", resp.RequestID)
	return nil
}

// WebhookPayload is the subset of the ElevenLabs webhook body we care
// about. Additional fields (audio_events, language_probability, etc.) are
// tolerated via json unmarshalling of unknown fields.
//
// The current Scribe v2 delivery wraps the words/text inside a
// `transcription` sub-object of the envelope's `data` field; older
// flat-payload deliveries put `words` and `text` directly at the top
// level we receive after the `{type, data:{...}}` envelope is unwrapped.
// Parse both shapes — at IngestWebhookResult time we pick whichever has
// content. The diagnostic showed:
//   data keys = [audio_duration_secs, character_cost, request_id,
//                transcription, transcripts, webhook_metadata]
type WebhookPayload struct {
	RequestID string           `json:"request_id"`
	Status    string           `json:"status"`          // legacy / safety net
	Error     string           `json:"error,omitempty"` // legacy / safety net
	Words     []ElevenLabsWord `json:"words,omitempty"` // legacy flat shape
	FullText  string           `json:"text,omitempty"`  // legacy flat shape

	// Scribe v2 nested shape — current production deliveries put the
	// transcript here.
	Transcription *struct {
		LanguageCode        string           `json:"language_code"`
		LanguageProbability float64          `json:"language_probability"`
		Text                string           `json:"text"`
		Words               []ElevenLabsWord `json:"words"`
	} `json:"transcription,omitempty"`
}

// IngestWebhookResult processes an already-authenticated webhook payload.
// Caller (handler) must have verified the HMAC signature first.
//
// Behaviour:
//   - status=ok: replace any existing segments, flip meeting to completed,
//     enqueue post-meeting pipeline so correction + summary + extraction run.
//   - status=error: flip meeting to failed, record the error message.
//     The background retry logic is upstream of the webhook — by the time
//     ElevenLabs tells us "error", we're done trying for this attempt.
func (s *TranscriptionService) IngestWebhookResult(ctx context.Context, payload WebhookPayload) error {
	if payload.RequestID == "" {
		metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageWebhook).Inc()
		return fmt.Errorf("webhook: request_id missing")
	}
	meeting, err := s.findMeetingByRequestID(ctx, payload.RequestID)
	if err != nil {
		metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageWebhook).Inc()
		return err
	}

	// Resolve the actual transcript content from whichever of the two
	// payload shapes the provider delivered (see WebhookPayload doc).
	words := payload.Words
	fullText := payload.FullText
	if payload.Transcription != nil {
		if len(payload.Transcription.Words) > 0 {
			words = payload.Transcription.Words
		}
		if payload.Transcription.Text != "" {
			fullText = payload.Transcription.Text
		}
	}

	// ElevenLabs Scribe success is implicit: no status field on the
	// success delivery. Empty content is treated as a degraded success
	// (silent audio) — mark completed with a warning so the pipeline
	// progresses rather than failing it. Only branch to markFailed on
	// an explicit error string.
	if len(words) == 0 && strings.TrimSpace(fullText) == "" {
		slog.Warn("webhook: empty transcription content (silent audio?)",
			"meeting_id", meeting.ID,
			"request_id", payload.RequestID,
		)
	}
	if payload.Error != "" {
		slog.Warn("webhook: non-empty error field on success-shaped payload",
			"meeting_id", meeting.ID,
			"error", payload.Error,
		)
		metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageWebhook).Inc()
		return s.markFailed(ctx, meeting.ID, payload.Error)
	}

	segments := MapWordsToSegments(meeting.ID, words)
	if len(segments) == 0 {
		// Transcription succeeded but no words were returned (e.g. silent
		// audio). Mark completed anyway — downstream shows "no transcript".
		slog.Warn("webhook: ok status with no words", "meeting_id", meeting.ID, "request_id", payload.RequestID)
	} else {
		// Clean slate in case of a retry — otherwise we'd stack duplicates.
		if err := s.transcript.DeleteSegmentsByMeetingID(ctx, meeting.ID); err != nil {
			metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageWebhook).Inc()
			return fmt.Errorf("webhook: clear old segments: %w", err)
		}
		if err := s.transcript.InsertSegments(ctx, meeting.ID, segments); err != nil {
			metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageWebhook).Inc()
			return fmt.Errorf("webhook: insert segments: %w", err)
		}
	}

	if err := s.meetingRepo.UpdateMeetingFields(ctx, meeting.ID, map[string]any{
		"transcription_status":       "completed",
		"transcription_completed_at": time.Now().UTC(),
	}); err != nil {
		metrics.BatchTranscriptionFailed.WithLabelValues(metrics.FailStageWebhook).Inc()
		return fmt.Errorf("webhook: mark meeting completed: %w", err)
	}

	// Segments + status persisted — this is the canonical "completed" signal.
	metrics.BatchTranscriptionCompleted.Inc()

	// STT usage is intentionally NOT recorded here anymore (issue #39
	// review fix 2026-05). It is recorded in SubmitForMeeting at the
	// moment ElevenLabs accepts the audio, which is when their billing
	// clock starts. Recording on webhook success used to under-count
	// every dropped / errored / never-arriving webhook.

	// Phase 8.3 — observe submit→ingest duration if we have the sample.
	// Missing entry means a process restart between submit and ingest; we
	// skip rather than emit a misleading zero.
	if submittedAt, ok := s.submitTimes.LoadAndDelete(payload.RequestID); ok {
		if t, ok := submittedAt.(time.Time); ok {
			metrics.BatchTranscriptionDurationSeconds.Observe(time.Since(t).Seconds())
		}
	}

	// Hand off to the existing post-meeting pipeline. This runs correction
	// + summary + extraction (roadmap 3.5). Use the same background worker
	// pattern the bot-completion webhook uses so behaviour stays consistent.
	if _, err := s.jobs.EnqueuePostMeetingCompletion(ctx, meeting.ID, "", 5); err != nil {
		slog.Error("webhook: enqueue post-meeting failed", "meeting_id", meeting.ID, "error", err)
		// Don't return: segments + status are already saved. A retry could
		// be handled by a reconciler or manual trigger.
	}

	// Phase 4 success path: drop the audio from storage now that we have
	// the transcript. Best-effort — a failure here just leaves the audio
	// for the cleanup cron to sweep later.
	if s.cleanup != nil {
		if err := s.cleanup.DeleteForMeeting(ctx, meeting.ID); err != nil {
			slog.Warn("webhook: immediate audio delete failed",
				"meeting_id", meeting.ID, "error", err)
		}
	}

	slog.Info("webhook: transcription ingested",
		"meeting_id", meeting.ID,
		"request_id", payload.RequestID,
		"segment_count", len(segments))
	return nil
}

func (s *TranscriptionService) markFailed(ctx context.Context, meetingID uuid.UUID, errMessage string) error {
	if errMessage == "" {
		errMessage = "ElevenLabs reported an error with no message"
	}
	slog.Warn("webhook: transcription failed", "meeting_id", meetingID, "error", errMessage)
	return s.meetingRepo.UpdateMeetingFields(ctx, meetingID, map[string]any{
		"transcription_status":     "failed",
		"transcription_last_error": truncateError(errMessage),
	})
}

// MarkTranscriptionFailed is the external hook used by the background job
// worker when a transcribe job exhausts its retries. Exposes the same
// flag-and-record behaviour the webhook error path uses so the UI state
// stays consistent regardless of which path reached the failure.
func (s *TranscriptionService) MarkTranscriptionFailed(ctx context.Context, meetingID uuid.UUID, errMessage string) error {
	return s.markFailed(ctx, meetingID, errMessage)
}

// findMeetingByRequestID resolves a meeting from the provider request_id
// we stored on submit. Routed through the injected MeetingRepository
// (Phase 3 fix 2026-04-20) so tests can exercise IngestWebhookResult
// without a real DB pool.
func (s *TranscriptionService) findMeetingByRequestID(ctx context.Context, requestID string) (*models.Meeting, error) {
	id, err := s.meetingRepo.GetMeetingIDByTranscriptionJob(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("webhook: no meeting for request_id %s: %w", requestID, err)
	}
	m, err := s.meetingRepo.GetMeetingByIDSystem(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("webhook: load meeting %s: %w", id, err)
	}
	return m, nil
}

func truncateError(msg string) string {
	const max = 512
	if len(msg) > max {
		return msg[:max] + "..."
	}
	return msg
}

