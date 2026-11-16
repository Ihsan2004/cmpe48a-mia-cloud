package services

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/google/uuid"
)

// ==================== E2E Test Helpers ====================

func skipIfNoE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_TEST") != "true" {
		t.Skip("skipping E2E test: set E2E_TEST=true with DATABASE_URL, NLP_SERVICE_URL, OPENAI_API_KEY")
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("skipping E2E test: DATABASE_URL not set")
	}
	if os.Getenv("NLP_SERVICE_URL") == "" {
		t.Skip("skipping E2E test: NLP_SERVICE_URL not set")
	}
}

func e2eSetupDB(t *testing.T) {
	t.Helper()
	if err := repository.Connect(); err != nil {
		t.Fatalf("failed to connect to DB: %v", err)
	}
	t.Cleanup(func() {
		repository.Close()
	})
}

func e2eCreateUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	email := fmt.Sprintf("e2e_%s@test.com", userID.String()[:8])

	_, err := repository.DB.Exec(ctx, `
		INSERT INTO public.users (id, email, password_hash, full_name, email_verified, is_active)
		VALUES ($1, $2, 'e2e_hash', 'E2E Test User', true, true)
	`, userID, email)
	if err != nil {
		t.Fatalf("failed to create e2e user: %v", err)
	}

	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", userID)
	})

	return userID
}

func e2eCreateMeeting(t *testing.T, ctx context.Context, userID uuid.UUID) uuid.UUID {
	t.Helper()
	meetingID := uuid.New()

	_, err := repository.DB.Exec(ctx, `
		INSERT INTO meetings (id, user_id, title, start_time, end_time, google_event_id, bot_status)
		VALUES ($1, $2, 'E2E Test Meeting', $3, $4, $5, 'completed')
	`, meetingID, userID,
		time.Now().Add(-2*time.Hour),
		time.Now().Add(-1*time.Hour),
		"e2e_event_"+meetingID.String()[:8],
	)
	if err != nil {
		t.Fatalf("failed to create e2e meeting: %v", err)
	}

	t.Cleanup(func() {
		repository.DB.Exec(context.Background(), "DELETE FROM meeting_summaries WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM summary_errors WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM transcript_chat_blocks WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM transcript_segments WHERE meeting_id = $1", meetingID)
		repository.DB.Exec(context.Background(), "DELETE FROM meetings WHERE id = $1", meetingID)
	})

	return meetingID
}

func e2eSeedTranscriptSegments(t *testing.T, ctx context.Context, meetingID uuid.UUID) {
	t.Helper()

	segments := []struct {
		speaker   string
		text      string
		startTime float64
		endTime   float64
	}{
		{"Alice", "Merhaba arkadaşlar, bugün proje planını konuşacağız.", 0.0, 3.0},
		{"Alice", "Öncelikle Q2 hedeflerimizi gözden geçirelim.", 3.5, 6.0},
		{"Bob", "Evet, geçen hafta müşteri görüşmelerini tamamladık.", 6.5, 9.0},
		{"Bob", "Üç yeni müşteriden olumlu geri dönüş aldık.", 9.5, 12.0},
		{"Alice", "Güzel, peki teknik tarafta ne durumdayız?", 12.5, 14.5},
		{"Charlie", "Backend API'yi tamamladık, şimdi test aşamasındayız.", 15.0, 18.0},
		{"Charlie", "Önümüzdeki hafta deploy edebiliriz.", 18.5, 20.5},
		{"Alice", "Tamam, o zaman şöyle karar alalım: deploy önümüzdeki Çarşamba.", 21.0, 24.0},
		{"Bob", "Ben müşteri bilgilendirmesini yapacağım o zamana kadar.", 24.5, 27.0},
		{"Alice", "Toplantıyı kapatalım, teşekkürler herkese.", 27.5, 30.0},
	}

	// Set created_at to 5 minutes ago to bypass the 45s quiet period check
	createdAt := time.Now().Add(-5 * time.Minute)

	for i, seg := range segments {
		idempotencyKey := fmt.Sprintf("e2e_%s_%d", meetingID.String()[:8], i)
		_, err := repository.DB.Exec(ctx, `
			INSERT INTO transcript_segments (meeting_id, speaker, text, start_time, end_time, role, stt_provider, idempotency_key, created_at)
			VALUES ($1, $2, $3, $4, $5, 'bot', 'e2e_test', $6, $7)
		`, meetingID, seg.speaker, seg.text, seg.startTime, seg.endTime, idempotencyKey, createdAt)
		if err != nil {
			t.Fatalf("failed to seed segment %d: %v", i, err)
		}
	}
}

// ==================== E2E Tests ====================

func TestE2E_PostMeeting_HappyPath(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	e2eSeedTranscriptSegments(t, ctx, meetingID)

	// Run the actual post-meeting pipeline
	svc := NewPostMeetingService()
	err := svc.ProcessMeetingCompletion(&meetingID, "")

	if err != nil {
		t.Fatalf("ProcessMeetingCompletion failed: %v", err)
	}

	// Verify summary was created
	var summaryCount int
	err = repository.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM meeting_summaries WHERE meeting_id = $1",
		meetingID,
	).Scan(&summaryCount)
	if err != nil {
		t.Fatalf("failed to query summaries: %v", err)
	}

	if summaryCount == 0 {
		t.Error("expected at least 1 summary to be created")
	}

	// Verify summary has content
	var summaryContent string
	err = repository.DB.QueryRow(ctx,
		"SELECT content FROM meeting_summaries WHERE meeting_id = $1 AND summary_type = 'final_detailed'",
		meetingID,
	).Scan(&summaryContent)
	if err != nil {
		t.Fatalf("failed to fetch summary content: %v", err)
	}

	if len(summaryContent) < 50 {
		t.Errorf("summary content too short (%d chars): %s", len(summaryContent), summaryContent)
	}

	// Verify no unresolved errors
	var errorCount int
	repository.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM summary_errors WHERE meeting_id = $1 AND resolved = false",
		meetingID,
	).Scan(&errorCount)

	if errorCount > 0 {
		t.Errorf("expected 0 unresolved errors, got %d", errorCount)
	}

	t.Logf("E2E Happy Path passed: summary=%d chars, errors=%d", len(summaryContent), errorCount)
}

func TestE2E_PostMeeting_NoTranscript(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	ctx := context.Background()
	userID := e2eCreateUser(t, ctx)
	meetingID := e2eCreateMeeting(t, ctx, userID)
	// No transcript segments seeded

	svc := NewPostMeetingService()
	err := svc.ProcessMeetingCompletion(&meetingID, "")

	// Should fail or defer — no transcript to summarize
	if err == nil {
		// Check if summary was actually created (shouldn't be)
		var count int
		repository.DB.QueryRow(ctx,
			"SELECT COUNT(*) FROM meeting_summaries WHERE meeting_id = $1",
			meetingID,
		).Scan(&count)

		if count > 0 {
			t.Error("expected no summary for meeting without transcript")
		}
	}

	t.Logf("E2E No Transcript: err=%v (expected)", err)
}

func TestE2E_PostMeeting_InvalidMeetingID(t *testing.T) {
	skipIfNoE2E(t)
	e2eSetupDB(t)

	svc := NewPostMeetingService()
	fakeMeetingID := uuid.New()

	err := svc.ProcessMeetingCompletion(&fakeMeetingID, "")
	if err == nil {
		t.Error("expected error for non-existent meeting ID")
	}

	t.Logf("E2E Invalid Meeting: err=%v (expected)", err)
}
