package handlers

import (
	"testing"
	"time"

	"mia-website/backend/internal/repository"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func TestBuildUsageSummaryResponse_RedactsDollarFields(t *testing.T) {
	resp := buildUsageSummaryResponse(2026, time.May, []repository.FeatureUsage{
		{
			Feature:      "summary",
			TotalInput:   1000,
			TotalOutput:  250,
			TotalAudio:   0,
			TotalCost:    1.23,
			RequestCount: 2,
		},
	})

	if _, ok := resp["total_cost_usd"]; ok {
		t.Fatalf("summary response must not leak total_cost_usd: %#v", resp)
	}
	features := resp["features"].(fiber.Map)
	summary := features["summary"].(fiber.Map)
	if _, ok := summary["cost_usd"]; ok {
		t.Fatalf("feature response must not leak cost_usd: %#v", summary)
	}
}

func TestBuildMeetingUsageResponse_RedactsDollarFields(t *testing.T) {
	meetingID := uuid.New()
	resp := buildMeetingUsageResponse(meetingID, []repository.UsageLog{
		{
			Feature:      "transcription",
			Provider:     "elevenlabs",
			Model:        "scribe_v2",
			AudioSeconds: 60,
			CostUSD:      0.40,
			CreatedAt:    time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
		},
	})

	if _, ok := resp["total_cost_usd"]; ok {
		t.Fatalf("meeting usage response must not leak total_cost_usd: %#v", resp)
	}
	items := resp["usage"].([]fiber.Map)
	if _, ok := items[0]["cost_usd"]; ok {
		t.Fatalf("meeting usage item must not leak cost_usd: %#v", items[0])
	}
}
