package services

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"mia-website/backend/internal/repository"
	"mia-website/backend/internal/services/quota"

	"github.com/google/uuid"
)

// Model pricing per unit. Override via environment variables.
// Token prices are per 1M tokens. Audio prices are per minute.
var modelPricing = map[string]struct {
	InputPerMillion  float64
	OutputPerMillion float64
	AudioPerMinute   float64
}{
	// OpenAI chat models
	"gpt-5.4":      {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"gpt-4o":       {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"gpt-4.1":      {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"gpt-4.1-mini": {InputPerMillion: 0.40, OutputPerMillion: 1.60},

	// OpenAI embeddings
	"text-embedding-3-large": {InputPerMillion: 0.13},
	"text-embedding-3-small": {InputPerMillion: 0.02},

	// ElevenLabs STT
	"scribe_v2_realtime": {AudioPerMinute: 0.40},
	"scribe_v2":          {AudioPerMinute: 0.40},

	// ElevenLabs TTS (per 1M characters mapped to OutputPerMillion)
	"eleven_flash_v2_5": {OutputPerMillion: 30.00},

	// ElevenLabs STS (Speech-to-Speech, per minute of audio)
	"eleven_english_sts_v2": {AudioPerMinute: 0.80},

	// Perplexity (per 1M tokens, approximate)
	"sonar": {InputPerMillion: 1.00, OutputPerMillion: 1.00},
}

func init() {
	loadPricingOverrides()
}

func loadPricingOverrides() {
	// Allow overriding default pricing via env vars.
	// Format: USAGE_PRICE_<MODEL>_INPUT, USAGE_PRICE_<MODEL>_OUTPUT, USAGE_PRICE_<MODEL>_AUDIO
	// Example: USAGE_PRICE_GPT_5_4_INPUT=3.00
	for model, pricing := range modelPricing {
		envKey := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(model, "-", "_"), ".", "_"))

		if v := os.Getenv("USAGE_PRICE_" + envKey + "_INPUT"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				pricing.InputPerMillion = f
				modelPricing[model] = pricing
			}
		}
		if v := os.Getenv("USAGE_PRICE_" + envKey + "_OUTPUT"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				pricing.OutputPerMillion = f
				modelPricing[model] = pricing
			}
		}
		if v := os.Getenv("USAGE_PRICE_" + envKey + "_AUDIO"); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				pricing.AudioPerMinute = f
				modelPricing[model] = pricing
			}
		}
	}
}

// CalculateCost returns the estimated cost in USD for a given usage.
func CalculateCost(model string, inputTokens, outputTokens int, audioSeconds float64) float64 {
	pricing, ok := modelPricing[model]
	if !ok {
		return 0
	}

	cost := 0.0
	if inputTokens > 0 {
		cost += float64(inputTokens) / 1_000_000 * pricing.InputPerMillion
	}
	if outputTokens > 0 {
		cost += float64(outputTokens) / 1_000_000 * pricing.OutputPerMillion
	}
	if audioSeconds > 0 {
		cost += (audioSeconds / 60.0) * pricing.AudioPerMinute
	}
	return cost
}

// UsageRecorder writes usage_logs AND bumps the user's monthly quota
// counter in a single atomic statement (quota.Service.RecordUsage →
// repository.UserQuotaRepository.RecordUsageAtomic). The repo write
// fires in a goroutine so the request path doesn't pay the DB latency;
// any failure logs and is reconciled by the drift cron (Faz 5).
type UsageRecorder struct {
	quota    quota.Service
	costFunc func(model string, in, out int, audio float64) float64
}

func NewUsageRecorder() *UsageRecorder {
	return &UsageRecorder{
		quota:    quota.NewService(repository.NewUserQuotaRepository()),
		costFunc: CalculateCost,
	}
}

// Record persists a usage log entry + counter bump asynchronously
// (fire-and-forget). Must not add latency to the request path.
func (r *UsageRecorder) Record(userID uuid.UUID, teamID *uuid.UUID, meetingID *uuid.UUID, feature, provider, model string, inputTokens, outputTokens int, audioSeconds float64, metadata map[string]interface{}) {
	cost := r.costFunc(model, inputTokens, outputTokens, audioSeconds)

	log := repository.UsageLog{
		UserID:       userID,
		TeamID:       teamID,
		MeetingID:    meetingID,
		Feature:      feature,
		Provider:     provider,
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		AudioSeconds: audioSeconds,
		CostUSD:      cost,
		Metadata:     metadata,
	}

	go func() {
		if err := r.quota.RecordUsage(context.Background(), log); err != nil {
			slog.Error("Failed to record usage",
				"feature", feature,
				"provider", provider,
				"error", err,
			)
		}
	}()
}

// RecordBatch persists multiple usage log entries asynchronously. Each
// row still goes through the quota writer so usage_logs and the active
// quota counter stay atomic per operation.
func (r *UsageRecorder) RecordBatch(logs []repository.UsageLog) {
	if len(logs) == 0 {
		return
	}

	batch := append([]repository.UsageLog(nil), logs...)
	for i := range batch {
		if batch[i].CostUSD == 0 {
			batch[i].CostUSD = r.costFunc(batch[i].Model, batch[i].InputTokens, batch[i].OutputTokens, batch[i].AudioSeconds)
		}
	}

	go func(batch []repository.UsageLog) {
		for _, log := range batch {
			if err := r.quota.RecordUsage(context.Background(), log); err != nil {
				slog.Error("Failed to record usage batch item",
					"feature", log.Feature,
					"provider", log.Provider,
					"error", err,
				)
			}
		}
	}(batch)
}
