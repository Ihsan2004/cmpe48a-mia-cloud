-- Migration 013 — Phase 2.5 B8
--
-- `meetings.detected_app` records which app the recorder's ad-hoc
-- fusion identified as the meeting source. Populated when the entry
-- trigger was the desktop fusion engine ("we saw Zoom go in_meeting=true
-- with mic active"), as opposed to the calendar-armed flow (which
-- already routes via meetings.id directly).
--
-- Allowed values per RECORDER_ROADMAP §2.5:
--   zoom | google_meet | microsoft_teams | webex | slack_huddle |
--   discord | facetime | other | NULL
--
-- NULL stays the default — calendar-armed and bot recordings don't
-- populate this column. CHECK constraint guards against typos
-- silently saving as freetext.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS + DROP/ADD CHECK pattern.
-- Safe to re-run.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

ALTER TABLE public.meetings
    ADD COLUMN IF NOT EXISTS detected_app TEXT;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'meetings'
          AND constraint_name = 'meetings_detected_app_check'
    ) THEN
        ALTER TABLE public.meetings
            DROP CONSTRAINT meetings_detected_app_check;
    END IF;

    ALTER TABLE public.meetings
        ADD CONSTRAINT meetings_detected_app_check
        CHECK (
            detected_app IS NULL
            OR detected_app IN (
                'zoom',
                'google_meet',
                'microsoft_teams',
                'webex',
                'slack_huddle',
                'discord',
                'facetime',
                'other'
            )
        );
END $$;

COMMIT;
