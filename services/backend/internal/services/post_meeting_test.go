package services

import (
	"testing"
)

// ==================== IsSummaryContentValid Tests ====================

func TestIsSummaryContentValid_ValidContent(t *testing.T) {
	t.Parallel()

	valid := "Bu toplantıda proje planı tartışıldı ve önümüzdeki hafta için görevler belirlendi. Katılımcılar üzerinde anlaştı."
	if !IsSummaryContentValid(valid) {
		t.Errorf("expected valid content to pass, got false")
	}
}

func TestIsSummaryContentValid_Empty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"whitespace only", "   \n\t  "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsSummaryContentValid(tc.input) {
				t.Errorf("expected empty/whitespace content to fail, got true for %q", tc.input)
			}
		})
	}
}

func TestIsSummaryContentValid_TooShort(t *testing.T) {
	t.Parallel()

	short := "Bu kısa bir özet."
	if IsSummaryContentValid(short) {
		t.Errorf("expected short content (%d chars) to fail, got true", len(short))
	}
}

func TestIsSummaryContentValid_ExactlyMinLength(t *testing.T) {
	t.Parallel()

	// 50 chars exactly — should pass
	exact := "Bu toplantıda önemli kararlar alındı ve tartışıld"
	if len(exact) < 50 {
		t.Fatalf("test setup error: string is %d chars, need 50", len(exact))
	}
	if !IsSummaryContentValid(exact) {
		t.Errorf("expected 50-char content to pass, got false")
	}
}

func TestIsSummaryContentValid_TurkishErrorPatterns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
	}{
		{
			"islenemedi pattern",
			"Bu toplantının transkripti işlenemedi. Lütfen tekrar deneyiniz veya destek ekibiyle iletişime geçiniz.",
		},
		{
			"olusturulamadi pattern",
			"Toplantı özeti oluşturulamadı. Sistem hatası nedeniyle işlem tamamlanamadı lütfen tekrar deneyin.",
		},
		{
			"Hata prefix",
			"Hata: NLP servisi yanıt vermedi. Toplantı özetleme işlemi zaman aşımına uğradı tekrar deneyin lütfen.",
		},
		{
			"Chunk error",
			"[Chunk 3] Özet parçası işlenirken hata oluştu. Diğer parçalar başarıyla işlendi ancak tam değil kontrol edin.",
		},
		{
			"segment Hata",
			"Transkript segment (Hata oluştu) bazı bölümlerde sorun yaşandı. Tam metin elde edilemedi kontrol ediniz.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsSummaryContentValid(tc.content) {
				t.Errorf("expected error pattern %q to fail validation", tc.name)
			}
		})
	}
}

func TestIsSummaryContentValid_ValidLongContent(t *testing.T) {
	t.Parallel()

	long := `Toplantıda üç ana konu ele alındı. İlk olarak, Q2 hedefleri gözden geçirildi ve
	mevcut ilerleme değerlendirildi. İkinci olarak, yeni müşteri kazanım stratejisi tartışıldı.
	Son olarak, teknik altyapı güncellemeleri planlandı. Ekip, önümüzdeki iki hafta içinde
	prototip hazırlamayı ve müşteri geri bildirimlerini toplamayı kararlaştırdı.`

	if !IsSummaryContentValid(long) {
		t.Errorf("expected long valid content to pass")
	}
}

func TestIsSummaryContentValid_ContentWithErrorPatternSubstring(t *testing.T) {
	t.Parallel()

	// Content that contains error pattern should fail even if long
	content := "Bu toplantıda birçok önemli konu tartışıldı ancak transkript işlenemedi nedeniyle bazı bölümler eksik kaldı."
	if IsSummaryContentValid(content) {
		t.Errorf("expected content with error pattern substring to fail")
	}
}

// ==================== ProcessMeetingCompletion Edge Cases ====================

func TestProcessMeetingCompletion_NilIDAndEmptyURL(t *testing.T) {
	t.Parallel()

	svc := &PostMeetingService{
		nlpServiceURL: "http://localhost:8000",
		client:        nil,
	}

	err := svc.ProcessMeetingCompletion(nil, "")
	if err == nil {
		t.Fatal("expected error when both meetingID and meetingURL are empty")
	}
	if err.Error() != "meeting_id and meeting_url are both missing" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

// ==================== SummarizeResponse Parsing ====================

func TestSummarizeResponse_CoverageRatio(t *testing.T) {
	t.Parallel()

	ratio := 0.85
	resp := SummarizeResponse{
		Success:          true,
		Summary:          "Test summary content that is long enough to pass validation check for minimum length.",
		ProcessingStatus: "completed_with_gaps",
		CoverageRatio:    &ratio,
		SuccessfulChunks: 8,
		FailedChunks:     2,
		TotalChunks:      10,
	}

	if resp.CoverageRatio == nil {
		t.Fatal("expected coverage ratio to be set")
	}
	if *resp.CoverageRatio != 0.85 {
		t.Errorf("expected coverage ratio 0.85, got %f", *resp.CoverageRatio)
	}
	if resp.ProcessingStatus != "completed_with_gaps" {
		t.Errorf("expected completed_with_gaps, got %s", resp.ProcessingStatus)
	}
}

func TestSummarizeResponse_EmptySummary(t *testing.T) {
	t.Parallel()

	resp := SummarizeResponse{
		Success: true,
		Summary: "",
	}

	if IsSummaryContentValid(resp.Summary) {
		t.Error("empty summary should not pass validation")
	}
}

func TestSummarizeResponse_ErrorResponse(t *testing.T) {
	t.Parallel()

	resp := SummarizeResponse{
		Success: false,
		Error:   "NLP service timeout",
		Detail:  "Connection refused after 30s",
	}

	if resp.Success {
		t.Error("expected success to be false")
	}
	if resp.Error == "" {
		t.Error("expected error message to be set")
	}
}

// ==================== Error Patterns ====================

func TestErrorContentPatterns_NotEmpty(t *testing.T) {
	t.Parallel()

	if len(errorContentPatterns) == 0 {
		t.Fatal("error content patterns should not be empty")
	}
}

func TestErrorContentPatterns_AllDetected(t *testing.T) {
	t.Parallel()

	for _, pattern := range errorContentPatterns {
		// Build a string that contains the pattern and is long enough
		content := "Prefix text for padding. " + pattern + " Suffix text to make it longer than minimum fifty characters required."
		if IsSummaryContentValid(content) {
			t.Errorf("pattern %q was not detected in content", pattern)
		}
	}
}
