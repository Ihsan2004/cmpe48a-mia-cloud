package services

import (
	"testing"

	"github.com/google/uuid"
)

func words(items ...any) []ElevenLabsWord {
	// Tiny helper: items come in groups of (text, start, end).
	// Keeps tests readable.
	if len(items)%3 != 0 {
		panic("words helper wants groups of 3")
	}
	out := make([]ElevenLabsWord, 0, len(items)/3)
	for i := 0; i < len(items); i += 3 {
		out = append(out, ElevenLabsWord{
			Text:  items[i].(string),
			Start: items[i+1].(float64),
			End:   items[i+2].(float64),
			Type:  "word",
		})
	}
	return out
}

func TestMapWordsToSegments_Empty(t *testing.T) {
	if got := MapWordsToSegments(uuid.New(), nil); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
	if got := MapWordsToSegments(uuid.New(), []ElevenLabsWord{}); got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}

func TestMapWordsToSegments_SingleSegmentNoGaps(t *testing.T) {
	meetingID := uuid.New()
	ws := words(
		"hello", 0.0, 0.5,
		"world", 0.6, 1.0,
		"foo", 1.1, 1.5,
	)
	got := MapWordsToSegments(meetingID, ws)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	seg := got[0]
	if seg.Text != "hello world foo" {
		t.Errorf("text = %q", seg.Text)
	}
	if seg.StartTime != 0.0 || seg.EndTime != 1.5 {
		t.Errorf("times = %v-%v", seg.StartTime, seg.EndTime)
	}
	if seg.MeetingID != meetingID {
		t.Errorf("meeting id mismatch")
	}
	if seg.Speaker != "" {
		t.Errorf("speaker should be empty (diarize off), got %q", seg.Speaker)
	}
}

func TestMapWordsToSegments_GapSplitsSegments(t *testing.T) {
	meetingID := uuid.New()
	// 1.5s gap between "world" and "foo" → two segments (threshold is 1.0s).
	ws := words(
		"hello", 0.0, 0.5,
		"world", 0.6, 1.0,
		"foo", 2.6, 3.0,
		"bar", 3.1, 3.5,
	)
	got := MapWordsToSegments(meetingID, ws)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Text != "hello world" {
		t.Errorf("seg0 text = %q", got[0].Text)
	}
	if got[1].Text != "foo bar" {
		t.Errorf("seg1 text = %q", got[1].Text)
	}
	if got[1].StartTime != 2.6 {
		t.Errorf("seg1 start = %v", got[1].StartTime)
	}
}

func TestMapWordsToSegments_ExactThresholdStaysInSameSegment(t *testing.T) {
	// Gap of exactly 1.0s — not > 1.0, so words should stay together.
	ws := words(
		"hello", 0.0, 0.5,
		"world", 1.5, 2.0,
	)
	got := MapWordsToSegments(uuid.New(), ws)
	if len(got) != 1 {
		t.Errorf("exactly-threshold gap should keep one segment, got %d", len(got))
	}
}

func TestMapWordsToSegments_SkipsNonWordTypes(t *testing.T) {
	meetingID := uuid.New()
	ws := []ElevenLabsWord{
		{Text: "hello", Start: 0.0, End: 0.5, Type: "word"},
		{Text: "[laughter]", Start: 0.6, End: 1.0, Type: "audio_event"},
		{Text: "world", Start: 1.1, End: 1.5, Type: "word"},
		{Text: " ", Start: 1.6, End: 1.7, Type: "spacing"},
	}
	got := MapWordsToSegments(meetingID, ws)
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Text != "hello world" {
		t.Errorf("non-word types should be skipped, got text %q", got[0].Text)
	}
}

func TestMapWordsToSegments_SkipsEmptyWords(t *testing.T) {
	ws := []ElevenLabsWord{
		{Text: "", Start: 0.0, End: 0.5, Type: "word"},
		{Text: "   ", Start: 0.6, End: 1.0, Type: "word"},
		{Text: "hello", Start: 1.1, End: 1.5, Type: "word"},
	}
	got := MapWordsToSegments(uuid.New(), ws)
	if len(got) != 1 || got[0].Text != "hello" {
		t.Errorf("empty/whitespace words should be skipped, got %+v", got)
	}
}

func TestMapWordsToSegments_UntypedWordsTreatedAsWords(t *testing.T) {
	// ElevenLabs sometimes omits type for plain words.
	ws := []ElevenLabsWord{
		{Text: "hello", Start: 0.0, End: 0.5},
		{Text: "world", Start: 0.6, End: 1.0},
	}
	got := MapWordsToSegments(uuid.New(), ws)
	if len(got) != 1 || got[0].Text != "hello world" {
		t.Errorf("untyped words should be included, got %+v", got)
	}
}
