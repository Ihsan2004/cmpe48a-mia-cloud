package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type UsageLog struct {
	ID           uuid.UUID              `json:"id"`
	UserID       uuid.UUID              `json:"user_id"`
	TeamID       *uuid.UUID             `json:"team_id,omitempty"`
	MeetingID    *uuid.UUID             `json:"meeting_id,omitempty"`
	Feature      string                 `json:"feature"`
	Provider     string                 `json:"provider"`
	Model        string                 `json:"model,omitempty"`
	InputTokens  int                    `json:"input_tokens"`
	OutputTokens int                    `json:"output_tokens"`
	AudioSeconds float64                `json:"audio_seconds"`
	CostUSD      float64                `json:"cost_usd"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
}

type FeatureUsage struct {
	Feature      string  `json:"feature"`
	TotalInput   int64   `json:"total_input_tokens"`
	TotalOutput  int64   `json:"total_output_tokens"`
	TotalAudio   float64 `json:"total_audio_seconds"`
	TotalCost    float64 `json:"total_cost_usd"`
	RequestCount int64   `json:"request_count"`
}

type UsageLogRepository interface {
	RecordUsage(ctx context.Context, log UsageLog) error
	RecordUsageBatch(ctx context.Context, logs []UsageLog) error
	GetUserUsageSummary(ctx context.Context, userID uuid.UUID, year int, month time.Month) ([]FeatureUsage, error)
	GetMeetingUsage(ctx context.Context, meetingID uuid.UUID) ([]UsageLog, error)
}

type postgresUsageLogRepository struct{}

func NewUsageLogRepository() UsageLogRepository {
	return &postgresUsageLogRepository{}
}

func (r *postgresUsageLogRepository) RecordUsage(ctx context.Context, log UsageLog) error {
	metadataJSON, err := json.Marshal(log.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}

	_, err = DB.Exec(ctx, `
		INSERT INTO usage_logs (user_id, team_id, meeting_id, feature, provider, model, input_tokens, output_tokens, audio_seconds, cost_usd, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, log.UserID, log.TeamID, log.MeetingID, log.Feature, log.Provider, log.Model,
		log.InputTokens, log.OutputTokens, log.AudioSeconds, log.CostUSD, metadataJSON)
	if err != nil {
		return fmt.Errorf("insert usage log: %w", err)
	}
	return nil
}

func (r *postgresUsageLogRepository) RecordUsageBatch(ctx context.Context, logs []UsageLog) error {
	if len(logs) == 0 {
		return nil
	}

	tx, err := DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, log := range logs {
		metadataJSON, err := json.Marshal(log.Metadata)
		if err != nil {
			metadataJSON = []byte("{}")
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO usage_logs (user_id, team_id, meeting_id, feature, provider, model, input_tokens, output_tokens, audio_seconds, cost_usd, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, log.UserID, log.TeamID, log.MeetingID, log.Feature, log.Provider, log.Model,
			log.InputTokens, log.OutputTokens, log.AudioSeconds, log.CostUSD, metadataJSON)
		if err != nil {
			return fmt.Errorf("insert usage log in batch: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit usage batch: %w", err)
	}
	return nil
}

func (r *postgresUsageLogRepository) GetUserUsageSummary(ctx context.Context, userID uuid.UUID, year int, month time.Month) ([]FeatureUsage, error) {
	start := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	rows, err := DB.Query(ctx, `
		SELECT feature,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(audio_seconds), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COUNT(*)
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY feature
		ORDER BY feature
	`, userID, start, end)
	if err != nil {
		return nil, fmt.Errorf("query user usage summary: %w", err)
	}
	defer rows.Close()

	return scanFeatureUsageRows(rows)
}

func (r *postgresUsageLogRepository) GetMeetingUsage(ctx context.Context, meetingID uuid.UUID) ([]UsageLog, error) {
	rows, err := DB.Query(ctx, `
		SELECT id, user_id, team_id, meeting_id, feature, provider, model,
		       input_tokens, output_tokens, audio_seconds, cost_usd, metadata, created_at
		FROM usage_logs
		WHERE meeting_id = $1
		ORDER BY created_at
	`, meetingID)
	if err != nil {
		return nil, fmt.Errorf("query meeting usage: %w", err)
	}
	defer rows.Close()

	var logs []UsageLog
	for rows.Next() {
		var log UsageLog
		var metadataJSON []byte
		if err := rows.Scan(
			&log.ID, &log.UserID, &log.TeamID, &log.MeetingID,
			&log.Feature, &log.Provider, &log.Model,
			&log.InputTokens, &log.OutputTokens, &log.AudioSeconds,
			&log.CostUSD, &metadataJSON, &log.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan meeting usage row: %w", err)
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &log.Metadata)
		}
		logs = append(logs, log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meeting usage rows: %w", err)
	}
	return logs, nil
}

type featureUsageScanner interface {
	Next() bool
	Scan(dest ...interface{}) error
}

type featureUsageRowsWithErr interface {
	featureUsageScanner
	Err() error
}

func scanFeatureUsageRows(rows featureUsageRowsWithErr) ([]FeatureUsage, error) {
	var results []FeatureUsage
	for rows.Next() {
		var fu FeatureUsage
		if err := rows.Scan(
			&fu.Feature, &fu.TotalInput, &fu.TotalOutput,
			&fu.TotalAudio, &fu.TotalCost, &fu.RequestCount,
		); err != nil {
			return nil, fmt.Errorf("scan feature usage row: %w", err)
		}
		results = append(results, fu)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate feature usage rows: %w", err)
	}
	return results, nil
}
