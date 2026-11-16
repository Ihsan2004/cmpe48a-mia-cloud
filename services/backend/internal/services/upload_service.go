// Package services — upload_service.go orchestrates the resumable audio
// upload protocol used by the bot and desktop recorder.
//
// Chunk strategy: Supabase Storage does not support native appends, so
// chunks are staged as objects under `_staging/{session_id}/{seq:08d}` and
// concatenated into the final `{user_id}/{meeting_id}/audio.{ext}` on
// FinalizeUpload. The concat streams through an io.Pipe so we never buffer
// the full audio in backend memory.
//
// Ownership:
//   - User-initiated uploads verify meeting.user_id = caller's user_id.
//   - Bot uploads bypass that check (the BOT_API_KEY is the trust anchor);
//     callers pass AsBot() to skip ownership verification but still pin the
//     source to "bot".
package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/storage"

	"github.com/google/uuid"
)

// ErrMeetingNotFound is returned when a meeting doesn't exist or the caller
// doesn't own it (for user-initiated flows).
var ErrMeetingNotFound = errors.New("meeting not found")

// UploadSource tracks which client produced the audio — persisted on meeting.
type UploadSource string

const (
	UploadSourceBot     UploadSource = "bot"
	UploadSourceDesktop UploadSource = "desktop"
)

// UploadService is the orchestration boundary between handlers and
// storage/repository. Safe to share across requests.
type UploadService struct {
	repo    repository.UploadRepository
	meeting repository.MeetingRepository
	jobs    repository.BackgroundJobRepository
	storage *storage.Client
}

// NewUploadService wires the default dependencies.
func NewUploadService(store *storage.Client) *UploadService {
	return &UploadService{
		repo:    repository.NewUploadRepository(),
		meeting: repository.NewMeetingRepository(),
		jobs:    repository.NewBackgroundJobRepository(),
		storage: store,
	}
}

// NewUploadServiceWithDeps constructs the service with explicit dependencies.
// Exposed for tests so the DB-backed repositories can be swapped for fakes.
// jobs may be nil — FinalizeUpload + UploadWholeFile skip the transcribe
// enqueue step in that case.
func NewUploadServiceWithDeps(
	repo repository.UploadRepository,
	meeting repository.MeetingRepository,
	store *storage.Client,
) *UploadService {
	return &UploadService{repo: repo, meeting: meeting, jobs: nil, storage: store}
}

// WithJobs returns a copy of the service wired with the given background job
// repo. Tests compose their fakes this way.
func (s *UploadService) WithJobs(jobs repository.BackgroundJobRepository) *UploadService {
	cp := *s
	cp.jobs = jobs
	return &cp
}

// InitUploadOptions configures how InitUpload validates ownership + records source.
type InitUploadOptions struct {
	UserID      uuid.UUID
	MeetingID   uuid.UUID
	ContentType string
	TotalBytes  *int64 // optional; client may declare upfront
	Source      UploadSource
	SkipOwner   bool // true for bot uploads (BOT_API_KEY is trust anchor)
}

// InitUpload returns the active session for the meeting (creating one if none
// exists). Idempotent: repeated calls with the same caller return the same
// active session, so a client that crashed mid-upload can resume.
func (s *UploadService) InitUpload(ctx context.Context, opts InitUploadOptions) (*repository.UploadSession, error) {
	if opts.ContentType == "" {
		return nil, fmt.Errorf("init upload: content type required")
	}
	if !opts.SkipOwner {
		if err := s.meeting.VerifyMeetingOwnership(ctx, opts.UserID, opts.MeetingID); err != nil {
			return nil, ErrMeetingNotFound
		}
	}

	existing, err := s.repo.GetActiveByMeeting(ctx, opts.MeetingID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, repository.ErrUploadSessionNotFound) {
		return nil, fmt.Errorf("init upload: lookup existing session: %w", err)
	}

	session := &repository.UploadSession{
		MeetingID:   opts.MeetingID,
		UserID:      opts.UserID,
		ContentType: opts.ContentType,
		TotalBytes:  opts.TotalBytes,
		StoragePath: storage.BuildPath(opts.UserID, opts.MeetingID, opts.ContentType),
	}
	if err := s.repo.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("init upload: create session: %w", err)
	}

	// Pin audio_source + transcription_status=uploading so an interrupted
	// upload still carries provenance. If this update fails the session is
	// live but the meeting is out of sync — abort the session so the client
	// sees the failure and retries cleanly, rather than getting a phantom
	// success with a meeting stuck in the wrong state.
	if err := s.meeting.UpdateMeetingFields(ctx, opts.MeetingID, map[string]any{
		"audio_source":         string(opts.Source),
		"transcription_status": "uploading",
	}); err != nil {
		_ = s.repo.MarkAborted(ctx, session.ID)
		return nil, fmt.Errorf("init upload: pin meeting audio_source/status: %w", err)
	}

	return session, nil
}

// AppendChunkOutcome tells the caller whether a chunk was accepted, treated
// as an idempotent replay of an already-applied chunk, or rejected as a
// protocol-violating range.
type AppendChunkOutcome int

const (
	// AppendChunkApplied — bytes were written, received_bytes advanced.
	AppendChunkApplied AppendChunkOutcome = iota
	// AppendChunkIdempotent — the chunk was already fully applied; the
	// client likely retried after a lost ACK. No-op, not an error.
	AppendChunkIdempotent
	// AppendChunkConflict — the chunk's range doesn't align with the
	// session's current offset (gap or partial overlap). Client must
	// resume from NewOffset.
	AppendChunkConflict
)

// AppendChunkResult is the typed response from AppendChunk.
type AppendChunkResult struct {
	Outcome   AppendChunkOutcome
	NewOffset int64 // Current session offset after the call (for both Applied and Idempotent/Conflict paths).
}

// AppendChunk uploads one chunk of the resumable upload to staging and
// advances the session's received_bytes counter.
//
// `start` is the absolute byte offset the client claims the chunk begins
// at (from the HTTP Content-Range header). This must equal the session's
// current received_bytes, otherwise:
//
//   - [start, start+chunkSize) entirely below received_bytes →
//     Outcome=AppendChunkIdempotent (retry after lost ACK), body discarded
//   - otherwise (gap or partial overlap) →
//     Outcome=AppendChunkConflict, body discarded
//
// Without this validation, a duplicate retry would be staged as a fresh
// chunk (old handler derived seq from received_bytes only) and the final
// concatenation would contain the overlap twice — corrupt audio.
func (s *UploadService) AppendChunk(
	ctx context.Context,
	sessionID uuid.UUID,
	start int64,
	body io.Reader,
	chunkSize int64,
) (AppendChunkResult, error) {
	if start < 0 {
		return AppendChunkResult{}, fmt.Errorf("append chunk: start must be >= 0, got %d", start)
	}
	if chunkSize <= 0 {
		return AppendChunkResult{}, fmt.Errorf("append chunk: size must be > 0, got %d", chunkSize)
	}

	session, err := s.repo.GetByID(ctx, sessionID)
	if err != nil {
		return AppendChunkResult{}, err
	}
	if session.Status != "in_progress" {
		return AppendChunkResult{}, fmt.Errorf("append chunk: session %s is %s, not in_progress", sessionID, session.Status)
	}

	// Entire range already applied — idempotent retry.
	if start+chunkSize <= session.ReceivedBytes {
		// Defensive nil-guard: when the request reaches us through the
		// Container Apps envoy proxy and the upstream connection is
		// torn before the body is forwarded, fasthttp's BodyStream()
		// returns a nil reader. The previous unconditional io.Copy
		// SIGSEGV'd the entire backend process, restart-looping the
		// container under a stuck recorder retry. Body discard is only
		// useful to drain a real reader; a nil one is already drained.
		if body != nil {
			_, _ = io.Copy(io.Discard, body)
		}
		return AppendChunkResult{Outcome: AppendChunkIdempotent, NewOffset: session.ReceivedBytes}, nil
	}
	// Gap or partial overlap — protocol violation.
	if start != session.ReceivedBytes {
		if body != nil {
			_, _ = io.Copy(io.Discard, body)
		}
		return AppendChunkResult{Outcome: AppendChunkConflict, NewOffset: session.ReceivedBytes}, nil
	}

	// Compute seq from the current offset. We only reach here when start
	// == received_bytes, so seq strictly advances and staging keys are
	// unique. Zero-padded for lexical == numerical sort on List.
	seq := int(session.ReceivedBytes / chunkSize)
	chunkPath := stagingChunkPath(sessionID, seq)

	// Buffer the body before uploading: fasthttp's BodyStream() returns
	// an io.Reader without a known length, so the Go HTTP client falls
	// back to chunked transfer encoding when forwarding to Supabase
	// Storage. The meeting-audio bucket on Supabase silently writes a
	// 0-byte object on chunked-encoded uploads (returns 200 OK with no
	// payload persisted) — every chunk landed empty, the final concat
	// produced a 0-byte file, and ElevenLabs Scribe got fed silence.
	// Reading into a buffer lets net/http set Content-Length precisely.
	// Also gives us the actual received byte count, which matters because
	// AddReceivedBytes was being called with the declared chunkSize (from
	// the Content-Range header), so a truncated body bumped the counter
	// to a "completed" upload while staging stayed empty.
	var buf []byte
	if body != nil {
		readBuf, err := io.ReadAll(body)
		if err != nil {
			return AppendChunkResult{}, fmt.Errorf("append chunk: read body: %w", err)
		}
		buf = readBuf
	}
	if int64(len(buf)) != chunkSize {
		return AppendChunkResult{}, fmt.Errorf(
			"append chunk: declared %d bytes in Content-Range but read %d from body",
			chunkSize, len(buf),
		)
	}

	// Staging chunks use audio/ogg explicitly — the meeting-audio bucket's
	// Supabase Storage policy rejects application/octet-stream with
	// HTTP 415 (`invalid_mime_type`).
	if err := s.storage.Upload(ctx, chunkPath, bytes.NewReader(buf), "audio/ogg"); err != nil {
		return AppendChunkResult{}, fmt.Errorf("append chunk: upload to storage: %w", err)
	}

	offset, err := s.repo.AddReceivedBytes(ctx, sessionID, int64(len(buf)))
	if err != nil {
		// Roll back the staged chunk so a retry doesn't see a ghost.
		_ = s.storage.Delete(ctx, chunkPath)
		return AppendChunkResult{}, fmt.Errorf("append chunk: bump offset: %w", err)
	}
	return AppendChunkResult{Outcome: AppendChunkApplied, NewOffset: offset}, nil
}

// FinalizeMeta is what the client declares on upload-complete.
//
// ContentType is optional: when set it overrides the session's content type
// (and storage path) at finalization time. The desktop recorder needs this
// because session.ContentType is captured from the FIRST chunk's
// X-Audio-Content-Type header — if that header is missing the session
// defaults to audio/webm and the final object lands at `audio.webm` with
// the wrong mime, even though the bytes are Ogg/Opus. Letting the client
// declare the truth at finalize lets us correct the path before the
// transcribe job consumes it.
type FinalizeMeta struct {
	ContentType     string
	TotalBytes      int64
	DurationSeconds int
}

// FinalizeUpload concatenates staged chunks into the final audio object and
// updates the meeting's audio_* columns, leaving transcription_status=pending
// for the batch worker (Phase 3) to pick up.
//
// If any step fails after the final upload succeeds, the meeting is left in
// transcription_status=pending — the batch worker will surface the issue.
func (s *UploadService) FinalizeUpload(
	ctx context.Context,
	sessionID uuid.UUID,
	meta FinalizeMeta,
) error {
	session, err := s.repo.GetByID(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.Status != "in_progress" {
		return fmt.Errorf("finalize: session %s already %s", sessionID, session.Status)
	}

	// Phase 2 integrity gate: refuse to finalize a partial upload. The client
	// tells us how many bytes it intended to send; we compare with the
	// server-side tally built from Content-Range-validated chunks (see
	// handler's stale-chunk guard). Without this check a missing/truncated
	// last chunk can still become the canonical audio object and trigger
	// transcription on bogus data.
	if meta.TotalBytes > 0 && session.ReceivedBytes != meta.TotalBytes {
		return fmt.Errorf(
			"finalize: received %d bytes, client declared %d — refusing partial upload",
			session.ReceivedBytes, meta.TotalBytes,
		)
	}

	// Apply the client's content-type override (if any) before computing the
	// final path. See FinalizeMeta.ContentType comment.
	finalContentType := session.ContentType
	finalStoragePath := session.StoragePath
	if meta.ContentType != "" && meta.ContentType != session.ContentType {
		finalContentType = meta.ContentType
		finalStoragePath = storage.BuildPath(session.UserID, session.MeetingID, meta.ContentType)
	}

	chunks, err := s.listStagingChunks(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("finalize: list staging: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Errorf("finalize: no chunks staged for session %s", sessionID)
	}

	// Stream-concat staged chunks into the final object via io.Pipe so we
	// never hold the full audio in memory at once.
	reader := s.concatChunks(ctx, chunks)
	if err := s.storage.Upload(ctx, finalStoragePath, reader, finalContentType); err != nil {
		return fmt.Errorf("finalize: upload final object: %w", err)
	}

	if err := s.repo.MarkCompleted(ctx, sessionID); err != nil {
		return fmt.Errorf("finalize: mark completed: %w", err)
	}

	// Best-effort staging cleanup — the upload already succeeded; orphan
	// staging chunks will be swept by the cleanup cron (Phase 4).
	for _, c := range chunks {
		if err := s.storage.Delete(ctx, c.Name); err != nil {
			slog.Warn("finalize: delete staging chunk failed", "chunk", c.Name, "error", err)
		}
	}

	// Audio size: prefer the client's declared total when present, otherwise
	// fall back to the server-side received_bytes tally so the row is never
	// stamped with size=0 on a successful upload (the desktop recorder
	// hardcodes total_bytes:0 in confirm_upload because tracks are uploaded
	// independently and the client doesn't easily know the post-finalize
	// total — the server does).
	audioSizeBytes := meta.TotalBytes
	if audioSizeBytes <= 0 {
		audioSizeBytes = session.ReceivedBytes
	}

	fields := map[string]any{
		"audio_storage_path":   finalStoragePath,
		"audio_content_type":   finalContentType,
		"audio_size_bytes":     audioSizeBytes,
		"transcription_status": "pending",
	}
	if meta.DurationSeconds > 0 {
		fields["audio_duration_seconds"] = meta.DurationSeconds
	}
	if err := s.meeting.UpdateMeetingFields(ctx, session.MeetingID, fields); err != nil {
		return fmt.Errorf("finalize: update meeting: %w", err)
	}

	// Fire-and-forget: enqueue ElevenLabs batch transcription. Failure to
	// enqueue isn't fatal for the upload itself (the audio is safely in
	// storage) but is logged so ops can trigger it manually.
	s.enqueueTranscribeJob(ctx, session.MeetingID)

	return nil
}

// UploadWholeFileOptions drives the single-shot fallback endpoint used when
// the client either doesn't have chunking or is sending a small file.
type UploadWholeFileOptions struct {
	UserID          uuid.UUID
	MeetingID       uuid.UUID
	ContentType     string
	Body            io.Reader
	TotalBytes      int64
	DurationSeconds int
	Source          UploadSource
	SkipOwner       bool
}

// UploadWholeFile PUTs the audio body straight to the final path — no staging,
// no session state. Used by the bot today and as a fallback for the recorder.
func (s *UploadService) UploadWholeFile(ctx context.Context, opts UploadWholeFileOptions) error {
	if opts.ContentType == "" || opts.Body == nil {
		return fmt.Errorf("upload whole file: content type and body required")
	}
	if !opts.SkipOwner {
		if err := s.meeting.VerifyMeetingOwnership(ctx, opts.UserID, opts.MeetingID); err != nil {
			return ErrMeetingNotFound
		}
	}

	path := storage.BuildPath(opts.UserID, opts.MeetingID, opts.ContentType)
	if err := s.storage.Upload(ctx, path, opts.Body, opts.ContentType); err != nil {
		return fmt.Errorf("upload whole file: storage put: %w", err)
	}

	fields := map[string]any{
		"audio_storage_path":   path,
		"audio_content_type":   opts.ContentType,
		"audio_size_bytes":     opts.TotalBytes,
		"audio_source":         string(opts.Source),
		"transcription_status": "pending",
	}
	if opts.DurationSeconds > 0 {
		fields["audio_duration_seconds"] = opts.DurationSeconds
	}
	if err := s.meeting.UpdateMeetingFields(ctx, opts.MeetingID, fields); err != nil {
		return fmt.Errorf("upload whole file: update meeting: %w", err)
	}

	// Enqueue batch transcription as soon as the audio is in place.
	s.enqueueTranscribeJob(ctx, opts.MeetingID)
	return nil
}

// enqueueTranscribeJob is a best-effort Phase 3 hook. Failure to enqueue is
// non-fatal (the audio is safely stored) — we log so an operator can
// retrigger manually. When s.jobs is nil (tests or transcription-disabled
// deployments) this is a no-op.
func (s *UploadService) enqueueTranscribeJob(ctx context.Context, meetingID uuid.UUID) {
	if s.jobs == nil {
		return
	}
	jobID, err := s.jobs.EnqueueTranscribe(ctx, meetingID, 3)
	if err != nil {
		slog.Warn("upload: failed to enqueue transcribe job",
			"meeting_id", meetingID, "error", err)
		return
	}
	slog.Info("upload: transcribe job enqueued",
		"meeting_id", meetingID, "job_id", jobID)
}

// CreateSignedURL issues a short-lived download URL for the meeting's audio.
// Used by deep-link notifications (desktop recorder post-meeting flow) and by
// the transcription worker when submitting to ElevenLabs (Phase 3).
func (s *UploadService) CreateSignedURL(
	ctx context.Context,
	userID uuid.UUID,
	meetingID uuid.UUID,
	expiresIn int,
	skipOwner bool,
) (storage.SignedURL, error) {
	if !skipOwner {
		if err := s.meeting.VerifyMeetingOwnership(ctx, userID, meetingID); err != nil {
			return storage.SignedURL{}, ErrMeetingNotFound
		}
	}
	// We look up the path from the meeting row rather than recomputing it —
	// the stored path is the source of truth (handles future path changes).
	meeting, err := s.meeting.GetMeetingByIDSystem(ctx, meetingID)
	if err != nil {
		return storage.SignedURL{}, fmt.Errorf("signed url: load meeting: %w", err)
	}
	if meeting.AudioStoragePath == nil || *meeting.AudioStoragePath == "" {
		return storage.SignedURL{}, fmt.Errorf("signed url: meeting has no audio yet")
	}
	if expiresIn <= 0 {
		expiresIn = 900 // 15 minutes default
	}
	return s.storage.CreateSignedURL(ctx, *meeting.AudioStoragePath, expiresIn)
}

// --- internals ---

// stagingChunkPath returns the storage key for a single chunk.
// Zero-padded seq keeps lexical sort == numerical sort so List() returns
// chunks in the order they were uploaded.
func stagingChunkPath(sessionID uuid.UUID, seq int) string {
	return fmt.Sprintf("_staging/%s/%08d", sessionID.String(), seq)
}

func (s *UploadService) listStagingChunks(ctx context.Context, sessionID uuid.UUID) ([]storage.ListObject, error) {
	prefix := fmt.Sprintf("_staging/%s/", sessionID.String())
	objs, err := s.storage.List(ctx, prefix, 1000)
	if err != nil {
		return nil, err
	}
	// Supabase's list returns names relative to the prefix; prepend prefix back
	// so the returned keys are usable directly with Download/Delete.
	for i := range objs {
		if !strings.HasPrefix(objs[i].Name, prefix) {
			objs[i].Name = prefix + objs[i].Name
		}
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return objs, nil
}

// concatChunks returns a Reader that streams the concatenation of `chunks`
// sequentially. On any error the reader returns a pipe-propagated error to
// the upload goroutine, which then fails the PUT request.
func (s *UploadService) concatChunks(ctx context.Context, chunks []storage.ListObject) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for _, c := range chunks {
			rc, err := s.storage.Download(ctx, c.Name)
			if err != nil {
				_ = pw.CloseWithError(fmt.Errorf("concat chunks: download %s: %w", c.Name, err))
				return
			}
			if _, err := io.Copy(pw, rc); err != nil {
				rc.Close()
				_ = pw.CloseWithError(fmt.Errorf("concat chunks: copy %s: %w", c.Name, err))
				return
			}
			rc.Close()
		}
	}()
	return pr
}

// UploadContextKey is the context key under which handlers can stash an
// explicit deadline for large uploads. Used only for unit tests today.
type UploadContextKey string

const UploadDeadlineKey UploadContextKey = "upload_deadline"

// WithUploadDeadline returns ctx with a derived deadline used by long uploads.
// Exposed for tests; production code uses request-scoped context directly.
func WithUploadDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}
