package repository

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var DB *pgxpool.Pool // Main database (meetings, profile tables only)

func Connect() error {
	// Connect to main database (meetings, profile)
	// Python service handles transcript_segments from its own database (PROJECT 2)
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL environment variable is not set")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("failed to parse database config: %w", err)
	}

	maxConns := getEnvInt("DB_MAX_CONNS", 2)
	minConns := getEnvInt("DB_MIN_CONNS", 0)
	config.MaxConns = int32(maxConns)
	config.MinConns = int32(minConns)
	config.MaxConnIdleTime = 2 * time.Minute
	config.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	DB = pool
	slog.Info("Connected to main database (meetings, profile)",
		"max_conns", config.MaxConns,
		"min_conns", config.MinConns,
	)
	return nil
}

func getEnvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		slog.Warn("Invalid integer env var, using fallback",
			"key", key,
			"value", raw,
			"fallback", fallback,
		)
		return fallback
	}

	return value
}

func Close() {
	if DB != nil {
		DB.Close()
	}
}
