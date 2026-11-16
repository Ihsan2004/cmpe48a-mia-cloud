-- Migration 005: batch transcription pipeline
-- Supports: ElevenLabs Scribe v2 batch API, Supabase Storage audio lifecycle,
-- per-meeting recording method override, resumable uploads.
--
-- All statements are idempotent (IF NOT EXISTS / ADD COLUMN IF NOT EXISTS / CREATE OR REPLACE).

-- ============================================================
-- 1) meetings — audio + transcription lifecycle columns
-- ============================================================

ALTER TABLE public.meetings
    ADD COLUMN IF NOT EXISTS audio_storage_path TEXT,
    ADD COLUMN IF NOT EXISTS audio_content_type TEXT,
    ADD COLUMN IF NOT EXISTS audio_size_bytes BIGINT,
    ADD COLUMN IF NOT EXISTS audio_duration_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS audio_source TEXT,
    ADD COLUMN IF NOT EXISTS transcription_status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS transcription_provider_job_id TEXT,
    ADD COLUMN IF NOT EXISTS transcription_attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS transcription_last_error TEXT,
    ADD COLUMN IF NOT EXISTS transcription_completed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS audio_deleted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS recording_method_override TEXT;

-- Check constraints — DROP + ADD pattern (idempotent across re-runs)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'meetings' AND constraint_name = 'meetings_audio_source_check'
    ) THEN
        ALTER TABLE public.meetings
            ADD CONSTRAINT meetings_audio_source_check
            CHECK (audio_source IS NULL OR audio_source IN ('bot', 'desktop'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'meetings' AND constraint_name = 'meetings_transcription_status_check'
    ) THEN
        ALTER TABLE public.meetings
            ADD CONSTRAINT meetings_transcription_status_check
            CHECK (transcription_status IN ('pending', 'uploading', 'processing', 'completed', 'failed'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'meetings' AND constraint_name = 'meetings_recording_method_override_check'
    ) THEN
        ALTER TABLE public.meetings
            ADD CONSTRAINT meetings_recording_method_override_check
            CHECK (recording_method_override IS NULL OR recording_method_override IN ('bot', 'desktop'));
    END IF;
END $$;

-- Partial index: only active-state rows (keeps index small)
CREATE INDEX IF NOT EXISTS idx_meetings_transcription_status
    ON public.meetings (transcription_status)
    WHERE transcription_status IN ('pending', 'uploading', 'processing', 'failed');

-- Cleanup cron helper: find meetings whose audio can be deleted
CREATE INDEX IF NOT EXISTS idx_meetings_audio_cleanup
    ON public.meetings (transcription_status, transcription_completed_at)
    WHERE audio_storage_path IS NOT NULL AND audio_deleted_at IS NULL;

-- ============================================================
-- 2) profiles — recording_method preference
-- ============================================================

ALTER TABLE public.profiles
    ADD COLUMN IF NOT EXISTS recording_method TEXT NOT NULL DEFAULT 'bot';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'profiles' AND constraint_name = 'profiles_recording_method_check'
    ) THEN
        ALTER TABLE public.profiles
            ADD CONSTRAINT profiles_recording_method_check
            CHECK (recording_method IN ('bot', 'desktop'));
    END IF;
END $$;

-- ============================================================
-- 3) upload_sessions — resumable chunk upload state
-- ============================================================

CREATE TABLE IF NOT EXISTS public.upload_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES public.users(id),
    content_type TEXT NOT NULL,
    total_bytes BIGINT,
    received_bytes BIGINT NOT NULL DEFAULT 0,
    storage_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'in_progress',
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT upload_sessions_status_check
        CHECK (status IN ('in_progress', 'completed', 'aborted'))
);

CREATE INDEX IF NOT EXISTS idx_upload_sessions_meeting
    ON public.upload_sessions (meeting_id);

CREATE INDEX IF NOT EXISTS idx_upload_sessions_user_status
    ON public.upload_sessions (user_id, status)
    WHERE status = 'in_progress';

-- Keep updated_at fresh on every row touch
CREATE OR REPLACE FUNCTION public.upload_sessions_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS upload_sessions_updated_at ON public.upload_sessions;
CREATE TRIGGER upload_sessions_updated_at
    BEFORE UPDATE ON public.upload_sessions
    FOR EACH ROW
    EXECUTE FUNCTION public.upload_sessions_set_updated_at();

-- ============================================================
-- Done. Verify with:
--   SELECT column_name FROM information_schema.columns
--     WHERE table_name = 'meetings' AND column_name LIKE 'audio%';
--   SELECT column_name FROM information_schema.columns
--     WHERE table_name = 'profiles' AND column_name = 'recording_method';
--   SELECT * FROM information_schema.tables
--     WHERE table_name = 'upload_sessions';
-- ============================================================
