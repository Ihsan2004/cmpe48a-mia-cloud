package services

import (
	"strings"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ElevenLabsWord is one word-level timestamped unit from the Scribe v2
// transcript payload. Fields mirror the JSON we expect in the webhook.
type ElevenLabsWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Type  string  `json:"type,omitempty"` // "word" | "audio_event" | "spacing"
}

// SegmentGap is how much silence (seconds) between two words triggers a
// segment break. 1.0 is a natural conversational-pause threshold and
// matches the roadmap (Phase 3.4 rule).
const SegmentGap = 1.0

// MapWordsToSegments groups word-level ElevenLabs output into coarser
// segments compatible with the existing transcript_segments schema.
//
//   - Words with type != "word" are ignored (audio events, spacing).
//   - A gap > SegmentGap between consecutive words starts a new segment.
//   - Speaker is always empty — speaker detection is disabled (roadmap #4).
//   - Empty/whitespace words are skipped so the output text is clean.
//
// The returned slice is safe to pass directly to InsertSegments.
func MapWordsToSegments(meetingID uuid.UUID, words []ElevenLabsWord) []models.TranscriptSegment {
	if len(words) == 0 {
		return nil
	}

	var out []models.TranscriptSegment
	var (
		curText  strings.Builder
		curStart float64
		curEnd   float64
		hasCur   bool
	)

	flush := func() {
		if !hasCur {
			return
		}
		text := strings.TrimSpace(curText.String())
		if text == "" {
			hasCur = false
			curText.Reset()
			return
		}
		out = append(out, models.TranscriptSegment{
			ID:        uuid.New(),
			MeetingID: meetingID,
			StartTime: curStart,
			EndTime:   curEnd,
			Text:      text,
		})
		curText.Reset()
		hasCur = false
	}

	for _, w := range words {
		if w.Type != "" && w.Type != "word" {
			continue // skip audio_event / spacing / other markers
		}
		wordText := strings.TrimSpace(w.Text)
		if wordText == "" {
			continue
		}

		if !hasCur {
			curStart = w.Start
			curEnd = w.End
			curText.WriteString(wordText)
			hasCur = true
			continue
		}

		if w.Start-curEnd > SegmentGap {
			flush()
			curStart = w.Start
			curEnd = w.End
			curText.WriteString(wordText)
			hasCur = true
			continue
		}

		curText.WriteByte(' ')
		curText.WriteString(wordText)
		curEnd = w.End
	}
	flush()
	return out
}
