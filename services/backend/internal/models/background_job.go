package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	BackgroundJobTypePostMeetingCompletion = "post_meeting_completion"
	BackgroundJobTypeTranscribe            = "transcribe"    // Phase 3: submit audio to ElevenLabs batch
	BackgroundJobTypeNotifyBrief           = "notify_brief"  // Brief delivery: one job per brief_deliveries row

	BackgroundJobStatusPending    = "pending"
	BackgroundJobStatusProcessing = "processing"
	BackgroundJobStatusCompleted  = "completed"
	BackgroundJobStatusDead       = "dead"
)

// BackgroundJob represents a durable async job stored in the database.
type BackgroundJob struct {
	ID          uuid.UUID       `json:"id"`
	JobType     string          `json:"job_type"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	ScheduledAt time.Time       `json:"scheduled_at"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// PostMeetingCompletionJobPayload is the payload format for summary generation jobs.
type PostMeetingCompletionJobPayload struct {
	MeetingID  uuid.UUID `json:"meeting_id"`
	MeetingURL string    `json:"meeting_url,omitempty"`
}

// TranscribeJobPayload carries the meeting whose audio should be submitted to
// ElevenLabs batch STT. The worker resolves the audio path + signs a short URL
// at run time so payload stays tiny and tokens don't leak into the jobs table.
type TranscribeJobPayload struct {
	MeetingID uuid.UUID `json:"meeting_id"`
}
