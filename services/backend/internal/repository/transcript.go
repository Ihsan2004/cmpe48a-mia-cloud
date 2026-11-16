package repository

import (
	"context"
	"fmt"
	"mia-website/backend/internal/models"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DeltaTranscriptRow represents a speaker/text pair from transcript segments
type DeltaTranscriptRow struct {
	Speaker string
	Text    string
}

type TranscriptRepository interface {
	GetSegmentsByMeetingID(ctx context.Context, meetingID uuid.UUID) ([]models.TranscriptSegment, error)
	GetSegmentsByMeetingIDFromTime(ctx context.Context, meetingID uuid.UUID, startTime float64) ([]models.TranscriptSegment, error)
	InsertSegments(ctx context.Context, meetingID uuid.UUID, segs []models.TranscriptSegment) error
	DeleteSegmentsByMeetingID(ctx context.Context, meetingID uuid.UUID) error
	CreateChatBlocks(ctx context.Context, blocks []models.TranscriptChatBlock) error
	DeleteChatBlocksByMeetingID(ctx context.Context, meetingID uuid.UUID) error
	DeleteChatBlocksFromTime(ctx context.Context, meetingID uuid.UUID, startTime float64) error
	MarkChatBlocksFinalized(ctx context.Context, meetingID uuid.UUID, finalizedAt time.Time, sourceSegmentCount int) error
	GetChatBlocksByMeetingID(ctx context.Context, meetingID uuid.UUID) ([]models.TranscriptChatBlock, error)
	GetMeetingMetadata(ctx context.Context, meetingID uuid.UUID) (title string, participants []string, err error)
	GetChatBlocksWithoutEmbedding(ctx context.Context, meetingID uuid.UUID) ([]models.TranscriptChatBlock, error)
	GetChatBlocksWithoutEmbeddingBeforeTime(ctx context.Context, meetingID uuid.UUID, beforeTime float64) ([]models.TranscriptChatBlock, error)
	UpdateChatBlockEmbedding(ctx context.Context, blockID uuid.UUID, embedding []float64) error
	GetDeltaTranscripts(ctx context.Context, meetingID uuid.UUID, afterTime float64) ([]DeltaTranscriptRow, error)
}

type transcriptRepository struct{}

func NewTranscriptRepository() TranscriptRepository {
	return &transcriptRepository{}
}

func (r *transcriptRepository) GetSegmentsByMeetingID(ctx context.Context, meetingID uuid.UUID) ([]models.TranscriptSegment, error) {
	query := `
		SELECT id, meeting_id, start_time, end_time, COALESCE(speaker, ''), COALESCE(text, ''), created_at
		FROM transcript_segments
		WHERE meeting_id = $1
		ORDER BY start_time ASC
	`
	rows, err := DB.Query(ctx, query, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to query transcript segments: %w", err)
	}
	defer rows.Close()

	var segments []models.TranscriptSegment
	for rows.Next() {
		var s models.TranscriptSegment
		if err := rows.Scan(
			&s.ID, &s.MeetingID, &s.StartTime, &s.EndTime, &s.Speaker, &s.Text, &s.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan transcript segment: %w", err)
		}
		segments = append(segments, s)
	}
	return segments, nil
}

// InsertSegments writes a batch of transcript segments to the database.
// Used by the ElevenLabs webhook handler (Phase 3 batch pipeline). Existing
// segments for the meeting are NOT deleted here — the caller is expected to
// invoke DeleteSegmentsByMeetingID first if a clean replace is desired.
func (r *transcriptRepository) InsertSegments(ctx context.Context, meetingID uuid.UUID, segs []models.TranscriptSegment) error {
	if len(segs) == 0 {
		return nil
	}
	// pgx doesn't ship a CopyFrom helper on this pool config, so build a
	// single parameterised multi-row INSERT. 1000 segments in a 1h meeting
	// is well under any realistic parameter-count limit.
	var sb strings.Builder
	sb.WriteString(`INSERT INTO transcript_segments (id, meeting_id, start_time, end_time, speaker, text, created_at) VALUES `)
	args := make([]any, 0, len(segs)*6)
	for i, s := range segs {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * 6
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, NOW())",
			base+1, base+2, base+3, base+4, base+5, base+6)
		id := s.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		args = append(args, id, meetingID, s.StartTime, s.EndTime, s.Speaker, s.Text)
	}
	if _, err := DB.Exec(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert transcript segments: %w", err)
	}
	return nil
}

// DeleteSegmentsByMeetingID wipes a meeting's segments before re-inserting.
// Used when a transcription is retried so we don't stack duplicates.
func (r *transcriptRepository) DeleteSegmentsByMeetingID(ctx context.Context, meetingID uuid.UUID) error {
	if _, err := DB.Exec(ctx, `DELETE FROM transcript_segments WHERE meeting_id = $1`, meetingID); err != nil {
		return fmt.Errorf("delete transcript segments: %w", err)
	}
	return nil
}

func (r *transcriptRepository) GetSegmentsByMeetingIDFromTime(ctx context.Context, meetingID uuid.UUID, startTime float64) ([]models.TranscriptSegment, error) {
	query := `
		SELECT id, meeting_id, start_time, end_time, COALESCE(speaker, ''), COALESCE(text, ''), created_at
		FROM transcript_segments
		WHERE meeting_id = $1
		  AND start_time >= $2
		ORDER BY start_time ASC
	`
	rows, err := DB.Query(ctx, query, meetingID, startTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query transcript segments from time: %w", err)
	}
	defer rows.Close()

	var segments []models.TranscriptSegment
	for rows.Next() {
		var s models.TranscriptSegment
		if err := rows.Scan(
			&s.ID, &s.MeetingID, &s.StartTime, &s.EndTime, &s.Speaker, &s.Text, &s.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan transcript segment: %w", err)
		}
		segments = append(segments, s)
	}
	return segments, nil
}

func (r *transcriptRepository) CreateChatBlocks(ctx context.Context, blocks []models.TranscriptChatBlock) error {
	if len(blocks) == 0 {
		return nil
	}

	query := `
		INSERT INTO transcript_chat_blocks (
			id, meeting_id, speaker, text, start_time, end_time, raw_segment_ids,
			is_noise, is_finalized, finalized_at, source_segment_count
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`

	tx, err := DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Using standard Exec in loop for simplicity and correctness
	for _, b := range blocks {
		_, err := tx.Exec(ctx, query,
			b.ID, b.MeetingID, b.Speaker, b.Text, b.StartTime, b.EndTime, b.RawSegmentIDs,
			b.IsNoise,
			b.IsFinalized, b.FinalizedAt, b.SourceSegmentCount,
		)
		if err != nil {
			return fmt.Errorf("failed to insert chat block: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (r *transcriptRepository) DeleteChatBlocksByMeetingID(ctx context.Context, meetingID uuid.UUID) error {
	query := `DELETE FROM transcript_chat_blocks WHERE meeting_id = $1`
	_, err := DB.Exec(ctx, query, meetingID)
	if err != nil {
		return fmt.Errorf("failed to delete existing chat blocks: %w", err)
	}
	return nil
}

func (r *transcriptRepository) DeleteChatBlocksFromTime(ctx context.Context, meetingID uuid.UUID, startTime float64) error {
	query := `
		DELETE FROM transcript_chat_blocks
		WHERE meeting_id = $1
		  AND start_time >= $2
	`
	_, err := DB.Exec(ctx, query, meetingID, startTime)
	if err != nil {
		return fmt.Errorf("failed to delete chat blocks from time: %w", err)
	}
	return nil
}

func (r *transcriptRepository) MarkChatBlocksFinalized(ctx context.Context, meetingID uuid.UUID, finalizedAt time.Time, sourceSegmentCount int) error {
	query := `
		UPDATE transcript_chat_blocks
		SET is_finalized = TRUE,
		    finalized_at = $2,
		    source_segment_count = CASE
		        WHEN source_segment_count IS NULL THEN $3
		        ELSE source_segment_count
		    END
		WHERE meeting_id = $1
	`
	_, err := DB.Exec(ctx, query, meetingID, finalizedAt, sourceSegmentCount)
	if err != nil {
		return fmt.Errorf("failed to mark chat blocks finalized: %w", err)
	}
	return nil
}

func (r *transcriptRepository) GetChatBlocksByMeetingID(ctx context.Context, meetingID uuid.UUID) ([]models.TranscriptChatBlock, error) {
	query := `
		SELECT
			id, meeting_id, speaker, text, start_time, end_time, raw_segment_ids,
			is_noise, is_finalized, finalized_at, source_segment_count, created_at
		FROM transcript_chat_blocks
		WHERE meeting_id = $1
		ORDER BY start_time ASC
	`
	rows, err := DB.Query(ctx, query, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to query chat blocks: %w", err)
	}
	defer rows.Close()

	var blocks []models.TranscriptChatBlock
	for rows.Next() {
		var b models.TranscriptChatBlock
		if err := rows.Scan(
			&b.ID, &b.MeetingID, &b.Speaker, &b.Text, &b.StartTime, &b.EndTime, &b.RawSegmentIDs,
			&b.IsNoise, &b.IsFinalized, &b.FinalizedAt, &b.SourceSegmentCount, &b.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan chat block: %w", err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

func (r *transcriptRepository) GetMeetingMetadata(ctx context.Context, meetingID uuid.UUID) (string, []string, error) {
	// Query meeting title
	var title string
	titleQuery := `SELECT COALESCE(title, '') FROM meetings WHERE id = $1`
	err := DB.QueryRow(ctx, titleQuery, meetingID).Scan(&title)
	if err != nil {
		return "", nil, fmt.Errorf("failed to fetch meeting title: %w", err)
	}

	// Query participants (extract unique speakers from transcript_segments)
	participantsQuery := `
		SELECT DISTINCT speaker
		FROM transcript_segments
		WHERE meeting_id = $1 AND speaker != '' AND speaker IS NOT NULL
		ORDER BY speaker
	`
	rows, err := DB.Query(ctx, participantsQuery, meetingID)
	if err != nil {
		return title, nil, fmt.Errorf("failed to query participants: %w", err)
	}
	defer rows.Close()

	var participants []string
	for rows.Next() {
		var speaker string
		if err := rows.Scan(&speaker); err != nil {
			return title, nil, fmt.Errorf("failed to scan participant: %w", err)
		}
		participants = append(participants, speaker)
	}

	return title, participants, nil
}

func (r *transcriptRepository) GetChatBlocksWithoutEmbedding(ctx context.Context, meetingID uuid.UUID) ([]models.TranscriptChatBlock, error) {
	query := `
		SELECT
			id, meeting_id, speaker, text, start_time, end_time, raw_segment_ids,
			is_noise, is_finalized, finalized_at, source_segment_count, created_at
		FROM transcript_chat_blocks
		WHERE meeting_id = $1 AND embedding IS NULL
		ORDER BY start_time ASC
	`
	rows, err := DB.Query(ctx, query, meetingID)
	if err != nil {
		return nil, fmt.Errorf("failed to query chat blocks without embedding: %w", err)
	}
	defer rows.Close()

	var blocks []models.TranscriptChatBlock
	for rows.Next() {
		var b models.TranscriptChatBlock
		if err := rows.Scan(
			&b.ID, &b.MeetingID, &b.Speaker, &b.Text, &b.StartTime, &b.EndTime, &b.RawSegmentIDs,
			&b.IsNoise, &b.IsFinalized, &b.FinalizedAt, &b.SourceSegmentCount, &b.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan chat block: %w", err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

func (r *transcriptRepository) GetChatBlocksWithoutEmbeddingBeforeTime(ctx context.Context, meetingID uuid.UUID, beforeTime float64) ([]models.TranscriptChatBlock, error) {
	query := `
		SELECT
			id, meeting_id, speaker, text, start_time, end_time, raw_segment_ids,
			is_noise, is_finalized, finalized_at, source_segment_count, created_at
		FROM transcript_chat_blocks
		WHERE meeting_id = $1 AND embedding IS NULL AND start_time < $2
		ORDER BY start_time ASC
	`
	rows, err := DB.Query(ctx, query, meetingID, beforeTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query stable chat blocks without embedding: %w", err)
	}
	defer rows.Close()

	var blocks []models.TranscriptChatBlock
	for rows.Next() {
		var b models.TranscriptChatBlock
		if err := rows.Scan(
			&b.ID, &b.MeetingID, &b.Speaker, &b.Text, &b.StartTime, &b.EndTime, &b.RawSegmentIDs,
			&b.IsNoise, &b.IsFinalized, &b.FinalizedAt, &b.SourceSegmentCount, &b.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan chat block: %w", err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

func (r *transcriptRepository) UpdateChatBlockEmbedding(ctx context.Context, blockID uuid.UUID, embedding []float64) error {
	// pgvector expects format: [0.1,0.2,0.3,...] but pgx serializes []float64 as {0.1,0.2,0.3,...}
	// Convert to pgvector-compatible string
	parts := make([]string, len(embedding))
	for i, v := range embedding {
		parts[i] = fmt.Sprintf("%g", v)
	}
	vectorStr := "[" + strings.Join(parts, ",") + "]"

	query := `
		UPDATE transcript_chat_blocks
		SET embedding = $1::vector,
		    embedding_model = $2,
		    embedding_generated_at = NOW()
		WHERE id = $3
	`
	_, err := DB.Exec(ctx, query, vectorStr, "text-embedding-3-small", blockID)
	if err != nil {
		return fmt.Errorf("failed to update chat block embedding: %w", err)
	}
	return nil
}

func (r *transcriptRepository) GetDeltaTranscripts(ctx context.Context, meetingID uuid.UUID, afterTime float64) ([]DeltaTranscriptRow, error) {
	query := `
		SELECT speaker, text
		FROM transcript_segments
		WHERE meeting_id = $1 AND start_time > $2
		ORDER BY start_time ASC
	`
	rows, err := DB.Query(ctx, query, meetingID, afterTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeltaTranscriptRow
	for rows.Next() {
		var speaker, text *string
		if err := rows.Scan(&speaker, &text); err != nil {
			continue
		}
		spk := "Unknown"
		if speaker != nil && *speaker != "" {
			spk = *speaker
		}
		txt := ""
		if text != nil {
			txt = *text
		}
		if txt != "" {
			results = append(results, DeltaTranscriptRow{Speaker: spk, Text: txt})
		}
	}
	return results, nil
}
