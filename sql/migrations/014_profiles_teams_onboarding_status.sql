-- Migration 014 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Adds `profiles.teams_onboarding_status` — a TEXT enum-like column that
-- tracks where each user sits in the v1.1 team-detection onboarding
-- funnel. Allowed values per Phase 4 CONTEXT.md D-04:
--   pending             — initial state, user has not completed onboarding
--   completed           — detection ran, user saved teams (or already
--                         had a calendar connected pre-v1.1, see backfill)
--   skipped             — user explicitly dismissed the onboarding
--   empty_state_skipped — algorithm found zero teams, user accepted
--
-- Default is 'pending' (NOT NULL). The column is enforced by a CHECK
-- constraint named `profiles_teams_onboarding_status_check` so the four
-- allowed values are guarded at the DB layer (mirrors the migration 013
-- TEXT+CHECK pattern instead of a Postgres ENUM type).
--
-- Inline backfill (atomic with the ALTER inside BEGIN/COMMIT): every
-- profile that already has `is_calendar_connected = TRUE` is moved to
-- 'completed' so a re-auth, page refresh, or token refresh on a
-- pre-existing connected user does NOT retroactively trigger the
-- onboarding flow (structural prevention for pitfall P10). Users with
-- `is_calendar_connected = FALSE` (or NULL) keep the default 'pending'
-- and will go through onboarding the first time they connect.
--
-- Idempotent:
--   * `ADD COLUMN IF NOT EXISTS` is safe on re-run.
--   * `DROP CONSTRAINT IF EXISTS` + `ADD CONSTRAINT` re-creates the CHECK
--     cleanly so the migration tolerates an existing partial state.
--   * The backfill UPDATE includes `AND teams_onboarding_status = 'pending'`
--     — re-running the migration will NOT regress users who have
--     legitimately advanced to 'skipped' or 'empty_state_skipped' back
--     to 'completed'.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

ALTER TABLE public.profiles
    ADD COLUMN IF NOT EXISTS teams_onboarding_status TEXT NOT NULL DEFAULT 'pending';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'profiles'
          AND constraint_name = 'profiles_teams_onboarding_status_check'
    ) THEN
        ALTER TABLE public.profiles
            DROP CONSTRAINT profiles_teams_onboarding_status_check;
    END IF;

    ALTER TABLE public.profiles
        ADD CONSTRAINT profiles_teams_onboarding_status_check
        CHECK (
            teams_onboarding_status IN (
                'pending',
                'completed',
                'skipped',
                'empty_state_skipped'
            )
        );
END $$;

-- Backfill: pre-existing connected users land directly on 'completed' so
-- re-auth, page refresh, or token refresh cannot trigger onboarding.
-- Idempotent: only touches rows still at the default 'pending'.
UPDATE public.profiles
SET teams_onboarding_status = 'completed'
WHERE is_calendar_connected = TRUE
  AND teams_onboarding_status = 'pending';

COMMIT;
