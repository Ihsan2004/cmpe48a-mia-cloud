-- ============================================================
-- AI Chief of Staff — MVP Schema
-- Run this in a fresh Supabase project's SQL Editor
-- Date: 2026-03-29
-- ============================================================

-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS vector;

-- ============================================================
-- 1. USERS
-- ============================================================
CREATE TABLE public.users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '',
    email_verified BOOLEAN DEFAULT false,
    full_name TEXT,
    avatar_url TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    is_active BOOLEAN DEFAULT true,
    failed_login_attempts INTEGER DEFAULT 0,
    locked_until TIMESTAMPTZ
);

CREATE INDEX idx_users_email ON public.users (email);
CREATE INDEX idx_users_is_active ON public.users (is_active) WHERE is_active = true;

-- Auto-update updated_at
CREATE OR REPLACE FUNCTION public.update_users_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER update_users_updated_at
    BEFORE UPDATE ON public.users
    FOR EACH ROW EXECUTE FUNCTION update_users_updated_at();

-- ============================================================
-- 2. PROFILES
-- ============================================================
CREATE TABLE public.profiles (
    id UUID PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    full_name TEXT,
    email TEXT UNIQUE,
    job_title TEXT,
    company TEXT,
    timezone TEXT DEFAULT 'UTC',
    default_meeting_duration INTEGER DEFAULT 30,
    work_start_hour TEXT,
    work_end_hour TEXT,
    bot_name TEXT DEFAULT 'MIA Assistant',
    bot_language TEXT DEFAULT 'tr',
    google_refresh_token TEXT,
    is_calendar_connected BOOLEAN DEFAULT false,
    google_email TEXT,
    auto_share_meetings BOOLEAN DEFAULT false,
    interface_language TEXT DEFAULT 'tr',
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_profiles_google_refresh_token ON public.profiles (google_refresh_token) WHERE google_refresh_token IS NOT NULL;
CREATE INDEX idx_profiles_calendar_connected ON public.profiles (is_calendar_connected) WHERE is_calendar_connected = true;
CREATE INDEX idx_profiles_google_email ON public.profiles (google_email) WHERE google_email IS NOT NULL;

-- Auto-update is_calendar_connected when refresh token changes
CREATE OR REPLACE FUNCTION public.update_calendar_connection_status()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.google_refresh_token IS NOT NULL AND NEW.google_refresh_token != '' THEN
        NEW.is_calendar_connected = true;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_refresh_token_change
    BEFORE UPDATE OF google_refresh_token ON public.profiles
    FOR EACH ROW EXECUTE FUNCTION update_calendar_connection_status();

-- Auto-create profile when user is created
CREATE OR REPLACE FUNCTION public.handle_new_user()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO public.profiles (id, full_name, email)
    VALUES (NEW.id, NEW.full_name, NEW.email)
    ON CONFLICT (id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER on_user_created
    AFTER INSERT ON public.users
    FOR EACH ROW EXECUTE FUNCTION handle_new_user();

-- ============================================================
-- 3. AUTH TOKENS
-- ============================================================
CREATE TABLE public.password_reset_tokens (
    token TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_password_reset_tokens_user_id ON public.password_reset_tokens (user_id);

CREATE TABLE public.refresh_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    token TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked BOOLEAN DEFAULT false,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_refresh_tokens_user_id ON public.refresh_tokens (user_id);
CREATE INDEX idx_refresh_tokens_token ON public.refresh_tokens (token);
CREATE INDEX idx_refresh_tokens_expires_at ON public.refresh_tokens (expires_at);
CREATE INDEX idx_refresh_tokens_revoked ON public.refresh_tokens (revoked) WHERE revoked = false;

-- ============================================================
-- 4. MEETINGS
-- ============================================================
CREATE TABLE public.meetings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NOT NULL,
    meeting_url TEXT,
    google_event_id TEXT NOT NULL,
    description TEXT,
    location TEXT,
    attendees JSONB DEFAULT '[]'::jsonb,
    organizer_email TEXT,
    auto_bot_enabled BOOLEAN DEFAULT true,
    bot_status TEXT,
    bot_joined_at TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    agenda_items TEXT,
    expected_outcomes TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_meetings_user_id ON public.meetings (user_id);
CREATE INDEX idx_meetings_start_time ON public.meetings (start_time);
CREATE INDEX idx_meetings_google_event_id ON public.meetings (google_event_id);
CREATE INDEX idx_meetings_meeting_url ON public.meetings (meeting_url) WHERE meeting_url IS NOT NULL;
CREATE INDEX idx_meetings_auto_bot_enabled ON public.meetings (auto_bot_enabled) WHERE auto_bot_enabled = true;
CREATE INDEX idx_meetings_bot_status ON public.meetings (bot_status) WHERE bot_status IS NOT NULL;
CREATE INDEX idx_meetings_bot_watchdog ON public.meetings (bot_status, last_heartbeat_at);

-- ============================================================
-- 5. TRANSCRIPT SEGMENTS (raw STT output)
-- ============================================================
CREATE TABLE public.transcript_segments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID REFERENCES public.meetings(id) ON DELETE SET NULL,
    meeting_url TEXT,
    text TEXT NOT NULL,
    start_time DOUBLE PRECISION NOT NULL,
    end_time DOUBLE PRECISION NOT NULL,
    duration DOUBLE PRECISION GENERATED ALWAYS AS (end_time - start_time) STORED,
    speaker TEXT,
    role TEXT NOT NULL,
    stt_provider TEXT,
    metadata JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ DEFAULT now(),
    idempotency_key TEXT UNIQUE
);

CREATE INDEX idx_transcript_segments_meeting_id ON public.transcript_segments (meeting_id);
CREATE INDEX idx_transcript_segments_start_time ON public.transcript_segments (start_time);
CREATE INDEX idx_transcript_segments_created_at ON public.transcript_segments (created_at DESC);
CREATE INDEX idx_transcript_segments_speaker ON public.transcript_segments (speaker) WHERE speaker IS NOT NULL;
CREATE INDEX idx_transcript_segments_idempotency ON public.transcript_segments (idempotency_key) WHERE idempotency_key IS NOT NULL;

-- ============================================================
-- 6. TRANSCRIPT CHAT BLOCKS (corrected + aggregated)
-- ============================================================
CREATE TABLE public.transcript_chat_blocks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    speaker TEXT NOT NULL,
    text TEXT NOT NULL,
    start_time DOUBLE PRECISION NOT NULL,
    end_time DOUBLE PRECISION NOT NULL,
    raw_segment_ids JSONB,
    is_noise BOOLEAN DEFAULT false,
    embedding vector(1536),
    embedding_model VARCHAR(50) DEFAULT 'text-embedding-3-small',
    embedding_generated_at TIMESTAMP,
    is_finalized BOOLEAN DEFAULT false,
    finalized_at TIMESTAMPTZ,
    source_segment_count INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_blocks_not_noise ON public.transcript_chat_blocks (meeting_id, is_noise) WHERE is_noise = false;
CREATE INDEX idx_chat_blocks_finalized ON public.transcript_chat_blocks (meeting_id, is_finalized, start_time);
CREATE INDEX idx_chat_blocks_meeting_start ON public.transcript_chat_blocks (meeting_id, start_time);
CREATE INDEX idx_chat_blocks_embedding ON public.transcript_chat_blocks USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX idx_chat_blocks_embedding_status ON public.transcript_chat_blocks (embedding_generated_at) WHERE embedding IS NOT NULL;

-- ============================================================
-- 7. MEETING SUMMARIES
-- ============================================================
CREATE TABLE public.meeting_summaries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    content TEXT,
    summary_type TEXT DEFAULT 'final_detailed' CHECK (summary_type IN ('interim', 'final_detailed', 'final_brief')),
    processing_status TEXT DEFAULT 'completed' CHECK (processing_status IN ('pending', 'processing', 'completed', 'completed_with_gaps', 'failed')),
    retry_count INTEGER DEFAULT 0,
    coverage_ratio DOUBLE PRECISION,
    successful_chunks INTEGER,
    failed_chunks INTEGER,
    summary_source TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_meeting_summaries_meeting_id ON public.meeting_summaries (meeting_id);
CREATE INDEX idx_meeting_summaries_status ON public.meeting_summaries (meeting_id, summary_type, processing_status);
CREATE UNIQUE INDEX idx_meeting_summaries_unique_type ON public.meeting_summaries (meeting_id, summary_type);
CREATE INDEX idx_meeting_summaries_source ON public.meeting_summaries (summary_source, created_at DESC) WHERE summary_type = 'final_detailed';

-- ============================================================
-- 8. CALENDAR SUBSCRIPTIONS
-- ============================================================
CREATE TABLE public.calendar_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL UNIQUE,
    resource_id TEXT NOT NULL,
    expiration TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_calendar_subscriptions_user_id ON public.calendar_subscriptions (user_id);
CREATE INDEX idx_calendar_subscriptions_channel_id ON public.calendar_subscriptions (channel_id);
CREATE INDEX idx_calendar_subscriptions_expiration ON public.calendar_subscriptions (expiration);

CREATE OR REPLACE FUNCTION public.update_calendar_subscriptions_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER calendar_subscriptions_updated_at
    BEFORE UPDATE ON public.calendar_subscriptions
    FOR EACH ROW EXECUTE FUNCTION update_calendar_subscriptions_updated_at();

-- ============================================================
-- 9. BACKGROUND JOBS
-- ============================================================
CREATE TABLE public.background_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed', 'dead')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    locked_by TEXT,
    locked_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_background_jobs_pending ON public.background_jobs (status, scheduled_at, created_at) WHERE status = 'pending';
CREATE INDEX idx_background_jobs_type_status ON public.background_jobs (job_type, status, created_at DESC);

CREATE TABLE public.background_jobs_dlq (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    original_job_id UUID REFERENCES public.background_jobs(id) ON DELETE SET NULL,
    job_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    failed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_background_jobs_dlq_failed_at ON public.background_jobs_dlq (failed_at DESC);

-- ============================================================
-- 10. CORRECTION DICTIONARY
-- ============================================================
CREATE TABLE public.correction_dictionary (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wrong_form TEXT NOT NULL,
    correct_form TEXT NOT NULL,
    description TEXT,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_correction_dictionary_active ON public.correction_dictionary (is_active) WHERE is_active = true;

-- Seed common domain terms
INSERT INTO public.correction_dictionary (wrong_form, correct_form, description) VALUES
    ('miya', 'MIA', 'Ürün adı'),
    ('mia', 'MIA', 'Ürün adı'),
    ('pre miting', 'pre-meeting', 'Toplantı öncesi hazırlık'),
    ('pre meeting', 'pre-meeting', 'Toplantı öncesi hazırlık'),
    ('premiting', 'pre-meeting', 'Toplantı öncesi hazırlık'),
    ('post miting', 'post-meeting', 'Toplantı sonrası'),
    ('pos meeting', 'post-meeting', 'Toplantı sonrası'),
    ('aksiyon aytem', 'action item', 'Görev/yapılacak iş'),
    ('aksiyon aytım', 'action item', 'Görev/yapılacak iş'),
    ('elevenlebs', 'ElevenLabs', 'Ses teknolojisi sağlayıcısı'),
    ('eleven labs', 'ElevenLabs', 'Ses teknolojisi sağlayıcısı'),
    ('open ei ay', 'OpenAI', 'AI sağlayıcısı'),
    ('open ay ay', 'OpenAI', 'AI sağlayıcısı'),
    ('çet cipi ti', 'ChatGPT', 'OpenAI chatbot'),
    ('cipi ti', 'GPT', 'AI model ailesi'),
    ('rag', 'RAG', 'Retrieval Augmented Generation'),
    ('supabase', 'Supabase', 'Veritabanı platformu'),
    ('süperbeys', 'Supabase', 'Veritabanı platformu'),
    ('speach tu tekst', 'speech-to-text', 'Konuşmadan metne çevirme'),
    ('tekst tu speach', 'text-to-speech', 'Metinden konuşmaya çevirme'),
    ('STT', 'STT', 'Speech-to-Text kısaltması'),
    ('TTS', 'TTS', 'Text-to-Speech kısaltması')
ON CONFLICT DO NOTHING;

-- ============================================================
-- 11. USAGE LOGS
-- ============================================================
CREATE TABLE public.usage_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    team_id UUID,
    meeting_id UUID,
    feature TEXT NOT NULL,
    provider TEXT NOT NULL,
    model TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    audio_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    metadata JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_usage_logs_user_id ON public.usage_logs (user_id);
CREATE INDEX idx_usage_logs_feature ON public.usage_logs (feature);
CREATE INDEX idx_usage_logs_created_at ON public.usage_logs (created_at);
CREATE INDEX idx_usage_logs_meeting_id ON public.usage_logs (meeting_id);
CREATE INDEX idx_usage_logs_user_month ON public.usage_logs (user_id, created_at);
