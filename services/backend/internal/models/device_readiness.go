package models

import (
	"time"

	"github.com/google/uuid"
)

type DeviceReadinessRequest struct {
	DeviceID     string   `json:"device_id"`
	Score        *float64 `json:"score"`
	SpeedMbps    *float64 `json:"speed_mbps,omitempty"`
	LatencyMs    *float64 `json:"latency_ms,omitempty"`
	CpuPct       *float64 `json:"cpu_pct,omitempty"`
	BatteryLevel *float64 `json:"battery_level,omitempty"`
}

type DeviceReadinessResponse struct {
	Picked              bool      `json:"picked"`
	CurrentSourceDevice string    `json:"current_source_device_id"`
	YourRank            int       `json:"your_rank"`
	MeetingID           uuid.UUID `json:"meeting_id"`
	DeviceID            string    `json:"device_id"`
}

type DeviceCandidate struct {
	UserID         uuid.UUID `json:"user_id"`
	DeviceID       string    `json:"device_id"`
	MeetingID      uuid.UUID `json:"meeting_id"`
	Score          float64   `json:"score"`
	EffectiveScore float64   `json:"effective_score"`
	IsHost         bool      `json:"is_host"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	RegisteredAt   time.Time `json:"registered_at"`
}

type PickerEvent struct {
	Type              string `json:"type"`
	MeetingID         string `json:"meeting_id"`
	PickedDeviceID    string `json:"picked_device_id"`
	PickedUserID      string `json:"picked_user_id"`
	PreviousDeviceID  string `json:"previous_device_id,omitempty"`
	Reason            string `json:"reason,omitempty"`
	CandidatesInPool  int    `json:"candidates_in_pool"`
}
