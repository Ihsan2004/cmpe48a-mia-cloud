package services

import (
	"testing"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

func TestDetectNoise(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		duration float64
		expected bool
	}{
		{
			name:     "Single word from blacklist",
			text:     "evet",
			duration: 0.5,
			expected: true,
		},
		{
			name:     "Single word not in blacklist (still noise - any single word)",
			text:     "merhaba",
			duration: 0.5,
			expected: true,
		},
		{
			name:     "Very short duration",
			text:     "test",
			duration: 0.1,
			expected: true,
		},
		{
			name:     "Multiple filler words",
			text:     "evet tamam",
			duration: 1.0,
			expected: true,
		},
		{
			name:     "Dash-only pattern (screen sharing artifact)",
			text:     "-   -   -   -   -   -   -",
			duration: 2.5,
			expected: true,
		},
		{
			name:     "Short dash pattern",
			text:     "-   -   -",
			duration: 0.5,
			expected: true,
		},
		{
			name:     "Dash with real text mixed in — NOT noise",
			text:     "-   -   -   -  Neredeydi bizim diyagramlar? -   -",
			duration: 3.0,
			expected: false,
		},
		{
			name:     "Dash-dot pattern",
			text:     "- - - -",
			duration: 2.3,
			expected: true,
		},
		{
			name:     "Only punctuation and symbols",
			text:     "... --- ...",
			duration: 1.5,
			expected: true,
		},
		{
			name:     "Normal sentence",
			text:     "Bu bir normal cümledir",
			duration: 2.0,
			expected: false,
		},
		{
			name:     "Mixed content",
			text:     "evet bunu yapacağım",
			duration: 2.0,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectNoise(tt.text, tt.duration)
			if result != tt.expected {
				t.Errorf("detectNoise(%q, %.1f) = %v, expected %v", tt.text, tt.duration, result, tt.expected)
			}
		})
	}
}

func TestDetectAmbientSound(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		// True positives: pure ambient sound markers
		{
			name:     "Only ambient sound in parens",
			text:     "(gülüşmeler)",
			expected: true,
		},
		{
			name:     "Only ambient sound in brackets",
			text:     "[müzik]",
			expected: true,
		},
		{
			name:     "Ambient with extra whitespace",
			text:     "  (alkış sesleri)  ",
			expected: true,
		},
		{
			name:     "Ambient with keyword substring",
			text:     "(arka plan gürültüsü)",
			expected: true,
		},
		{
			name:     "Kapı çalma sesi",
			text:     "(kapı çalma sesi)",
			expected: true,
		},
		{
			name:     "Ekran kapama sesi",
			text:     "(ekran kapama sesi)",
			expected: true,
		},
		// True negatives: real speech with parentheses — must NOT be noise
		{
			name:     "POS Point of Sale with parens",
			text:     "POS (Point of Sale) ödeme sistemi",
			expected: false,
		},
		{
			name:     "MIA with parens explanation",
			text:     "MIA (Meeting Intelligence Assistant) demo'sunu göstereceğiz",
			expected: false,
		},
		{
			name:     "Speech with ambient in middle",
			text:     "Merhaba (ekran kapama sesi) nasılsın",
			expected: false,
		},
		{
			name:     "Speech with bracket ambient",
			text:     "[gürültü] test devam ediyor",
			expected: false,
		},
		{
			name:     "Normal text no parens",
			text:     "Bu normal bir cümledir",
			expected: false,
		},
		{
			name:     "Empty text",
			text:     "",
			expected: false,
		},
		{
			name:     "Unknown bracket content — not ambient",
			text:     "(bilinmeyen ifade)",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectAmbientSound(tt.text)
			if result != tt.expected {
				t.Errorf("detectAmbientSound(%q) = %v, expected %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestCorrectionDiffGuard(t *testing.T) {
	tests := []struct {
		name      string
		original  string
		corrected string
		expected  bool
	}{
		{
			name:      "Normal correction — minor cleanup",
			original:  "eee şey bi MIA demosunu göstericez işte yarın",
			corrected: "MIA demo'sunu göstereceğiz yarın",
			expected:  true,
		},
		{
			name:      "Identical text",
			original:  "Bu bir test cümlesidir",
			corrected: "Bu bir test cümlesidir",
			expected:  true,
		},
		{
			name:      "GPT summarized — too short",
			original:  "Evet arkadaşlar bugün toplantıda bütçe konusunu ele alacağız ve Q2 hedeflerini belirleyeceğiz herkes hazır mı",
			corrected: "Bütçe konuşulacak",
			expected:  false,
		},
		{
			name:      "GPT hallucinated — too long",
			original:  "Bütçeyi artıralım bence bu sene",
			corrected: "Bütçeyi artırmamız gerekiyor çünkü geçen sene yapılan analizlere göre mevcut bütçe yetersiz kalıyor ve ekip genişlemesi ile birlikte operasyonel maliyetler de ciddi bir artış gösteriyor bu yüzden bu sene bütçeyi kesinlikle artırmalıyız ve yönetim kuruluna da bunu sunmalıyız",
			expected:  false,
		},
		{
			name:      "Short original — guard skipped",
			original:  "evet tamam",
			corrected: "Evet, tamam. Anlaşıldı.",
			expected:  true,
		},
		{
			name:      "Filler removal — acceptable ratio",
			original:  "eee yani şey biz bu projeyi şey yapacağız hani deploy edeceğiz",
			corrected: "Biz bu projeyi deploy edeceğiz",
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := correctionDiffGuard(tt.original, tt.corrected)
			if result != tt.expected {
				t.Errorf("correctionDiffGuard(%q, %q) = %v, expected %v",
					tt.original, tt.corrected, result, tt.expected)
			}
		})
	}
}

func TestPropagateSpeakers(t *testing.T) {
	// Helper to create segment with ID
	makeSegment := func(speaker string) models.TranscriptSegment {
		return models.TranscriptSegment{
			ID:      uuid.New(),
			Speaker: speaker,
		}
	}

	t.Run("Empty segments", func(t *testing.T) {
		segments, propagated := propagateSpeakers([]models.TranscriptSegment{})
		if len(segments) != 0 {
			t.Errorf("Expected empty segments, got %d", len(segments))
		}
		if len(propagated) != 0 {
			t.Errorf("Expected empty propagated map, got %d", len(propagated))
		}
	})

	t.Run("All speakers present", func(t *testing.T) {
		segments := []models.TranscriptSegment{
			makeSegment("Alice"),
			makeSegment("Bob"),
			makeSegment("Alice"),
		}
		result, propagated := propagateSpeakers(segments)
		if len(propagated) != 0 {
			t.Errorf("Expected no propagation, got %d", len(propagated))
		}
		if result[0].Speaker != "Alice" || result[1].Speaker != "Bob" || result[2].Speaker != "Alice" {
			t.Errorf("Speakers should remain unchanged")
		}
	})

	t.Run("Propagate from previous segment", func(t *testing.T) {
		seg1 := makeSegment("Alice")
		seg2 := makeSegment("") // Empty - should get Alice
		seg3 := makeSegment("Bob")

		segments := []models.TranscriptSegment{seg1, seg2, seg3}
		result, propagated := propagateSpeakers(segments)

		if result[1].Speaker != "Alice" {
			t.Errorf("Expected segment 2 speaker to be 'Alice', got '%s'", result[1].Speaker)
		}
		if !propagated[seg2.ID] {
			t.Errorf("Segment 2 should be marked as propagated")
		}
		if len(propagated) != 1 {
			t.Errorf("Expected 1 propagated segment, got %d", len(propagated))
		}
	})

	t.Run("First segment empty - no propagation", func(t *testing.T) {
		seg1 := makeSegment("") // Empty - nothing to propagate from
		seg2 := makeSegment("Alice")

		segments := []models.TranscriptSegment{seg1, seg2}
		result, propagated := propagateSpeakers(segments)

		if result[0].Speaker != "" {
			t.Errorf("First segment should remain empty, got '%s'", result[0].Speaker)
		}
		if len(propagated) != 0 {
			t.Errorf("Expected no propagation, got %d", len(propagated))
		}
	})

	t.Run("Unknown speaker not propagated", func(t *testing.T) {
		seg1 := makeSegment("unknown")
		seg2 := makeSegment("") // Should NOT get "unknown"
		seg3 := makeSegment("Alice")

		segments := []models.TranscriptSegment{seg1, seg2, seg3}
		result, propagated := propagateSpeakers(segments)

		// seg2 should remain empty because "unknown" is not valid for propagation
		if result[1].Speaker != "" {
			t.Errorf("Segment 2 should remain empty (unknown is invalid), got '%s'", result[1].Speaker)
		}
		if len(propagated) != 0 {
			t.Errorf("Expected no propagation, got %d", len(propagated))
		}
	})

	t.Run("Consecutive empty segments", func(t *testing.T) {
		seg1 := makeSegment("Alice")
		seg2 := makeSegment("") // Gets Alice
		seg3 := makeSegment("") // Gets Alice from seg2

		segments := []models.TranscriptSegment{seg1, seg2, seg3}
		result, propagated := propagateSpeakers(segments)

		if result[1].Speaker != "Alice" {
			t.Errorf("Segment 2 should be 'Alice', got '%s'", result[1].Speaker)
		}
		if result[2].Speaker != "Alice" {
			t.Errorf("Segment 3 should be 'Alice', got '%s'", result[2].Speaker)
		}
		if !propagated[seg2.ID] || !propagated[seg3.ID] {
			t.Errorf("Both segments should be marked as propagated")
		}
	})
}
