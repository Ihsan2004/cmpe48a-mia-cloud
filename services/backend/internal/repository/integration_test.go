package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== Test Helpers ====================

func skipIfNoIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("INTEGRATION_TEST") != "true" {
		t.Skip("skipping integration test: set INTEGRATION_TEST=true and DATABASE_URL")
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("skipping integration test: DATABASE_URL not set")
	}
}

func setupTestDB(t *testing.T) {
	t.Helper()
	if err := Connect(); err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}
	t.Cleanup(func() {
		Close()
	})
}

func createTestUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	email := "test_" + userID.String()[:8] + "@test.com"

	_, err := DB.Exec(ctx, `
		INSERT INTO public.users (id, email, password_hash, full_name, email_verified, is_active)
		VALUES ($1, $2, 'test_hash', 'Test User', true, true)
	`, userID, email)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM public.users WHERE id = $1", userID)
	})

	return userID
}

func createTestMeeting(t *testing.T, ctx context.Context, userID uuid.UUID) uuid.UUID {
	t.Helper()
	meetingID := uuid.New()
	googleEventID := "test_event_" + meetingID.String()[:8]

	_, err := DB.Exec(ctx, `
		INSERT INTO meetings (id, user_id, title, start_time, end_time, google_event_id)
		VALUES ($1, $2, 'Test Meeting', $3, $4, $5)
	`, meetingID, userID, time.Now().Add(1*time.Hour), time.Now().Add(2*time.Hour), googleEventID)
	if err != nil {
		t.Fatalf("failed to create test meeting: %v", err)
	}

	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM meetings WHERE id = $1", meetingID)
	})

	return meetingID
}

// ==================== Meeting Repository Integration Tests ====================

func TestIntegration_CreateAndGetMeeting(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	repo := NewMeetingRepository()

	meeting := &models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Integration Test Meeting",
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
		GoogleEventID: "integration_test_" + uuid.New().String()[:8],
	}

	// Create
	err := repo.CreateMeeting(ctx, meeting)
	if err != nil {
		t.Fatalf("CreateMeeting failed: %v", err)
	}

	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM meetings WHERE id = $1", meeting.ID)
	})

	// Get by ID
	fetched, err := repo.GetMeetingByID(ctx, userID, meeting.ID)
	if err != nil {
		t.Fatalf("GetMeetingByID failed: %v", err)
	}

	if fetched.Title != "Integration Test Meeting" {
		t.Errorf("expected title 'Integration Test Meeting', got '%s'", fetched.Title)
	}
}

func TestIntegration_GetMeetingBySource(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	repo := NewMeetingRepository()

	googleEventID := "source_test_" + uuid.New().String()[:8]
	meeting := &models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Source Lookup Test",
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
		GoogleEventID: googleEventID,
	}

	err := repo.CreateMeeting(ctx, meeting)
	if err != nil {
		t.Fatalf("CreateMeeting failed: %v", err)
	}
	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM meetings WHERE id = $1", meeting.ID)
	})

	// Lookup by source
	found, err := repo.GetMeetingBySource(ctx, userID, googleEventID)
	if err != nil {
		t.Fatalf("GetMeetingBySource failed: %v", err)
	}
	if found.ID != meeting.ID {
		t.Errorf("expected meeting ID %s, got %s", meeting.ID, found.ID)
	}
}

func TestIntegration_UpdateMeetingIngestion(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	repo := NewMeetingRepository()

	meeting := &models.Meeting{
		ID:            uuid.New(),
		UserID:        userID,
		Title:         "Original Title",
		StartTime:     time.Now().Add(1 * time.Hour),
		EndTime:       time.Now().Add(2 * time.Hour),
		GoogleEventID: "update_test_" + uuid.New().String()[:8],
	}

	err := repo.CreateMeeting(ctx, meeting)
	if err != nil {
		t.Fatalf("CreateMeeting failed: %v", err)
	}
	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM meetings WHERE id = $1", meeting.ID)
	})

	// Update title
	meeting.Title = "Updated Title"
	err = repo.UpdateMeetingIngestion(ctx, meeting)
	if err != nil {
		t.Fatalf("UpdateMeetingIngestion failed: %v", err)
	}

	// Verify update
	fetched, err := repo.GetMeetingByIDSystem(ctx, meeting.ID)
	if err != nil {
		t.Fatalf("GetMeetingByIDSystem failed: %v", err)
	}
	if fetched.Title != "Updated Title" {
		t.Errorf("expected 'Updated Title', got '%s'", fetched.Title)
	}
}

func TestIntegration_DeleteMeeting(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	meetingID := createTestMeeting(t, ctx, userID)
	repo := NewMeetingRepository()

	err := repo.DeleteMeeting(ctx, userID, meetingID)
	if err != nil {
		t.Fatalf("DeleteMeeting failed: %v", err)
	}

	// Verify deleted
	_, err = repo.GetMeetingByID(ctx, userID, meetingID)
	if err == nil {
		t.Error("expected error when fetching deleted meeting")
	}
}

// ==================== Summary Repository Integration Tests ====================

func TestIntegration_CreateSummary(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	meetingID := createTestMeeting(t, ctx, userID)
	repo := NewSummaryRepository()

	summary := &models.MeetingSummary{
		ID:               uuid.New(),
		MeetingID:        meetingID,
		Content:          "This is a test summary with enough content to pass validation checks.",
		SummaryType:      "final_detailed",
		ProcessingStatus: "completed",
	}

	err := repo.CreateSummary(ctx, summary)
	if err != nil {
		t.Fatalf("CreateSummary failed: %v", err)
	}

	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM meeting_summaries WHERE id = $1", summary.ID)
	})
}

func TestIntegration_CreateSummary_UniqueConstraint(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	meetingID := createTestMeeting(t, ctx, userID)
	repo := NewSummaryRepository()

	summary1 := &models.MeetingSummary{
		ID:               uuid.New(),
		MeetingID:        meetingID,
		Content:          "First summary",
		SummaryType:      "final_detailed",
		ProcessingStatus: "completed",
	}

	err := repo.CreateSummary(ctx, summary1)
	if err != nil {
		t.Fatalf("first CreateSummary failed: %v", err)
	}
	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM meeting_summaries WHERE meeting_id = $1", meetingID)
	})

	// Second summary with same type should fail (unique constraint)
	summary2 := &models.MeetingSummary{
		ID:               uuid.New(),
		MeetingID:        meetingID,
		Content:          "Duplicate summary",
		SummaryType:      "final_detailed",
		ProcessingStatus: "completed",
	}

	err = repo.CreateSummary(ctx, summary2)
	if err == nil {
		t.Error("expected unique constraint violation for duplicate summary type")
	}
}

// ==================== Summary Error Integration Tests ====================

func TestIntegration_SummaryError_CreateAndResolve(t *testing.T) {
	skipIfNoIntegration(t)
	setupTestDB(t)

	ctx := context.Background()
	userID := createTestUser(t, ctx)
	meetingID := createTestMeeting(t, ctx, userID)
	repo := NewSummaryRepository()

	summaryErr := &models.SummaryError{
		ID:           uuid.New(),
		MeetingID:    meetingID,
		ErrorType:    "api_error",
		ErrorMessage: "NLP service timeout",
		ErrorDetail:  "Connection refused after 30s",
		Phase:        "generation",
	}

	err := repo.CreateSummaryError(ctx, summaryErr)
	if err != nil {
		t.Fatalf("CreateSummaryError failed: %v", err)
	}
	t.Cleanup(func() {
		DB.Exec(context.Background(), "DELETE FROM summary_errors WHERE meeting_id = $1", meetingID)
	})

	// Resolve should not error
	err = repo.ResolveErrors(ctx, meetingID)
	if err != nil {
		t.Fatalf("ResolveErrors failed: %v", err)
	}
}

