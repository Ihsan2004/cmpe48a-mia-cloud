-- Migration 009 — Drop partial unique index, recreate as non-partial.
--
-- Migration 008 created a PARTIAL unique index on
-- meetings (user_id, google_event_id) WHERE google_event_id IS NOT NULL
-- AND google_event_id <> ''. PostgreSQL requires the inference predicate
-- on `INSERT ... ON CONFLICT (cols)` to match the partial index's WHERE
-- clause exactly; since the Go upsert in MeetingRepository.UpsertDesktopMeeting
-- uses the bare `ON CONFLICT (user_id, google_event_id)` form, every call
-- aborts with `there is no unique or exclusion constraint matching the
-- ON CONFLICT specification (SQLSTATE 42P10)`, manifesting in production
-- as `POST /api/meetings/desktop -> 500 Toplantı kaydedilemedi`.
--
-- The partial filter from 008 was defensive — `meetings.google_event_id`
-- is `TEXT NOT NULL` in the schema (sql/001_mvp_schema.sql:138), so the
-- predicate cannot exclude any row. Replacing the index with a plain
-- (non-partial) unique index makes the existing ON CONFLICT clause
-- match without code changes.
--
-- Idempotent: DROP IF EXISTS + CREATE IF NOT EXISTS on a different name
-- (so re-running this migration after an interrupted run still converges).

BEGIN;

DROP INDEX IF EXISTS public.meetings_user_google_event_unique;

CREATE UNIQUE INDEX IF NOT EXISTS meetings_user_google_event_unique
    ON public.meetings (user_id, google_event_id);

COMMIT;
