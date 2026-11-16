// Package services — audio_cleanup_service.go removes meeting audio from
// Supabase Storage once it's no longer needed.
//
// Two triggers:
//
//   1. Success path (real-time): TranscriptionService.IngestWebhookResult
//      calls DeleteForMeeting as soon as the transcript is persisted.
//
//   2. Cron safety net (daily): RunCleanup sweeps completed meetings whose
//      audio lingered past the retention window and failed meetings past
//      the debug window (see roadmap Phase 4).
//
// The service is deliberately side-effect-isolated: DB + Storage only.
// No webhooks, no notifications, no user-facing state changes beyond
// setting audio_deleted_at.
package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/storage"

	"github.com/google/uuid"
)

// Retention windows — match roadmap Karar #7-#9.
const (
	// SuccessRetention defines how long a successfully-transcribed meeting's
	// audio is allowed to linger before the cron sweeps it. The webhook
	// tries to delete immediately; this window only catches the tail where
	// the webhook delete raced a crash.
	SuccessRetention = 48 * time.Hour
	// FailedRetention keeps a failed meeting's audio around for debugging.
	FailedRetention = 7 * 24 * time.Hour
)

// CleanupReport summarises a single cron pass.
type CleanupReport struct {
	DeletedSuccessful int
	DeletedFailed     int
	Errors            int
	// OrphansSeen — Phase 4 fix 2026-04-20, roadmap 4.3 leg 3: objects
	// present in Supabase but with no DB row referencing them. MVP does
	// NOT auto-delete (too easy to wipe in-flight uploads); the count is
	// logged for ops triage. Set to -1 when the audit pass errored.
	OrphansSeen int
}

// AudioCleanupService owns the storage + meeting repo handles. Safe to share
// across goroutines — both underlying clients are concurrency-safe.
type AudioCleanupService struct {
	storage     *storage.Client
	meetingRepo repository.MeetingRepository

	// override points for tests
	nowFn       func() time.Time
	listCandidates func(ctx context.Context, cutoff CleanupCutoffs) ([]AudioCleanupCandidate, error)
}

// NewAudioCleanupService wires the default dependencies. storage may be nil
// in tests or disabled environments; callers should guard with nil check.
func NewAudioCleanupService(store *storage.Client) *AudioCleanupService {
	s := &AudioCleanupService{
		storage:     store,
		meetingRepo: repository.NewMeetingRepository(),
		nowFn:       time.Now,
	}
	s.listCandidates = s.defaultListCandidates
	return s
}

// NewAudioCleanupServiceWithDeps is for tests that want to mock DB access.
func NewAudioCleanupServiceWithDeps(
	store *storage.Client,
	meetingRepo repository.MeetingRepository,
	lister func(ctx context.Context, cutoff CleanupCutoffs) ([]AudioCleanupCandidate, error),
	now func() time.Time,
) *AudioCleanupService {
	return &AudioCleanupService{
		storage:        store,
		meetingRepo:    meetingRepo,
		nowFn:          now,
		listCandidates: lister,
	}
}

// AudioCleanupCandidate is a row the cron considers for deletion.
type AudioCleanupCandidate struct {
	MeetingID            uuid.UUID
	Status               string // "completed" | "failed"
	AudioStoragePath     string
}

// CleanupCutoffs is what the cron query filters on. Exposed so tests can
// assert the exact thresholds passed through.
type CleanupCutoffs struct {
	SuccessBefore time.Time // rows with transcription_status=completed older than this
	FailedBefore  time.Time // rows with transcription_status=failed older than this
}

// DeleteForMeeting wipes a single meeting's audio from Supabase and sets
// audio_deleted_at. No-op (without error) when the meeting has no audio
// path or was already deleted. Called from the webhook success path.
func (s *AudioCleanupService) DeleteForMeeting(ctx context.Context, meetingID uuid.UUID) error {
	if s.storage == nil {
		return fmt.Errorf("audio cleanup: storage client is nil")
	}
	meeting, err := s.meetingRepo.GetMeetingByIDSystem(ctx, meetingID)
	if err != nil {
		return fmt.Errorf("audio cleanup: load meeting %s: %w", meetingID, err)
	}
	if meeting.AudioDeletedAt != nil {
		return nil // already cleaned up
	}
	if meeting.AudioStoragePath == nil || *meeting.AudioStoragePath == "" {
		return nil // never had audio
	}

	if err := s.storage.Delete(ctx, *meeting.AudioStoragePath); err != nil {
		return fmt.Errorf("audio cleanup: delete %s: %w", *meeting.AudioStoragePath, err)
	}

	// Clear the path + set deleted_at so future cron passes skip this row.
	if err := s.meetingRepo.UpdateMeetingFields(ctx, meetingID, map[string]any{
		"audio_deleted_at":   s.nowFn().UTC(),
		"audio_storage_path": nil,
	}); err != nil {
		return fmt.Errorf("audio cleanup: mark deleted: %w", err)
	}
	metrics.AudioStorageDeleted.Inc()
	slog.Info("audio cleanup: deleted", "meeting_id", meetingID)
	return nil
}

// RunCleanup sweeps stale audio objects. Called by the daily cron.
// Always returns a report — individual failures are logged but don't
// short-circuit the whole sweep.
func (s *AudioCleanupService) RunCleanup(ctx context.Context) (CleanupReport, error) {
	if s.storage == nil {
		return CleanupReport{}, fmt.Errorf("audio cleanup: storage client is nil")
	}
	now := s.nowFn()
	cutoffs := CleanupCutoffs{
		SuccessBefore: now.Add(-SuccessRetention),
		FailedBefore:  now.Add(-FailedRetention),
	}
	candidates, err := s.listCandidates(ctx, cutoffs)
	if err != nil {
		return CleanupReport{}, fmt.Errorf("audio cleanup: list candidates: %w", err)
	}

	var report CleanupReport
	for _, c := range candidates {
		if err := s.DeleteForMeeting(ctx, c.MeetingID); err != nil {
			slog.Warn("audio cleanup: candidate failed",
				"meeting_id", c.MeetingID,
				"status", c.Status,
				"error", err)
			report.Errors++
			continue
		}
		if c.Status == "completed" {
			report.DeletedSuccessful++
		} else if c.Status == "failed" {
			report.DeletedFailed++
		}
	}
	// Roadmap 4.3 leg 3 (Phase 4 fix 2026-04-20) — log-only orphan audit.
	// Anything in Supabase with no DB reference is surfaced for ops but
	// NOT deleted automatically: a storage object that's mid-upload (no
	// upload_sessions row yet if the InitUpload → CreateSession race is
	// lost) would be wrongly wiped. Once we add a proper two-phase
	// reconciler, this log-only pass becomes a safe input to it.
	orphans, auditErr := s.auditStorageOrphans(ctx)
	if auditErr != nil {
		slog.Warn("audio cleanup: orphan audit skipped", "error", auditErr)
		report.OrphansSeen = -1
	} else {
		report.OrphansSeen = len(orphans)
		if len(orphans) > 0 {
			slog.Warn("audio cleanup: storage orphans detected — not auto-deleted",
				"count", len(orphans),
				"sample", firstNStrings(orphans, 5))
		}
	}

	slog.Info("audio cleanup: run finished",
		"deleted_successful", report.DeletedSuccessful,
		"deleted_failed", report.DeletedFailed,
		"orphans_seen", report.OrphansSeen,
		"errors", report.Errors)
	return report, nil
}

// auditStorageOrphans lists every object under the configured audio bucket
// and returns the paths for which the DB has no meeting row referencing
// them. Non-destructive — caller decides what to do with the result
// (MVP: log only).
//
// Two indirect inputs make this expensive if done naively: each List()
// page returns up to N objects, and each object needs a SQL lookup. We
// batch the SQL into a single ANY($1) call.
func (s *AudioCleanupService) auditStorageOrphans(ctx context.Context) ([]string, error) {
	const rootPrefix = "" // empty prefix lists the whole bucket
	const pageSize = 1000

	objs, err := s.storage.List(ctx, rootPrefix, pageSize)
	if err != nil {
		return nil, fmt.Errorf("list bucket: %w", err)
	}
	if len(objs) == 0 {
		return nil, nil
	}

	paths := make([]string, 0, len(objs))
	for _, o := range objs {
		// Ignore staging scratch space; 005b_storage_bucket.sql calls this
		// bucket `_staging/...` in its path convention. Audit is about
		// *final* audio objects that should match a meeting row.
		if len(o.Name) >= len("_staging/") && o.Name[:len("_staging/")] == "_staging/" {
			continue
		}
		paths = append(paths, o.Name)
	}
	if len(paths) == 0 {
		return nil, nil
	}

	// Ask the DB which of these paths have a matching meeting row.
	query := `
		SELECT audio_storage_path
		FROM meetings
		WHERE audio_storage_path = ANY($1)`
	rows, err := repository.DB.Query(ctx, query, paths)
	if err != nil {
		return nil, fmt.Errorf("query known paths: %w", err)
	}
	defer rows.Close()

	known := make(map[string]struct{}, len(paths))
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan known path: %w", err)
		}
		known[p] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var orphans []string
	for _, p := range paths {
		if _, ok := known[p]; !ok {
			orphans = append(orphans, p)
		}
	}
	return orphans, nil
}

// firstNStrings returns up to n elements for log truncation. Cheap helper
// so the orphan log doesn't dump a thousand paths.
func firstNStrings(s []string, n int) []string {
	if n < 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

// defaultListCandidates queries the meetings table directly. Kept inline
// because the shape is specific to this service and doesn't fit cleanly
// into the MeetingRepository interface (which has no other cleanup concerns).
func (s *AudioCleanupService) defaultListCandidates(ctx context.Context, cutoff CleanupCutoffs) ([]AudioCleanupCandidate, error) {
	// The idx_meetings_audio_cleanup partial index (migration 005) covers
	// this exact predicate combination.
	query := `
		SELECT id, transcription_status, audio_storage_path
		FROM meetings
		WHERE audio_storage_path IS NOT NULL
		  AND audio_deleted_at IS NULL
		  AND (
		    (transcription_status = 'completed' AND transcription_completed_at IS NOT NULL AND transcription_completed_at < $1)
		    OR
		    (transcription_status = 'failed' AND updated_at < $2)
		  )
		ORDER BY updated_at ASC
		LIMIT 500`
	rows, err := repository.DB.Query(ctx, query, cutoff.SuccessBefore, cutoff.FailedBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AudioCleanupCandidate
	for rows.Next() {
		var c AudioCleanupCandidate
		var path *string
		if err := rows.Scan(&c.MeetingID, &c.Status, &path); err != nil {
			return nil, err
		}
		if path != nil {
			c.AudioStoragePath = *path
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StartCron runs RunCleanup once on startup (safety-net drift catcher)
// and then on every `interval` tick aligned to 00:00 UTC.
//
// Phase 4 fix 2026-04-20 — the old implementation started a ticker
// immediately on process boot, so the daily run-time drifted with each
// deploy. Roadmap 4.3 contract is "daily at 24:00 UTC" — now the first
// scheduled run is delayed to the next UTC midnight, and subsequent runs
// march exactly `interval` apart (default 24h → always 00:00 UTC).
//
// The initial startup run is preserved so a fresh deploy mid-day still
// sweeps anything stale; it's separate from the aligned schedule.
func (s *AudioCleanupService) StartCron(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	// Run once on startup — safety net, independent of the midnight schedule.
	if _, err := s.RunCleanup(ctx); err != nil {
		slog.Error("audio cleanup cron: initial run failed", "error", err)
	}

	// Compute delay to next UTC midnight.
	now := time.Now().UTC()
	nextMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	initialDelay := nextMidnight.Sub(now)
	slog.Info("audio cleanup cron: next aligned run",
		"at_utc", nextMidnight, "in", initialDelay)

	firstTimer := time.NewTimer(initialDelay)
	defer firstTimer.Stop()

	// Wait for the first aligned tick, then hand off to a fixed-interval ticker.
	select {
	case <-ctx.Done():
		slog.Info("audio cleanup cron stopped")
		return
	case <-firstTimer.C:
		if _, err := s.RunCleanup(ctx); err != nil {
			slog.Error("audio cleanup cron: aligned run failed", "error", err)
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("audio cleanup cron stopped")
			return
		case <-ticker.C:
			if _, err := s.RunCleanup(ctx); err != nil {
				slog.Error("audio cleanup cron: run failed", "error", err)
			}
		}
	}
}
