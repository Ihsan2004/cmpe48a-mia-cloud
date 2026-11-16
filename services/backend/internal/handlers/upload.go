package handlers

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"mia-website/backend/internal/pkg/metrics"
	"mia-website/backend/internal/services"
	"mia-website/backend/internal/storage"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// UploadHandler exposes the resumable audio upload endpoints used by the
// desktop recorder (JWT auth) and the meeting bot (X-Bot-Api-Key auth).
type UploadHandler struct {
	svc *services.UploadService
}

// NewUploadHandler wires the default dependencies. Returns nil if storage
// isn't configured — routes.go guards against registering the endpoints in
// that case so requests get a clear 503 rather than a nil panic.
func NewUploadHandler(store *storage.Client) *UploadHandler {
	if store == nil {
		return nil
	}
	return &UploadHandler{svc: services.NewUploadService(store)}
}

// ---------- user-facing (JWT) ----------

// UploadManifest accepts the recorder's manifest JSON and, as a side-effect,
// initializes the upload session so later chunks have somewhere to land.
func (h *UploadHandler) UploadManifest(c *fiber.Ctx) error {
	userID, ok := parseAuthUser(c)
	if !ok {
		return unauthorized(c)
	}
	meetingID, ok := parseMeetingID(c)
	if !ok {
		return badRequest(c, "invalid meeting id")
	}
	// Content type defaults to audio/webm if the recorder doesn't tell us —
	// the real content type is finalized on upload-complete from the binary
	// chunk MIME type.
	contentType := c.Get("X-Audio-Content-Type", "audio/webm")

	sess, err := h.svc.InitUpload(c.Context(), services.InitUploadOptions{
		UserID:      userID,
		MeetingID:   meetingID,
		ContentType: contentType,
		Source:      services.UploadSourceDesktop,
	})
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(fiber.Map{
		"upload_session_id": sess.ID,
		"storage_path":      sess.StoragePath,
		"received_bytes":    sess.ReceivedBytes,
	})
}

// UploadAudioChunk receives a single chunk of a resumable upload.
// Expected: Content-Range: bytes <start>-<end>/<total|*> plus a binary body.
// Returns 308 when more chunks are expected, 200 when the upload is complete.
func (h *UploadHandler) UploadAudioChunk(c *fiber.Ctx) error {
	userID, ok := parseAuthUser(c)
	if !ok {
		return unauthorized(c)
	}
	return h.handleChunk(c, userID, services.UploadSourceDesktop, false)
}

// UploadAudio is the single-shot fallback for clients that can't stream
// chunks. Multipart form field "audio" carries the whole file.
func (h *UploadHandler) UploadAudio(c *fiber.Ctx) error {
	userID, ok := parseAuthUser(c)
	if !ok {
		return unauthorized(c)
	}
	return h.handleWholeFile(c, userID, services.UploadSourceDesktop, false)
}

// UploadComplete marks the upload finished, concats staged chunks into the
// final audio object, and flips the meeting into transcription_status=pending.
func (h *UploadHandler) UploadComplete(c *fiber.Ctx) error {
	userID, ok := parseAuthUser(c)
	if !ok {
		return unauthorized(c)
	}
	return h.handleComplete(c, userID, false)
}

// SignedURL returns a short-lived HTTPS URL to the meeting's audio object.
// Used by post-meeting deep-link notifications and (in Phase 3) by the
// backend itself when submitting to ElevenLabs.
func (h *UploadHandler) SignedURL(c *fiber.Ctx) error {
	userID, ok := parseAuthUser(c)
	if !ok {
		return unauthorized(c)
	}
	meetingID, ok := parseMeetingID(c)
	if !ok {
		return badRequest(c, "invalid meeting id")
	}

	signed, err := h.svc.CreateSignedURL(c.Context(), userID, meetingID, 900, false)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(fiber.Map{
		"url":        signed.URL,
		"expires_at": signed.ExpiresAt,
	})
}

// ---------- bot-facing (X-Bot-Api-Key) ----------

// BotUploadAudioChunk mirrors UploadAudioChunk but skips ownership checks;
// the BOT_API_KEY is the trust anchor.
func (h *UploadHandler) BotUploadAudioChunk(c *fiber.Ctx) error {
	return h.handleChunk(c, uuid.Nil, services.UploadSourceBot, true)
}

// BotUploadComplete mirrors UploadComplete for the bot.
func (h *UploadHandler) BotUploadComplete(c *fiber.Ctx) error {
	return h.handleComplete(c, uuid.Nil, true)
}

// ---------- shared implementation ----------

func (h *UploadHandler) handleChunk(c *fiber.Ctx, userID uuid.UUID, source services.UploadSource, isBot bool) error {
	meetingID, ok := parseMeetingID(c)
	if !ok {
		return badRequest(c, "invalid meeting id")
	}
	cr, err := parseContentRange(c.Get("Content-Range"))
	if err != nil {
		return badRequest(c, "invalid Content-Range header: "+err.Error())
	}

	// For bot uploads the caller doesn't know its user_id, so look it up
	// from the meeting before building the storage path.
	effectiveUserID := userID
	if isBot {
		// The service's InitUpload (SkipOwner=true) will accept any user_id
		// we pass; we need the real one for the storage path though.
		uid, err := lookupMeetingOwner(c, meetingID)
		if err != nil {
			return mapServiceError(c, err)
		}
		effectiveUserID = uid
	}

	contentType := c.Get("X-Audio-Content-Type", "audio/webm")

	session, err := h.svc.InitUpload(c.Context(), services.InitUploadOptions{
		UserID:      effectiveUserID,
		MeetingID:   meetingID,
		ContentType: contentType,
		TotalBytes:  cr.Total,
		Source:      source,
		SkipOwner:   isBot,
	})
	if err != nil {
		return mapServiceError(c, err)
	}

	chunkSize := cr.End - cr.Start + 1
	_ = session // session is validated + used internally by AppendChunk

	// fasthttp pre-buffers the request body in memory; `c.Body()` returns
	// the canonical bytes regardless of streaming/non-streaming origin.
	// `c.Request().BodyStream()` was returning an empty reader for desktop
	// recorder uploads (~470KB Vec<u8> bodies) — fasthttp's BodyStream
	// implementation only returns a reader when body streaming was
	// explicitly enabled on the server, otherwise it gives an empty
	// reader even though Body() has the bytes. Wrap Body() in a Reader
	// so AppendChunk's io.Reader interface is unchanged.
	bodyBytes := c.Body()
	result, err := h.svc.AppendChunk(c.Context(), session.ID, cr.Start, bytes.NewReader(bodyBytes), chunkSize)
	if err != nil {
		return mapServiceError(c, err)
	}

	// Service-level outcome drives the response. See upload_service.go for
	// the full contract: Applied (bytes written), Idempotent (already-applied
	// retry after a lost ACK), or Conflict (partial overlap or gap — client
	// must resume from NewOffset).
	switch result.Outcome {
	case services.AppendChunkConflict:
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":       "content range does not align with session offset",
			"received":    result.NewOffset,
			"next_offset": result.NewOffset,
		})
	case services.AppendChunkIdempotent:
		c.Set("Range", "bytes=0-"+strconv.FormatInt(result.NewOffset-1, 10))
		return c.Status(308).JSON(fiber.Map{
			"received":    result.NewOffset,
			"next_offset": result.NewOffset,
		})
	}

	// Applied — count for observability.
	metrics.AudioUploadBytes.Add(float64(chunkSize))

	// More to come?
	if cr.Total != nil && result.NewOffset < *cr.Total {
		c.Set("Range", "bytes=0-"+strconv.FormatInt(result.NewOffset-1, 10))
		return c.Status(308).JSON(fiber.Map{
			"received":    result.NewOffset,
			"next_offset": result.NewOffset,
		})
	}
	return c.JSON(fiber.Map{
		"received":    result.NewOffset,
		"next_offset": result.NewOffset,
		"complete":    true,
	})
}

func (h *UploadHandler) handleWholeFile(c *fiber.Ctx, userID uuid.UUID, source services.UploadSource, isBot bool) error {
	meetingID, ok := parseMeetingID(c)
	if !ok {
		return badRequest(c, "invalid meeting id")
	}
	file, err := c.FormFile("audio")
	if err != nil {
		return badRequest(c, "missing multipart field 'audio'")
	}
	f, err := file.Open()
	if err != nil {
		return internalError(c, "could not open upload body", err)
	}
	defer f.Close()

	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/webm"
	}

	effectiveUserID := userID
	if isBot {
		uid, err := lookupMeetingOwner(c, meetingID)
		if err != nil {
			return mapServiceError(c, err)
		}
		effectiveUserID = uid
	}

	duration, _ := strconv.Atoi(c.FormValue("duration_seconds"))

	if err := h.svc.UploadWholeFile(c.Context(), services.UploadWholeFileOptions{
		UserID:          effectiveUserID,
		MeetingID:       meetingID,
		ContentType:     contentType,
		Body:            f,
		TotalBytes:      file.Size,
		DurationSeconds: duration,
		Source:          source,
		SkipOwner:       isBot,
	}); err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(fiber.Map{"status": "uploaded", "size": file.Size})
}

func (h *UploadHandler) handleComplete(c *fiber.Ctx, userID uuid.UUID, isBot bool) error {
	meetingID, ok := parseMeetingID(c)
	if !ok {
		return badRequest(c, "invalid meeting id")
	}
	var body struct {
		ContentType     string `json:"content_type"`
		TotalBytes      int64  `json:"total_bytes"`
		DurationSeconds int    `json:"duration_seconds"`
	}
	if err := c.BodyParser(&body); err != nil {
		return badRequest(c, "invalid json body")
	}

	// Ownership: user path verified via JWT → meeting lookup; bot path
	// trusts BOT_API_KEY so the middleware is enough.
	if !isBot {
		if err := verifyOwnership(c, userID, meetingID); err != nil {
			return mapServiceError(c, err)
		}
	}

	session, err := getActiveSession(c, meetingID)
	if err != nil {
		return mapServiceError(c, err)
	}
	if err := h.svc.FinalizeUpload(c.Context(), session.ID, services.FinalizeMeta{
		ContentType:     body.ContentType,
		TotalBytes:      body.TotalBytes,
		DurationSeconds: body.DurationSeconds,
	}); err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(fiber.Map{"status": "complete", "session_id": session.ID})
}

// ---------- helpers ----------

// contentRange captures a parsed Content-Range header.
type contentRange struct {
	Start int64
	End   int64
	Total *int64 // nil when client sent "*"
}

var contentRangeRE = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)

func parseContentRange(h string) (*contentRange, error) {
	if h == "" {
		return nil, fmt.Errorf("missing")
	}
	m := contentRangeRE.FindStringSubmatch(h)
	if m == nil {
		return nil, fmt.Errorf("format must be 'bytes <start>-<end>/<total|*>'")
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	end, _ := strconv.ParseInt(m[2], 10, 64)
	if end < start {
		return nil, fmt.Errorf("end < start")
	}
	cr := &contentRange{Start: start, End: end}
	if m[3] != "*" {
		total, _ := strconv.ParseInt(m[3], 10, 64)
		cr.Total = &total
	}
	return cr, nil
}

func parseAuthUser(c *fiber.Ctx) (uuid.UUID, bool) {
	s, ok := c.Locals("userID").(string)
	if !ok || s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func parseMeetingID(c *fiber.Ctx) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func unauthorized(c *fiber.Ctx) error {
	return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
}

func badRequest(c *fiber.Ctx, msg string) error {
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": msg})
}

func internalError(c *fiber.Ctx, msg string, err error) error {
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
		"error":  msg,
		"detail": err.Error(),
	})
}

func mapServiceError(c *fiber.Ctx, err error) error {
	if err == nil {
		return nil
	}
	if errIsMeetingNotFound(err) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "meeting not found"})
	}
	return internalError(c, "upload failed", err)
}

func errIsMeetingNotFound(err error) bool {
	return err != nil && err.Error() == "meeting not found"
}

// Split helpers: lookupMeetingOwner / verifyOwnership / getActiveSession are
// thin adapters that let us avoid depending on concrete repositories from the
// handler (they'll be swapped for mocks in tests).
var (
	lookupMeetingOwner = defaultLookupMeetingOwner
	verifyOwnership    = defaultVerifyOwnership
	getActiveSession   = defaultGetActiveSession
)

// ensure the package uses the io import even if helpers change later
var _ = io.EOF

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
