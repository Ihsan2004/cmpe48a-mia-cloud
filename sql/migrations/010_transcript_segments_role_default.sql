-- Migration 010 — Make transcript_segments.role optional with default ''.
--
-- The `role` column is a holdover from the meeting-bot transcript path,
-- where each line was tagged "host"/"guest"/etc. The Phase 8 batch path
-- (ElevenLabs Scribe) writes word-level segments via
-- TranscriptRepository.InsertSegments which doesn't carry a role — and
-- Scribe doesn't expose speaker tagging on the single-channel codepath
-- we use. Without this migration the INSERT fails with
-- `null value in column "role" of relation "transcript_segments"
-- violates not-null constraint (SQLSTATE 23502)`, leaving every
-- transcribed meeting stuck at transcription_status=processing with
-- zero segments.
--
-- The same ALTER was already executed against the production Supabase
-- DB during the Phase 1 E2E acceptance pass; this file commits it to
-- the repo so dev / CI / future re-deploys converge on the same schema.
--
-- Idempotent — safe to re-run; ALTER COLUMN is a no-op when the column
-- already matches the requested shape.

BEGIN;

ALTER TABLE public.transcript_segments
    ALTER COLUMN role SET DEFAULT '',
    ALTER COLUMN role DROP NOT NULL;

COMMIT;
