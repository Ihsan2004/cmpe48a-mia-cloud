-- Migration 011 — Phase 2.4 B5
--
-- Extend `meetings.recording_method_override` to accept `'skip'` in
-- addition to the existing `'bot' | 'desktop' | NULL` set.
--
-- Per RECORDER_ROADMAP §2.4 B5, the resolution order is:
--     meeting.recording_method_override
--   ?? profile.recording_method
--   ?? 'bot'
-- and `skip` is a meeting-level only escape hatch — there is intentionally
-- no `'skip'` value at the profile level (a user shouldn't accidentally
-- silence every meeting). The CHECK constraint on `profiles.recording_method`
-- is therefore left as `('bot', 'desktop')`.
--
-- Migration is idempotent: drops the old constraint if it exists and
-- recreates it with the wider allowed set. Safe to re-run.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'meetings'
          AND constraint_name = 'meetings_recording_method_override_check'
    ) THEN
        ALTER TABLE public.meetings
            DROP CONSTRAINT meetings_recording_method_override_check;
    END IF;

    ALTER TABLE public.meetings
        ADD CONSTRAINT meetings_recording_method_override_check
        CHECK (
            recording_method_override IS NULL
            OR recording_method_override IN ('bot', 'desktop', 'skip')
        );
END $$;

COMMIT;
