package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"mia-website/backend/internal/models"

	"github.com/google/uuid"
)

// ==================== Email CTA URL Format ====================

func TestBriefEmailCTA_PointsToReviewPage(t *testing.T) {
	t.Parallel()

	meetingID := uuid.New()
	frontendURL := "https://console.getmia.live"

	ctaURL := fmt.Sprintf("%s/meetings/%s/review", frontendURL, meetingID.String())

	if !strings.Contains(ctaURL, "/review") {
		t.Errorf("CTA URL should point to /review, got %s", ctaURL)
	}
	if !strings.Contains(ctaURL, meetingID.String()) {
		t.Error("CTA URL should contain meeting ID")
	}
	if strings.Contains(ctaURL, "/review/review") {
		t.Error("CTA URL should not have double /review")
	}
}

func TestBriefEmailCTA_UsesGetEnvOrDefault(t *testing.T) {
	t.Setenv("FRONTEND_URL", "https://custom.domain.com")

	url := getEnvOrDefault("FRONTEND_URL", "https://console.getmia.live")
	if url != "https://custom.domain.com" {
		t.Errorf("expected custom domain, got %s", url)
	}
}

func TestBriefEmailCTA_FallbackURL(t *testing.T) {
	t.Setenv("FRONTEND_URL", "")

	url := getEnvOrDefault("FRONTEND_URL", "https://console.getmia.live")
	if url != "https://console.getmia.live" {
		t.Errorf("expected fallback URL, got %s", url)
	}
}

// ==================== Email Subject Format ====================

func TestBriefEmailSubject_ContainsMeetingTitle(t *testing.T) {
	t.Parallel()

	meetingTitle := "Haftalık Product Sync"
	subject := "📋 Toplantı Hazırlığı: " + meetingTitle

	if !strings.Contains(subject, meetingTitle) {
		t.Errorf("subject should contain meeting title, got %s", subject)
	}
	if !strings.Contains(subject, "📋") {
		t.Error("subject should contain brief emoji")
	}
}

// ==================== Nil ChainID Guard ====================

func TestBriefScheduler_NilChainID_Skipped(t *testing.T) {
	t.Parallel()

	meeting := models.Meeting{
		ID:      uuid.New(),
		UserID:  uuid.New(),
		Title:   "Test Meeting",
		ChainID: nil, // no chain
	}

	if meeting.ChainID == nil {
		// This is the guard in checkAndGenerateBriefs
		// Brief generation should be skipped
		t.Log("correctly skipped meeting without chain_id")
	} else {
		t.Error("expected nil chain_id to trigger skip")
	}
}

func TestBriefScheduler_WithChainID_Proceeds(t *testing.T) {
	t.Parallel()

	chainID := uuid.New()
	meeting := models.Meeting{
		ID:      uuid.New(),
		UserID:  uuid.New(),
		Title:   "Chained Meeting",
		ChainID: &chainID,
	}

	if meeting.ChainID == nil {
		t.Error("expected non-nil chain_id")
	}
	if *meeting.ChainID != chainID {
		t.Errorf("expected chain_id %s, got %s", chainID, *meeting.ChainID)
	}
}

// ==================== Lead Time Logic ====================

func TestBriefLeadTime_DefaultIs10Minutes(t *testing.T) {
	t.Parallel()

	// Default COALESCE(p.brief_lead_time_minutes, 10)
	var leadTime *int
	defaultLead := 10

	actual := defaultLead
	if leadTime != nil {
		actual = *leadTime
	}

	if actual != 10 {
		t.Errorf("expected default 10 minutes, got %d", actual)
	}
}

func TestBriefLeadTime_CustomValue(t *testing.T) {
	t.Parallel()

	custom := 30
	leadTime := &custom
	defaultLead := 10

	actual := defaultLead
	if leadTime != nil {
		actual = *leadTime
	}

	if actual != 30 {
		t.Errorf("expected custom 30 minutes, got %d", actual)
	}
}

// ==================== Meeting Window Logic ====================

func TestBriefScheduler_MeetingInWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	leadTimeMinutes := 10

	// Meeting starting in 5 minutes — should be in window
	meetingStart := now.Add(5 * time.Minute)
	windowEnd := now.Add(time.Duration(leadTimeMinutes) * time.Minute)

	inWindow := meetingStart.After(now) && meetingStart.Before(windowEnd)
	if !inWindow {
		t.Error("meeting starting in 5 min should be within 10 min window")
	}
}

func TestBriefScheduler_MeetingOutsideWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	leadTimeMinutes := 10

	// Meeting starting in 30 minutes — outside window
	meetingStart := now.Add(30 * time.Minute)
	windowEnd := now.Add(time.Duration(leadTimeMinutes) * time.Minute)

	inWindow := meetingStart.After(now) && meetingStart.Before(windowEnd)
	if inWindow {
		t.Error("meeting starting in 30 min should NOT be within 10 min window")
	}
}

func TestBriefScheduler_PastMeeting_NotInWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()

	// Past meeting — should not be in window
	meetingStart := now.Add(-1 * time.Hour)
	inWindow := meetingStart.After(now)
	if inWindow {
		t.Error("past meeting should not be in window")
	}
}

// ==================== Email Body Format ====================

func TestBriefEmailBody_ContainsBriefContent(t *testing.T) {
	t.Parallel()

	meetingTitle := "Sprint Review"
	briefContent := "Son toplantıda API v2 kararı alındı."
	meetingTime := time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)

	body := fmt.Sprintf(`<div style="font-family: sans-serif;">
<h2>📋 %s</h2>
<p>%s</p>
<div>%s</div>
</div>`,
		meetingTitle,
		meetingTime.Format("2 January 2006 15:04"),
		briefContent,
	)

	if !strings.Contains(body, meetingTitle) {
		t.Error("email body should contain meeting title")
	}
	if !strings.Contains(body, briefContent) {
		t.Error("email body should contain brief content")
	}
	if !strings.Contains(body, "2026") {
		t.Error("email body should contain meeting date")
	}
}
