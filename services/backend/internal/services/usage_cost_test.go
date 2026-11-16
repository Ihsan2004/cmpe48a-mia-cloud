package services

import (
	"context"
	"math"
	"testing"
	"time"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/google/uuid"
)

// ==================== CalculateCost Tests ====================

func TestCalculateCost_GPT54_TokensOnly(t *testing.T) {
	t.Parallel()

	// gpt-5.4: input=$2.50/1M, output=$10.00/1M
	cost := CalculateCost("gpt-5.4", 1000, 500, 0)

	expectedInput := 1000.0 / 1_000_000 * 2.50  // 0.0025
	expectedOutput := 500.0 / 1_000_000 * 10.00 // 0.005
	expected := expectedInput + expectedOutput  // 0.0075

	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("expected cost %.6f, got %.6f", expected, cost)
	}
}

func TestCalculateCost_STT_AudioOnly(t *testing.T) {
	t.Parallel()

	// scribe_v2: $0.40/min
	cost := CalculateCost("scribe_v2", 0, 0, 300) // 5 minutes

	expected := (300.0 / 60.0) * 0.40 // 5 * 0.40 = 2.00
	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("expected cost %.4f, got %.4f", expected, cost)
	}
}

func TestCalculateCost_Embedding(t *testing.T) {
	t.Parallel()

	// text-embedding-3-small: input=$0.02/1M
	cost := CalculateCost("text-embedding-3-small", 10000, 0, 0)

	expected := 10000.0 / 1_000_000 * 0.02 // 0.0002
	if math.Abs(cost-expected) > 0.00001 {
		t.Errorf("expected cost %.6f, got %.6f", expected, cost)
	}
}

func TestCalculateCost_UnknownModel(t *testing.T) {
	t.Parallel()

	cost := CalculateCost("unknown-model-xyz", 1000, 500, 60)
	if cost != 0 {
		t.Errorf("expected 0 cost for unknown model, got %.4f", cost)
	}
}

func TestCalculateCost_ZeroUsage(t *testing.T) {
	t.Parallel()

	cost := CalculateCost("gpt-5.4", 0, 0, 0)
	if cost != 0 {
		t.Errorf("expected 0 cost for zero usage, got %.4f", cost)
	}
}

func TestCalculateCost_OnlyInputTokens(t *testing.T) {
	t.Parallel()

	cost := CalculateCost("gpt-5.4", 1_000_000, 0, 0)
	expected := 2.50 // 1M tokens * $2.50/1M

	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("expected cost %.4f, got %.4f", expected, cost)
	}
}

func TestCalculateCost_OnlyOutputTokens(t *testing.T) {
	t.Parallel()

	cost := CalculateCost("gpt-5.4", 0, 1_000_000, 0)
	expected := 10.00 // 1M tokens * $10.00/1M

	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("expected cost %.4f, got %.4f", expected, cost)
	}
}

func TestCalculateCost_MixedUsage(t *testing.T) {
	t.Parallel()

	// Correction: GPT tokens + no audio
	cost := CalculateCost("gpt-5.4", 50000, 10000, 0)

	expectedInput := 50000.0 / 1_000_000 * 2.50
	expectedOutput := 10000.0 / 1_000_000 * 10.00
	expected := expectedInput + expectedOutput

	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("expected cost %.6f, got %.6f", expected, cost)
	}
}

func TestCalculateCost_AllKnownModels(t *testing.T) {
	t.Parallel()

	// Verify all models in pricing table return non-zero for some usage
	knownModels := []string{
		"gpt-5.4", "gpt-4o", "gpt-4.1", "gpt-4.1-mini",
		"text-embedding-3-large", "text-embedding-3-small",
		"scribe_v2_realtime", "scribe_v2",
		"eleven_flash_v2_5",
		"eleven_english_sts_v2",
		"sonar",
	}

	for _, model := range knownModels {
		cost := CalculateCost(model, 1000, 1000, 60)
		if cost == 0 {
			t.Errorf("expected non-zero cost for known model %s", model)
		}
	}
}

type captureUsageQuotaService struct {
	recorded chan repository.UsageLog
}

func (s *captureUsageQuotaService) Check(context.Context, uuid.UUID) (quota.Status, error) {
	return quota.Status{}, nil
}

func (s *captureUsageQuotaService) RecordUsage(_ context.Context, log repository.UsageLog) error {
	s.recorded <- log
	return nil
}

func TestUsageRecorderRecordBatch_UsesAtomicQuotaWriter(t *testing.T) {
	svc := &captureUsageQuotaService{recorded: make(chan repository.UsageLog, 2)}
	recorder := &UsageRecorder{
		quota:    svc,
		costFunc: CalculateCost,
	}
	userID := uuid.New()
	logs := []repository.UsageLog{
		{
			UserID:      userID,
			Feature:     "summary",
			Provider:    "openai",
			Model:       "gpt-5.4",
			InputTokens: 1000,
		},
		{
			UserID:   userID,
			Feature:  "transcription",
			Provider: "elevenlabs",
			Model:    "scribe_v2",
			CostUSD:  1.23,
		},
	}

	recorder.RecordBatch(logs)

	first := waitForRecordedUsage(t, svc.recorded)
	second := waitForRecordedUsage(t, svc.recorded)

	if first.CostUSD == 0 {
		t.Fatalf("first log cost should be calculated before recording")
	}
	if second.CostUSD != 1.23 {
		t.Fatalf("explicit batch cost should be preserved, got %.4f", second.CostUSD)
	}
	if logs[0].CostUSD != 0 {
		t.Fatalf("RecordBatch should not mutate caller-owned log slice")
	}
}

func waitForRecordedUsage(t *testing.T, ch <-chan repository.UsageLog) repository.UsageLog {
	t.Helper()
	select {
	case log := <-ch:
		return log
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage recorder goroutine")
		return repository.UsageLog{}
	}
}
