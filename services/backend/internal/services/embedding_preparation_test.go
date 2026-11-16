package services

import (
	"strings"
	"testing"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

func makeBlock(speaker, text string) models.TranscriptChatBlock {
	return models.TranscriptChatBlock{
		ID:      uuid.New(),
		Speaker: speaker,
		Text:    text,
	}
}

func TestPrepareEmbeddingTexts_ContextOverlap(t *testing.T) {
	blocks := []models.TranscriptChatBlock{
		makeBlock("Ahmet", "Bugün bütçe konusunu ele alacağız ve Q2 hedeflerini belirleyeceğiz"),
		makeBlock("Mehmet", "Onu yarın yapalım"),
	}

	texts := prepareEmbeddingTexts(blocks)

	if len(texts) != 2 {
		t.Fatalf("Expected 2 texts, got %d", len(texts))
	}

	// First block: speaker prefix + own text, no overlap
	if !strings.HasPrefix(texts[0], "[Ahmet]: ") {
		t.Errorf("First block should have speaker prefix.\nGot: %q", texts[0])
	}
	if !strings.Contains(texts[0], "bütçe konusunu") {
		t.Errorf("First block should contain its own text.\nGot: %q", texts[0])
	}

	// Second block: should contain context from first block + speaker prefix
	if !strings.HasPrefix(texts[1], "[Mehmet]: ") {
		t.Errorf("Second block should have speaker prefix.\nGot: %q", texts[1])
	}
	if !strings.Contains(texts[1], "bütçe") {
		t.Errorf("Second block should contain context from first block.\nGot: %q", texts[1])
	}
	if !strings.Contains(texts[1], "yarın yapalım") {
		t.Errorf("Second block should contain its own text.\nGot: %q", texts[1])
	}
}

func TestPrepareEmbeddingTexts_ShortBlockMerging(t *testing.T) {
	blocks := []models.TranscriptChatBlock{
		makeBlock("Ahmet", "Evet"),               // Very short — should merge with next (same speaker)
		makeBlock("Ahmet", "Bunu yarın yapalım"), // Next block, same speaker
		makeBlock("Mehmet", "Tamam anlaştık"),
	}

	texts := prepareEmbeddingTexts(blocks)

	if len(texts) != 3 {
		t.Fatalf("Expected 3 texts (same count as blocks), got %d", len(texts))
	}

	// First two blocks should be merged (same speaker, first is short)
	// Both should contain the merged text with speaker prefix
	if !strings.Contains(texts[0], "Evet") || !strings.Contains(texts[0], "yarın yapalım") {
		t.Errorf("First block should contain merged text.\nGot: %q", texts[0])
	}
	if !strings.HasPrefix(texts[0], "[Ahmet]: ") {
		t.Errorf("First block should have speaker prefix.\nGot: %q", texts[0])
	}
	if !strings.Contains(texts[1], "Evet") || !strings.Contains(texts[1], "yarın yapalım") {
		t.Errorf("Second block should contain same merged text.\nGot: %q", texts[1])
	}

	// Third block from different speaker
	if !strings.Contains(texts[2], "Tamam anlaştık") {
		t.Errorf("Third block should contain its own text.\nGot: %q", texts[2])
	}
	if !strings.HasPrefix(texts[2], "[Mehmet]: ") {
		t.Errorf("Third block should have Mehmet speaker prefix.\nGot: %q", texts[2])
	}
}

func TestPrepareEmbeddingTexts_ShortBlockDifferentSpeaker(t *testing.T) {
	blocks := []models.TranscriptChatBlock{
		makeBlock("Ahmet", "Evet"),
		makeBlock("Mehmet", "Bu projeyi deploy edeceğiz"),
	}

	texts := prepareEmbeddingTexts(blocks)

	if len(texts) != 2 {
		t.Fatalf("Expected 2 texts, got %d", len(texts))
	}

	// Different speakers: should NOT merge
	if !strings.HasPrefix(texts[0], "[Ahmet]: ") {
		t.Errorf("First block should have Ahmet prefix.\nGot: %q", texts[0])
	}
	if !strings.HasPrefix(texts[1], "[Mehmet]: ") {
		t.Errorf("Second block should have Mehmet prefix.\nGot: %q", texts[1])
	}
}

func TestPrepareEmbeddingTexts_EmptyBlocks(t *testing.T) {
	texts := prepareEmbeddingTexts(nil)
	if texts != nil {
		t.Errorf("Expected nil for empty input, got %v", texts)
	}

	texts = prepareEmbeddingTexts([]models.TranscriptChatBlock{})
	if texts != nil {
		t.Errorf("Expected nil for empty slice, got %v", texts)
	}
}

func TestPrepareEmbeddingTexts_SingleBlock(t *testing.T) {
	blocks := []models.TranscriptChatBlock{
		makeBlock("Ahmet", "Bugün toplantımız var"),
	}

	texts := prepareEmbeddingTexts(blocks)

	if len(texts) != 1 {
		t.Fatalf("Expected 1 text, got %d", len(texts))
	}

	expected := "[Ahmet]: Bugün toplantımız var"
	if texts[0] != expected {
		t.Errorf("Single block should have speaker prefix.\nGot:      %q\nExpected: %q", texts[0], expected)
	}
}

func TestPrepareEmbeddingTexts_SpeakerPrefix(t *testing.T) {
	t.Run("Normal speaker gets prefix", func(t *testing.T) {
		blocks := []models.TranscriptChatBlock{
			makeBlock("Ayşe", "Toplantı notlarını paylaşacağım"),
		}
		texts := prepareEmbeddingTexts(blocks)
		if !strings.HasPrefix(texts[0], "[Ayşe]: ") {
			t.Errorf("Expected speaker prefix, got: %q", texts[0])
		}
	})

	t.Run("Unknown speaker gets no prefix", func(t *testing.T) {
		blocks := []models.TranscriptChatBlock{
			makeBlock("unknown", "Bir şey söylendi"),
		}
		texts := prepareEmbeddingTexts(blocks)
		if strings.HasPrefix(texts[0], "[") {
			t.Errorf("Unknown speaker should not get prefix, got: %q", texts[0])
		}
	})

	t.Run("Empty speaker gets no prefix", func(t *testing.T) {
		blocks := []models.TranscriptChatBlock{
			makeBlock("", "Anonim konuşma"),
		}
		texts := prepareEmbeddingTexts(blocks)
		if strings.HasPrefix(texts[0], "[") {
			t.Errorf("Empty speaker should not get prefix, got: %q", texts[0])
		}
	})
}

func TestPrepareEmbeddingTexts_LongContextTruncation(t *testing.T) {
	longText := ""
	for i := 0; i < 50; i++ {
		longText += "bu çok uzun bir cümle tekrar ediyor "
	}
	blocks := []models.TranscriptChatBlock{
		makeBlock("Ahmet", longText),
		makeBlock("Mehmet", "Anladım"),
	}

	texts := prepareEmbeddingTexts(blocks)

	// The second block's text should not contain the entire long text
	// Context overlap is capped at embeddingContextOverlapMaxChars + speaker prefix
	if len(texts[1]) > len(longText) {
		t.Errorf("Context overlap should be truncated, but text is longer than original long text")
	}
}
