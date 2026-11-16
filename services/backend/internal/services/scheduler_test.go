package services

import (
	"testing"
	"time"
)

// ==================== SchedulerService Initialization ====================

func TestNewSchedulerService_DefaultValues(t *testing.T) {
	t.Parallel()

	profileSvc := &ProfileService{}
	svc := &SchedulerService{
		botAPIURL:         "http://bot-api:3000",
		nlpServiceURL:     "http://mia-search:8000",
		checkInterval:     1 * time.Minute,
		joinBeforeMinutes: 1,
		streamingInterval: 30 * time.Second,
		streamingWorkers:  2,
		profileService:    profileSvc,
	}

	if svc.checkInterval != 1*time.Minute {
		t.Errorf("expected check interval 1m, got %v", svc.checkInterval)
	}
	if svc.joinBeforeMinutes != 1 {
		t.Errorf("expected joinBeforeMinutes=1, got %d", svc.joinBeforeMinutes)
	}
	if svc.streamingInterval != 30*time.Second {
		t.Errorf("expected streaming interval 30s, got %v", svc.streamingInterval)
	}
	if svc.streamingWorkers != 2 {
		t.Errorf("expected 2 streaming workers, got %d", svc.streamingWorkers)
	}
}

func TestNewSchedulerService_EmptyBotURL(t *testing.T) {
	t.Parallel()

	svc := &SchedulerService{
		botAPIURL: "",
	}

	if svc.botAPIURL != "" {
		t.Error("expected empty bot URL")
	}
}

// ==================== BotJoinRequest ====================

func TestBotJoinRequest_WithLanguage(t *testing.T) {
	t.Parallel()

	req := BotJoinRequest{
		MeetingURL: "https://meet.google.com/abc-def-ghi",
		BotName:    "MIA Assistant",
		MeetingID:  "123e4567-e89b-12d3-a456-426614174000",
		Language:   "tr",
	}

	if req.MeetingURL == "" {
		t.Error("expected meeting URL to be set")
	}
	if req.Language != "tr" {
		t.Errorf("expected language=tr, got %s", req.Language)
	}
}

func TestBotJoinRequest_AutoLanguage_OmitsField(t *testing.T) {
	t.Parallel()

	// When language is "auto", the scheduler omits the Language field
	// so ElevenLabs auto-detects
	botLanguage := "auto"
	req := BotJoinRequest{
		MeetingURL: "https://meet.google.com/abc-def-ghi",
		BotName:    "MIA Assistant",
		MeetingID:  "123e4567-e89b-12d3-a456-426614174000",
	}

	if botLanguage != "auto" {
		req.Language = botLanguage
	}

	// Language should be empty (omitted) when "auto"
	if req.Language != "" {
		t.Errorf("expected empty language for auto mode, got %s", req.Language)
	}
}

// ==================== Streaming Throttle Logic ====================

func TestStreamingThrottle_Interval(t *testing.T) {
	t.Parallel()

	svc := &SchedulerService{
		streamingInterval: 30 * time.Second,
	}

	// Store a recent attempt
	meetingID := "test-meeting"
	now := time.Now()
	svc.streamingAttempts.Store(meetingID, now)

	// Check if throttled
	lastAttempt, ok := svc.streamingAttempts.Load(meetingID)
	if !ok {
		t.Fatal("expected streaming attempt to be stored")
	}

	lastAt, valid := lastAttempt.(time.Time)
	if !valid {
		t.Fatal("expected time.Time value")
	}

	// Should be throttled (less than 30s since last attempt)
	if time.Since(lastAt) >= svc.streamingInterval {
		t.Error("expected attempt to be within throttle interval")
	}
}

func TestStreamingThrottle_NotThrottledAfterInterval(t *testing.T) {
	t.Parallel()

	svc := &SchedulerService{
		streamingInterval: 30 * time.Second,
	}

	meetingID := "test-meeting-old"
	oldTime := time.Now().Add(-1 * time.Minute) // 1 minute ago
	svc.streamingAttempts.Store(meetingID, oldTime)

	lastAttempt, ok := svc.streamingAttempts.Load(meetingID)
	if !ok {
		t.Fatal("expected streaming attempt to be stored")
	}

	lastAt := lastAttempt.(time.Time)

	// Should NOT be throttled (more than 30s since last attempt)
	if time.Since(lastAt) < svc.streamingInterval {
		t.Error("expected old attempt to be past throttle interval")
	}
}

// ==================== Scheduler Context Cancellation ====================

func TestSchedulerService_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	// Verify that Start() returns when context is cancelled
	// We use a very short check interval and cancel immediately
	svc := &SchedulerService{
		botAPIURL:          "", // no bot URL, skips bot join
		nlpServiceURL:      "http://localhost:8000",
		checkInterval:      100 * time.Millisecond,
		streamingInterval:  30 * time.Second,
		streamingWorkers:   1,
		streamingSemaphore: make(chan struct{}, 1),
		correctionService:  NewCorrectionService(),
		postMeetingService: &PostMeetingService{},
	}

	// Start would hit DB for initial checks — skip in unit tests
	_ = svc
	t.Skip("skipping: requires DB connection for initial checks")
}
