-- Migration: Create usage_logs table
-- Date: 2026-03-17
-- Description:
--   Token and usage tracking infrastructure for AI features.
--   Stores per-request usage data (tokens, audio seconds, cost) for
--   billing, quotas, and analytics. Supports OpenAI, ElevenLabs, and Perplexity providers.

CREATE TABLE IF NOT EXISTS public.usage_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL,
    team_id         UUID,
    meeting_id      UUID,
    feature         TEXT NOT NULL,
    provider        TEXT NOT NULL,
    model           TEXT,
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    audio_seconds   DOUBLE PRECISION NOT NULL DEFAULT 0,
    cost_usd        DOUBLE PRECISION NOT NULL DEFAULT 0,
    metadata        JSONB DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.usage_logs.feature IS 'summarize, stt, premeeting_chat, embedding, guest_intel, catch_me_up, semantic_search, who_said, tts, transcript_correction, risk_evaluation';
COMMENT ON COLUMN public.usage_logs.provider IS 'openai, elevenlabs, perplexity';
COMMENT ON COLUMN public.usage_logs.audio_seconds IS 'STT/TTS usage in seconds; 0 for token-based features';
COMMENT ON COLUMN public.usage_logs.cost_usd IS 'Calculated cost based on provider pricing at time of request';
COMMENT ON COLUMN public.usage_logs.metadata IS 'Extra context: chunk_count, segment_count, reconnect_count, character_count, etc.';

CREATE INDEX IF NOT EXISTS idx_usage_logs_user_id
    ON public.usage_logs (user_id);

CREATE INDEX IF NOT EXISTS idx_usage_logs_feature
    ON public.usage_logs (feature);

CREATE INDEX IF NOT EXISTS idx_usage_logs_created_at
    ON public.usage_logs (created_at);

CREATE INDEX IF NOT EXISTS idx_usage_logs_meeting_id
    ON public.usage_logs (meeting_id);

-- Composite index for monthly user usage queries (WHERE user_id = $1 AND created_at >= $2 AND created_at < $3)
CREATE INDEX IF NOT EXISTS idx_usage_logs_user_month
    ON public.usage_logs (user_id, created_at);
