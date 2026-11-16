package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the application
type Config struct {
	DatabaseURL        string // For meetings, profile tables (Go backend only)
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURI  string
	FrontendURL        string
	Port               string
	CORSOrigins        string
	MiaSearchURL       string
	ExternalBotAPIURL  string
	OpenAIAPIKey       string
	Environment        string
	PerplexityAPIKey   string
	GuestIntelEnabled  bool

	// Cloud Functions brief notifier (optional HTTP serverless deliverer)
	BriefNotifierFunctionURL    string
	InternalServiceSharedSecret string

	// Supabase Storage (legacy batch transcription — optional, disabled when unset)
	SupabaseStorageURL     string // e.g. https://<project>.supabase.co/storage/v1
	SupabaseServiceRoleKey string // server-side only, bypasses RLS
	SupabaseAudioBucket    string // bucket name for meeting audio (default "meeting-audio")

	// ElevenLabs Scribe v2 batch STT (Phase 3 of batch migration)
	ElevenLabsAPIKey        string // server-side API key
	ElevenLabsWebhookID     string // webhook destination configured in ElevenLabs dashboard
	ElevenLabsWebhookSecret string // HMAC shared secret for verifying incoming webhooks
	ElevenLabsModelID       string // default "scribe_v2"
	ElevenLabsLanguage      string // default "tr"

	// Brief delivery — SMTP (Phase 2 of brief delivery roadmap).
	// When SMTPHost + BotMailAppPassword are both set, the email
	// deliverer is registered with the briefnotify dispatcher; when
	// either is empty the email channel stays dormant. Google Workspace
	// SMTP relay setup: SMTPHost=smtp-relay.gmail.com, SMTPPort=587,
	// BotMailAuthUser=admin@syncrun.net (full-hesap for SMTP auth),
	// BotMailFromAddress=brief@syncrun.net (alias on the From header),
	// BotMailAppPassword=Google App Password (16 char, bypasses 2FA).
	SMTPHost               string
	SMTPPort               int
	BotMailAuthUser        string // SMTP auth hesabı (ör. admin@syncrun.net)
	BotMailAppPassword     string // Google Workspace App Password
	BotMailFromAddress     string // From header (ör. brief@syncrun.net, admin hesabının alias'ı)
	BotMailFromDisplayName string // From display name (ör. "Syncrun")
	BotMailReplyTo         string // Reply-To (default = BotMailFromAddress)

	// Unsubscribe (Phase 3) — HMAC signing key for signed opt-out links
	// in mail footers. Phase 2 already renders the footer, so we load
	// the secret now to fail fast if the deploy forgot to set it.
	UnsubscribeSigningSecret string

	// Slack OAuth (Phase 4 of brief delivery roadmap).
	// Distinct from Google Calendar OAuth — separate client ID,
	// separate redirect URI (api.syncrun.net/api/integrations/slack/
	// callback), separate token storage. Empty values disable the
	// Slack integration endpoints entirely so half-wired deploys 404
	// instead of exploding at OAuth time.
	SlackClientID        string
	SlackClientSecret    string
	SlackSigningSecret   string // reserved for Phase 5+ interactive handlers
	SlackTokenEncKey     string // 32-byte hex AES-GCM key for bot_token_encrypted
	SlackRedirectURI     string // must match Slack app manifest redirect_urls exactly
	SlackFrontendCallback string // SPA lands here after install completes
}

// Load reads configuration from environment variables
func Load() (*Config, error) {
	// Load .env file if it exists (optional in production)
	_ = godotenv.Load()

	cfg := &Config{
		DatabaseURL:        os.Getenv("DATABASE_URL"), // Main database (meetings, profile only)
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURI:  getEnvOrDefault("GOOGLE_REDIRECT_URI", ""),
		FrontendURL:        getEnvOrDefault("FRONTEND_URL", "http://localhost:3000"),
		Port:               getEnvOrDefault("PORT", "8080"),
		CORSOrigins:        getEnvOrDefault("CORS_ORIGINS", "http://localhost:3000"),
		MiaSearchURL:       getEnvOrDefault("NLP_SERVICE_URL", getEnvOrDefault("MIA_SEARCH_URL", "http://mia-search:8000")),
		ExternalBotAPIURL:  os.Getenv("EXTERNAL_BOT_API_URL"),
		OpenAIAPIKey:       os.Getenv("OPENAI_API_KEY"),
		Environment:        getEnvOrDefault("APP_ENV", "development"),
		PerplexityAPIKey:   os.Getenv("PERPLEXITY_API_KEY"),
		GuestIntelEnabled:  getEnvOrDefault("GUEST_INTEL_ENABLED", "") == "true",

		BriefNotifierFunctionURL:    os.Getenv("BRIEF_NOTIFIER_FUNCTION_URL"),
		InternalServiceSharedSecret: os.Getenv("INTERNAL_SERVICE_SHARED_SECRET"),

		SupabaseStorageURL:     os.Getenv("SUPABASE_STORAGE_URL"),
		SupabaseServiceRoleKey: os.Getenv("SUPABASE_SERVICE_ROLE_KEY"),
		SupabaseAudioBucket:    getEnvOrDefault("SUPABASE_AUDIO_BUCKET", "meeting-audio"),

		ElevenLabsAPIKey:        os.Getenv("ELEVENLABS_API_KEY"),
		ElevenLabsWebhookID:     os.Getenv("ELEVENLABS_WEBHOOK_ID"),
		ElevenLabsWebhookSecret: os.Getenv("ELEVENLABS_WEBHOOK_SECRET"),
		ElevenLabsModelID:       getEnvOrDefault("ELEVENLABS_MODEL_ID", "scribe_v2"),
		ElevenLabsLanguage:      getEnvOrDefault("ELEVENLABS_LANGUAGE", "tr"),

		SMTPHost:                 getEnvOrDefault("SMTP_HOST", "smtp-relay.gmail.com"),
		SMTPPort:                 envIntOrDefault("SMTP_PORT", 587),
		BotMailAuthUser:          os.Getenv("BOT_MAIL_AUTH_USER"),
		BotMailAppPassword:       os.Getenv("BOT_MAIL_APP_PASSWORD"),
		BotMailFromAddress:       os.Getenv("BOT_MAIL_FROM"),
		BotMailFromDisplayName:   getEnvOrDefault("BOT_MAIL_FROM_DISPLAY_NAME", "Syncrun"),
		BotMailReplyTo:           os.Getenv("BOT_MAIL_REPLY_TO"),
		UnsubscribeSigningSecret: os.Getenv("UNSUBSCRIBE_SIGNING_SECRET"),

		SlackClientID:         os.Getenv("SLACK_CLIENT_ID"),
		SlackClientSecret:     os.Getenv("SLACK_CLIENT_SECRET"),
		SlackSigningSecret:    os.Getenv("SLACK_SIGNING_SECRET"),
		SlackTokenEncKey:      os.Getenv("SLACK_TOKEN_ENC_KEY"),
		SlackRedirectURI:      getEnvOrDefault("SLACK_REDIRECT_URI", "https://api.syncrun.net/api/integrations/slack/callback"),
		SlackFrontendCallback: getEnvOrDefault("FRONTEND_SLACK_CALLBACK", "https://console.syncrun.net/settings/notifications"),
	}

	// Reply-To defaults to the From address — avoids "noreply@" UX and
	// matches roadmap Karar #3.
	if cfg.BotMailReplyTo == "" {
		cfg.BotMailReplyTo = cfg.BotMailFromAddress
	}

	return cfg, nil
}

func envIntOrDefault(key string, defaultValue int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultValue
	}
	return n
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
