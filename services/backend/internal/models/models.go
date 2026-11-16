package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Profile struct {
	ID                     uuid.UUID  `json:"id"`
	Email                  *string    `json:"email,omitempty"`
	FullName               string     `json:"full_name"`
	JobTitle               *string    `json:"job_title,omitempty"`
	Company                *string    `json:"company,omitempty"`
	Timezone               string     `json:"timezone"`
	DefaultMeetingDuration int        `json:"default_meeting_duration"`
	WorkStartHour          *string    `json:"work_start_hour,omitempty"`
	WorkEndHour            *string    `json:"work_end_hour,omitempty"`
	BotName                string     `json:"bot_name"`     // Custom bot name for meetings
	BotLanguage            string     `json:"bot_language"` // Preferred language for bot (tr, en)
	GoogleRefreshToken     *string    `json:"-"`            // Never exposed in JSON for security
	IsCalendarConnected    bool       `json:"is_calendar_connected"`
	GoogleEmail            *string    `json:"google_email,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at"`
	InterfaceLanguage      string     `json:"interface_language"`            // Preferred language for user interface (tr, en)
	BriefLeadTimeMinutes   int        `json:"brief_lead_time_minutes"`      // MVP: brief timing (default 10)
	RecordingMethod        string     `json:"recording_method"`              // batch pipeline: bot|desktop (default "desktop"; bot is dormant — UI no longer surfaces it)
	BriefDeliverViaEmail   bool       `json:"brief_deliver_via_email"`       // brief delivery toggle — default false, opt-in
	BriefDeliverViaSlack   bool       `json:"brief_deliver_via_slack"`       // brief delivery toggle — default false, opt-in
	EmailUnsubscribedAt    *time.Time `json:"email_unsubscribed_at,omitempty"` // signed unsubscribe link → set timestamp; NULL = subscribed
	TeamsOnboardingStatus  string     `json:"teams_onboarding_status"`         // v1.1 Phase 4: pending|completed|skipped|empty_state_skipped
}

// RecordingMethodBot / RecordingMethodDesktop / RecordingMethodSkip:
// allowed values for the recording-method enum. `bot` and `desktop`
// apply to both profiles.recording_method and meetings.recording_method_
// override; `skip` is meeting-level only — there is intentionally no
// profile-level "skip every meeting" escape hatch.
//
// Shared constants prevent drift between handler validation and DB check.
const (
	RecordingMethodBot     = "bot"
	RecordingMethodDesktop = "desktop"
	RecordingMethodSkip    = "skip"
)

// IsValidRecordingMethod returns true when m is one of the supported
// profile-level enum values (bot|desktop). Empty strings intentionally
// return false — callers should pass a sentinel like RecordingMethodBot
// explicitly instead of "".
func IsValidRecordingMethod(m string) bool {
	return m == RecordingMethodBot || m == RecordingMethodDesktop
}

// IsValidRecordingMethodOverride extends the validity check with `skip`
// for the meeting-level override column. Use this for callers that
// validate inbound payloads against `meetings.recording_method_override`.
func IsValidRecordingMethodOverride(m string) bool {
	return m == RecordingMethodBot ||
		m == RecordingMethodDesktop ||
		m == RecordingMethodSkip
}

// ResolveEffectiveMethod implements the Phase 2.4 B5 resolution order:
//
//	meeting.recording_method_override
//	?? profile.recording_method
//	?? "bot"
//
// `override` may be nil (no per-meeting override). `profileDefault` is
// the user's profile.recording_method; an empty string is treated as
// missing and falls through to "bot". The returned string is always one
// of {"bot", "desktop", "skip"} — callers downstream can switch on this
// to decide bot dispatch vs desktop arming vs no-op.
func ResolveEffectiveMethod(override *string, profileDefault string) string {
	if override != nil && *override != "" {
		return *override
	}
	if profileDefault != "" {
		return profileDefault
	}
	return RecordingMethodBot
}

// TeamsOnboardingStatus* — v1.1 Phase 4 onboarding state machine (ONBOARD-03).
//
// Lifecycle:
//
//	pending             — initial state for any new profile; calendar not yet connected.
//	completed           — detection ran and user saved teams (or pre-v1.1 connected user backfilled here).
//	skipped             — user dismissed the onboarding flow without saving.
//	empty_state_skipped — algorithm returned no teams and user accepted the empty state.
//
// Shared constants prevent drift between handler validation and the DB CHECK
// constraint (sql/migrations/014). Adding a new state requires both a new
// migration and an update to IsValidTeamsOnboardingStatus.
const (
	TeamsOnboardingStatusPending           = "pending"
	TeamsOnboardingStatusCompleted         = "completed"
	TeamsOnboardingStatusSkipped           = "skipped"
	TeamsOnboardingStatusEmptyStateSkipped = "empty_state_skipped"
)

// IsValidTeamsOnboardingStatus returns true when s is one of the four allowed
// onboarding states. Empty strings return false — callers MUST pass an
// explicit constant rather than the zero value. Mirrors IsValidRecordingMethod.
func IsValidTeamsOnboardingStatus(s string) bool {
	return s == TeamsOnboardingStatusPending ||
		s == TeamsOnboardingStatusCompleted ||
		s == TeamsOnboardingStatusSkipped ||
		s == TeamsOnboardingStatusEmptyStateSkipped
}

type Meeting struct {
	ID                    uuid.UUID        `json:"id"`
	UserID                uuid.UUID        `json:"user_id"`
	Title                 string           `json:"title"`
	StartTime             time.Time        `json:"start_time"`
	EndTime               time.Time        `json:"end_time"`
	MeetingURL            *string          `json:"meeting_url,omitempty"`
	Description           *string          `json:"description,omitempty"`
	Location              *string          `json:"location,omitempty"`
	Attendees             []Attendee       `json:"attendees,omitempty"`
	OrganizerEmail        *string          `json:"organizer_email,omitempty"`
	GoogleEventID         string           `json:"google_event_id"`
	Summary               *string          `json:"summary,omitempty" gorm:"-"`           // Virtual field, populated from meeting_summaries
	ExecutiveSummary      *string          `json:"executive_summary,omitempty" gorm:"-"` // Short summary, populated from meeting_summaries
	Summaries             []MeetingSummary `json:"-" gorm:"foreignKey:MeetingID"`        // Relation to fetch summaries
	ActionItems           []string         `json:"action_items,omitempty"`               // Legacy format
	ActionItemsStructured []ActionItem     `json:"action_items_structured,omitempty"`    // Faz 4: Structured with metadata
	KeyTopics             []string         `json:"key_topics,omitempty"`                 // Legacy format
	KeyTopicsStructured   []KeyTopic       `json:"key_topics_structured,omitempty"`      // Structured with stable topic order
	AutoBotEnabled        *bool            `json:"auto_bot_enabled,omitempty"`           // Default: true
	BotStatus             *string          `json:"bot_status,omitempty"`                 // joining, joined, failed
	BotJoinedAt           *time.Time       `json:"bot_joined_at,omitempty"`
	LastHeartbeatAt       *time.Time       `json:"last_heartbeat_at,omitempty"`
	AgendaItems           *string          `json:"agenda_items,omitempty"`          // JSON string
	ExpectedOutcomes      *string          `json:"expected_outcomes,omitempty"`     // JSON string
	CalendarSeriesID      *string          `json:"calendar_series_id,omitempty"`   // MVP: chain detection
	ChainID               *uuid.UUID       `json:"chain_id,omitempty"`             // MVP: meeting chain FK
	ProcessedAt           *time.Time       `json:"processed_at,omitempty"`         // MVP: extraction timestamp
	CreatedAt             time.Time        `json:"created_at"`

	// Batch transcription pipeline (migration 005)
	AudioStoragePath            *string    `json:"audio_storage_path,omitempty"`
	AudioContentType            *string    `json:"audio_content_type,omitempty"`
	AudioSizeBytes              *int64     `json:"audio_size_bytes,omitempty"`
	AudioDurationSeconds        *int       `json:"audio_duration_seconds,omitempty"`
	AudioSource                 *string    `json:"audio_source,omitempty"` // bot|desktop
	TranscriptionStatus         string     `json:"transcription_status"`   // pending|uploading|processing|completed|failed
	TranscriptionProviderJobID  *string    `json:"transcription_provider_job_id,omitempty"`
	TranscriptionAttemptCount   int        `json:"transcription_attempt_count"`
	TranscriptionLastError      *string    `json:"transcription_last_error,omitempty"`
	TranscriptionCompletedAt    *time.Time `json:"transcription_completed_at,omitempty"`
	AudioDeletedAt              *time.Time `json:"audio_deleted_at,omitempty"`
	RecordingMethodOverride     *string    `json:"recording_method_override,omitempty"` // bot|desktop
	// Phase 2.5 B8 — app the recorder's ad-hoc fusion engine
	// identified as the meeting source. NULL on calendar-armed and
	// bot recordings. Allowed values: zoom | google_meet |
	// microsoft_teams | webex | slack_huddle | discord | facetime |
	// other. CHECK constraint enforces the allowlist.
	DetectedApp *string `json:"detected_app,omitempty"`

	// Chain detection v3 (migration 019). Provenance + manual lock so
	// the matcher stays deterministic across re-syncs and the UI
	// surfaces the right "fix" affordance per assignment method.
	// ChainAssignmentReason is raw JSONB — the typed per-feature shape
	// lives next to the chain matcher (Phase 2).
	ChainAssignmentMethod   *string    `json:"chain_assignment_method,omitempty"`
	ChainAssignmentScore    *float64   `json:"chain_assignment_score,omitempty"`
	ChainAssignmentReason   []byte     `json:"-"`
	ChainAssignmentLockedAt *time.Time `json:"chain_assignment_locked_at,omitempty"`
}

// IsValidDetectedApp returns true when s is one of the supported
// ad-hoc detection app identifiers per RECORDER_ROADMAP §2.5 B8.
// Empty strings return false — callers should use a sentinel value
// (e.g. "other") when the detector can't classify the app.
func IsValidDetectedApp(s string) bool {
	switch s {
	case "zoom",
		"google_meet",
		"microsoft_teams",
		"webex",
		"slack_huddle",
		"discord",
		"facetime",
		"other":
		return true
	default:
		return false
	}
}

// EditMeetingRequest is the request body for PATCH /api/meetings/:id
type EditMeetingRequest struct {
	Title            *string    `json:"title,omitempty"`
	StartTime        *time.Time `json:"start_time,omitempty"`
	EndTime          *time.Time `json:"end_time,omitempty"`
	Description      *string    `json:"description,omitempty"`
	Location         *string    `json:"location,omitempty"`
	Attendees        []string   `json:"attendees,omitempty"`
	AgendaItems      *string    `json:"agenda_items,omitempty"`
	ExpectedOutcomes *string    `json:"expected_outcomes,omitempty"`
	SendUpdates      *string    `json:"send_updates,omitempty"` // "all" | "none"
}

type MeetingSummary struct {
	ID               uuid.UUID `json:"id"`
	MeetingID        uuid.UUID `json:"meeting_id"`
	Content          string    `json:"content"`
	SummaryType      string    `json:"summary_type"`                // final_detailed, final_brief, interim (legacy)
	ProcessingStatus string    `json:"processing_status,omitempty"` // pending, processing, completed, completed_with_gaps, failed
	RetryCount       int       `json:"retry_count,omitempty"`
	SummarySource    *string   `json:"summary_source,omitempty"` // chat_blocks_finalized/raw_segments_forced/etc.
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type SummaryError struct {
	ID           uuid.UUID  `json:"id"`
	MeetingID    uuid.UUID  `json:"meeting_id"`
	ErrorType    string     `json:"error_type"` // api_error, validation_error, empty_response, chunk_failure, timeout
	ErrorMessage string     `json:"error_message"`
	ErrorDetail  string     `json:"error_detail,omitempty"`
	Phase        string     `json:"phase"` // generation, correction, save, email
	RetryCount   int        `json:"retry_count"`
	Resolved     bool       `json:"resolved"`
	CreatedAt    time.Time  `json:"created_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
}

type ProgressiveMeetingSummary struct {
	ID        uuid.UUID  `json:"id"`
	MeetingID uuid.UUID  `json:"meeting_id"`
	Content   string     `json:"content"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type ActionItem struct {
	ID           uuid.UUID  `json:"id"`
	MeetingID    uuid.UUID  `json:"meeting_id"`
	Description  string     `json:"description"`
	AssigneeName *string    `json:"assignee_name,omitempty"`
	AssigneeID   *uuid.UUID `json:"assignee_id,omitempty"`
	Priority     *string    `json:"priority,omitempty"`
	DueDate      *time.Time `json:"due_date,omitempty"`
	// Faz 3 fields
	CommitmentLevel *string     `json:"commitment_level,omitempty"` // committed/candidate
	TopicIDs        []uuid.UUID `json:"topic_ids,omitempty"`        // Related key_topics UUIDs
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

type KeyTopic struct {
	ID         uuid.UUID `json:"id"`
	MeetingID  uuid.UUID `json:"meeting_id"`
	Topic      string    `json:"topic"`
	TopicOrder int       `json:"topic_order"` // Stable chronological order for dashboard/timeline (NOT NULL in schema)
	CreatedAt  time.Time `json:"created_at"`
}

type Attendee struct {
	Email          string `json:"email"`
	DisplayName    string `json:"display_name,omitempty"`
	ResponseStatus string `json:"response_status,omitempty"` // accepted, declined, tentative, needsAction
}

type SyncCalendarRequest struct {
	AccessToken string `json:"access_token"`
}

type CreateMeetingRequest struct {
	AccessToken      string    `json:"access_token"`
	Title            string    `json:"title"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
	Description      *string   `json:"description,omitempty"`
	Location         *string   `json:"location,omitempty"`
	Attendees        []string  `json:"attendees,omitempty"`        // Email addresses
	CreateGoogleMeet bool      `json:"create_google_meet"`         // Whether to create a Google Meet link
	AutoBotEnabled   *bool     `json:"auto_bot_enabled,omitempty"` // Default: true, user can disable
	MeetingURL       *string   `json:"meeting_url,omitempty"`      // Optional URL for ad-hoc meetings
}

// UpdateDesktopEndTimeRequest is the body for
// PATCH /api/meetings/:id/desktop-end. Only desktop synthetic meetings
// (google_event_id like "desktop_%") accept this; calling on a calendar
// meeting returns 422.
type UpdateDesktopEndTimeRequest struct {
	EndedAt time.Time `json:"ended_at"` // Required, RFC3339, must be > start_time
}

// CreateDesktopMeetingRequest is the request body for POST /api/meetings/desktop.
// Used by the desktop recorder to register an ad-hoc recording session WITHOUT
// touching Google Calendar (no event creation, no bot dispatch). Server forces
// audio_source="desktop", auto_bot_enabled=false, and synthesizes
// google_event_id="desktop_<local_session_id>" — making the create idempotent
// per (user_id, local_session_id).
type CreateDesktopMeetingRequest struct {
	// Required. Recorder-owned ULID generated at Start. Drives the synthetic
	// google_event_id for idempotency: the same (user, local_session_id) pair
	// always returns the same backend meeting row. Must be opaque, URL-safe,
	// and contain no whitespace.
	LocalSessionID string `json:"local_session_id"`

	// Optional. If empty/null, server generates "Desktop Recording <ts>".
	Title *string `json:"title,omitempty"`

	// Required, RFC3339. Recorder-side wall clock at the moment the user
	// pressed Start.
	StartedAt time.Time `json:"started_at"`

	// Optional. Absent during open-session create (recording in progress).
	// When provided on a subsequent call, ended_at must be > started_at.
	// PATCH /api/meetings/:id/desktop-end is the canonical way to set this
	// after recording stops; including it on POST is supported for callers
	// that already have it (e.g. orphan recovery resubmitting a finished
	// session).
	EndedAt *time.Time `json:"ended_at,omitempty"`

	// Optional. Phase 2B: app the recorder detected as the meeting source
	// (e.g. "zoom", "google_meet"). Server validates against an allowlist;
	// unknown values are stored as-is for forward compatibility but logged.
	DetectedApp *string `json:"detected_app,omitempty"`
}

// User represents a user in the system
type User struct {
	ID                  uuid.UUID  `json:"id"`
	Email               string     `json:"email"`
	PasswordHash        string     `json:"-"` // Never exposed in JSON
	EmailVerified       bool       `json:"email_verified"`
	FullName            *string    `json:"full_name,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	LastLoginAt         *time.Time `json:"last_login_at,omitempty"`
	IsActive            bool       `json:"is_active"`
	FailedLoginAttempts int        `json:"failed_login_attempts"`
	LockedUntil         *time.Time `json:"locked_until,omitempty"`
	// Admin field removed
}

// SignUpRequest represents a user signup request
type SignUpRequest struct {
	Email    string               `json:"email"`
	Password string               `json:"password"`
	FullName string               `json:"full_name,omitempty"`
	Consents []SignupConsentInput `json:"consents,omitempty"`
}

// SignupConsentInput is what the client posts at signup. The service
// layer validates that all MandatoryConsentTypes are present with
// granted=true and the expected version, then persists each as a row
// sharing one event_group_id.
type SignupConsentInput struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	Granted bool   `json:"granted"`
}

// SignInRequest represents a user signin request
type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthResponse represents the response after successful authentication
type AuthResponse struct {
	User         *User  `json:"user"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds
	Message      string `json:"message,omitempty"`
}

// Consent type constants — must match the CHECK constraint in
// sql/migrations/020_user_consents.sql. Adding a new type requires a
// follow-up migration to extend the CHECK list.
const (
	ConsentTypeTermsOfService      = "terms_of_service"
	ConsentTypePrivacyPolicy       = "privacy_policy"
	ConsentTypeKVKKAydinlatma      = "kvkk_aydinlatma"
	ConsentTypeCrossBorderTransfer = "cross_border_transfer"
	ConsentTypeAIProcessing        = "ai_processing"
	ConsentTypePilotSozlesmesi     = "pilot_sozlesmesi"
	ConsentTypeMarketing           = "marketing"
)

// MandatoryConsentTypes is the set required at signup. Pilot users
// additionally need ConsentTypePilotSozlesmesi; marketing is opt-in.
var MandatoryConsentTypes = []string{
	ConsentTypeTermsOfService,
	ConsentTypePrivacyPolicy,
	ConsentTypeKVKKAydinlatma,
	ConsentTypeCrossBorderTransfer,
	ConsentTypeAIProcessing,
}

func IsValidConsentType(t string) bool {
	switch t {
	case ConsentTypeTermsOfService, ConsentTypePrivacyPolicy, ConsentTypeKVKKAydinlatma,
		ConsentTypeCrossBorderTransfer, ConsentTypeAIProcessing,
		ConsentTypePilotSozlesmesi, ConsentTypeMarketing:
		return true
	}
	return false
}

// UserConsent records one consent action (grant or withdrawal).
// Append-only: acceptance creates a new row; withdrawal updates the
// latest matching row's revoked_at; re-acceptance after revocation
// creates a new row.
type UserConsent struct {
	ID              uuid.UUID       `json:"id"`
	UserID          uuid.UUID       `json:"user_id"`
	ConsentType     string          `json:"consent_type"`
	DocumentVersion string          `json:"document_version"`
	DocumentURL     *string         `json:"document_url,omitempty"`
	Granted         bool            `json:"granted"`
	AcceptedAt      time.Time       `json:"accepted_at"`
	RevokedAt       *time.Time      `json:"revoked_at,omitempty"`
	RevokeReason    *string         `json:"revoke_reason,omitempty"`
	IPAddress       *string         `json:"ip_address,omitempty"`
	UserAgent       *string         `json:"user_agent,omitempty"`
	EventGroupID    *uuid.UUID      `json:"event_group_id,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// IsActive returns true when the consent is currently valid: granted
// and not revoked. Use this for gate checks rather than poking the
// individual fields.
func (c *UserConsent) IsActive() bool {
	return c.Granted && c.RevokedAt == nil
}

type TranscriptSegment struct {
	ID        uuid.UUID `json:"id"`
	MeetingID uuid.UUID `json:"meeting_id"`
	StartTime float64   `json:"start_time"`
	EndTime   float64   `json:"end_time"`
	Speaker   string    `json:"speaker"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

type TranscriptChatBlock struct {
	ID                   uuid.UUID   `json:"id"`
	MeetingID            uuid.UUID   `json:"meeting_id"`
	Speaker              string      `json:"speaker"`
	Text                 string      `json:"text"` // Corrected text
	StartTime            float64     `json:"start_time"`
	EndTime              float64     `json:"end_time"`
	RawSegmentIDs        []uuid.UUID `json:"raw_segment_ids"` // References to source segments
	IsNoise              bool        `json:"is_noise,omitempty"`
	Embedding            *[]float64  `gorm:"type:vector(1536)" json:"embedding,omitempty"`
	EmbeddingModel       *string     `json:"embedding_model,omitempty"`
	EmbeddingGeneratedAt *time.Time  `json:"embedding_generated_at,omitempty"`
	IsFinalized          bool        `json:"is_finalized"`
	FinalizedAt          *time.Time  `json:"finalized_at,omitempty"`
	SourceSegmentCount   *int        `json:"source_segment_count,omitempty"`
	CreatedAt            time.Time   `json:"created_at"`
}

// ==================== MVP: Decision Tracker + Brief ====================

// MeetingChain represents a recurring meeting series or participant group.
// Chain detection v3 (migration 019) made chains tenant-scoped — every
// row carries the owning user_id so a Google calendar_series_id shared
// across users can't accidentally fan a single chain across tenants.
type MeetingChain struct {
	ID                uuid.UUID `json:"id"`
	UserID            uuid.UUID `json:"user_id"`
	ChainType         string    `json:"chain_type"`          // calendar_series, participant_overlap_title
	CalendarSeriesID  *string   `json:"calendar_series_id,omitempty"`
	ParticipantHash   *string   `json:"participant_hash,omitempty"`
	DisplayName       *string   `json:"display_name,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// Chain assignment method enum — see migration 019 + roadmap.
// Tracks WHY a meeting was attached to its chain so the UI can surface
// the right correction affordance and the matcher can honour manual locks.
const (
	ChainAssignmentMethodSeriesID       = "series_id"
	ChainAssignmentMethodAutoScore      = "auto_score"
	ChainAssignmentMethodLLMSuggestion  = "llm_suggestion"
	ChainAssignmentMethodDecisionMatch  = "decision_match"
	ChainAssignmentMethodManualUser     = "manual_user"
	ChainAssignmentMethodManualRemoved  = "manual_removed"
)

// ChainDecisionV2 mode + status enum — chain_decisions_v2 audit rows.
// Mode flags whether the row was a write that mutated chain_id
// (`applied`), a pending user prompt (`suggestion`), or an audit-only
// observation (`shadow`). Status is the lifecycle: `pending` waits on
// user input, `applied` fired immediately, `shadow_only` recorded but
// not surfaced, `suppressed` blocked by the streak gate.
const (
	ChainDecisionModeApplied    = "applied"
	ChainDecisionModeSuggestion = "suggestion"
	ChainDecisionModeShadow     = "shadow"

	ChainDecisionStatusApplied    = "applied"
	ChainDecisionStatusPending    = "pending"
	ChainDecisionStatusShadowOnly = "shadow_only"
	ChainDecisionStatusSuppressed = "suppressed"
)

// DecisionItem represents a decision, action item, or open topic extracted from a meeting
type DecisionItem struct {
	ID                     uuid.UUID  `json:"id"`
	MeetingID              uuid.UUID  `json:"meeting_id"`
	ChainID                *uuid.UUID `json:"chain_id,omitempty"`
	UserID                 uuid.UUID  `json:"user_id"`
	ItemType               string     `json:"item_type"`    // decision, action_item, open_topic
	Text                   string     `json:"text"`
	OwnerName              *string    `json:"owner_name,omitempty"`
	SourceQuote            *string    `json:"source_quote,omitempty"`
	Context                *string    `json:"context,omitempty"`                  // ADR: why this decision was made
	AlternativesConsidered *string    `json:"alternatives_considered,omitempty"`  // ADR: rejected alternatives
	DecisionTimestampStart *float64   `json:"decision_timestamp_start,omitempty"` // transcript time range
	DecisionTimestampEnd   *float64   `json:"decision_timestamp_end,omitempty"`
	Status                 string     `json:"status"`       // confirmed, completed, dismissed
	UserEdited             bool       `json:"user_edited"`
	ConfirmedAt            *time.Time `json:"confirmed_at,omitempty"`
	CompletedAt            *time.Time `json:"completed_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// MeetingNote represents a personal note for a meeting (one per user per meeting)
type MeetingNote struct {
	ID        uuid.UUID `json:"id"`
	MeetingID uuid.UUID `json:"meeting_id"`
	UserID    uuid.UUID `json:"user_id"`
	Content   *string   `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MeetingBrief represents a cached pre-meeting brief
type MeetingBrief struct {
	ID               uuid.UUID  `json:"id"`
	UserID           uuid.UUID  `json:"user_id"`
	TargetMeetingID  uuid.UUID  `json:"target_meeting_id"`
	ChainID          *uuid.UUID `json:"chain_id,omitempty"`
	Content          string     `json:"content"`
	GenerationMethod string     `json:"generation_method"` // llm, template_fallback
	OpenedAt         *time.Time `json:"opened_at,omitempty"`
	EmailSentAt      *time.Time `json:"email_sent_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// BriefFeedback represents user feedback on a brief
type BriefFeedback struct {
	ID              uuid.UUID  `json:"id"`
	UserID          uuid.UUID  `json:"user_id"`
	TargetMeetingID uuid.UUID  `json:"target_meeting_id"`
	BriefID         *uuid.UUID `json:"brief_id,omitempty"`
	Feedback        string     `json:"feedback"` // useful, not_useful
	Note            *string    `json:"note,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ==================== v1.1 Phase 4: Team Detection ====================

// Team represents a user-scoped team (v1.1 Phase 4, TEAMS-01).
// Mirrors sql/migrations/015_teams.sql column-for-column.
//
// Constraints (DB-enforced, not re-validated here):
//   - UNIQUE (user_id, name) — same user cannot have duplicate team names.
//   - ON DELETE CASCADE on user_id — deleting the profile cascades to teams.
//   - ON DELETE CASCADE on teams.id — deleting a team cascades to team_members
//     (defined on team_members.team_id; see TeamMember).
type Team struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TeamMember represents a member of a Team (v1.1 Phase 4, TEAMS-02 + PRIVACY-04).
// Mirrors sql/migrations/016_team_members.sql column-for-column.
//
// MINIMAL-PII CONTRACT (PRIVACY-04): only name + title + email are permitted.
// Adding phone, photo, manager_email, linkedin_url, is_external, domain, or any
// other contact-style field requires a privacy review and a roadmap change —
// it is not a deferred feature, it is out of scope.
//
// Title is nullable (*string) because EDIT-08 makes it user-typed and optional.
type TeamMember struct {
	ID        uuid.UUID `json:"id"`
	TeamID    uuid.UUID `json:"team_id"`
	Name      string    `json:"name"`
	Title     *string   `json:"title"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TeamDetectionRun records one detection run for reproducibility (v1.1 Phase 4
// schema; Phase 6 owns the values that go into ParametersJSONB and
// AlgorithmVersion). Mirrors sql/migrations/017_team_detection_runs.sql
// column-for-column.
//
// Status lifecycle: running -> success | degenerate | error.
//
// Phase 6 convention: ErrorMessage is reused to carry the EmptyReason
// string when Status='degenerate'. See repository/team.go
// UpdateDetectionRunFinish for the full convention rationale.
//
// Phase 6 gap-closure (migration 018) added four columns to satisfy
// OBS-01's literal must-have: InputEdgeCount + OutputMemberCount +
// OutputLargestTeamSize + StageDurationsJSONB. All four are nil-safe
// and populated by teamdetection.Service.Detect from in-memory
// pipeline state. StageDurationsJSONB key set is locked to
// {"fetch","graph_build","cluster","score"} (NOT "persist" — the
// persist stage's duration is observable only via the Prometheus
// histogram; including it in the JSONB would be self-referential
// because the JSONB is written by the UPDATE the persist stage is
// timing). See service.go finalizePersist doc-comment for rationale.
type TeamDetectionRun struct {
	ID                    uuid.UUID       `json:"id"`
	UserID                uuid.UUID       `json:"user_id"`
	StartedAt             time.Time       `json:"started_at"`
	FinishedAt            *time.Time      `json:"finished_at"`
	Status                string          `json:"status"`
	InputMeetingCount     *int            `json:"input_meeting_count"`
	InputAttendeeCount    *int            `json:"input_attendee_count"`
	InputEdgeCount        *int            `json:"input_edge_count"` // NEW (migration 018)
	OutputTeamCount       *int            `json:"output_team_count"`
	OutputMemberCount     *int            `json:"output_member_count"`      // NEW (migration 018)
	OutputLargestTeamSize *int            `json:"output_largest_team_size"` // NEW (migration 018)
	ParametersJSONB       json.RawMessage `json:"parameters_jsonb"`
	StageDurationsJSONB   json.RawMessage `json:"stage_durations_jsonb"` // NEW (migration 018)
	AlgorithmVersion      *string         `json:"algorithm_version"`
	ErrorMessage          *string         `json:"error_message"`
	CreatedAt             time.Time       `json:"created_at"`
}
