-- Migration 017 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Creates `public.team_detection_runs` — the run-reproducibility log
-- chosen as the structural prevention for pitfall P12 ("no production
-- reproducibility"). The table lands on day one even though the v1.1
-- algorithm only writes 1–2 rows per user lifetime (first-connect-only
-- trigger). Replay/observability tooling that reads the rows is
-- explicitly deferred to v1.2 (RESOLVED TENSION D — runs IN, replay OUT).
--
-- Schema (twelve columns, lean reproducibility set per Phase 4
-- CONTEXT.md D-08, in this exact order):
--   id                    UUID PK, server-generated
--   user_id               UUID FK → profiles(id) ON DELETE CASCADE
--   started_at            TIMESTAMPTZ NOT NULL — set when run begins
--   finished_at           TIMESTAMPTZ NULL    — NULL while running
--   status                TEXT NOT NULL       — see CHECK below
--   input_meeting_count   INTEGER NULL        — fetcher window summary
--   input_attendee_count  INTEGER NULL        — distinct attendees
--   output_team_count     INTEGER NULL        — clusters produced
--   parameters_jsonb      JSONB NULL          — algorithm parameter
--                                               snapshot owned by Phase 6
--                                               (k, weight-decay, cutoffs);
--                                               opaque to Phase 4
--   algorithm_version     TEXT NULL           — short tag like "cpm-k3-v1";
--                                               value space owned by Phase 6
--   error_message         TEXT NULL
--   created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
--
-- Status lifecycle (D-08):
--   running     — row inserted at run start, finished_at still NULL
--   success     — algorithm produced ≥1 team, output_team_count > 0
--   degenerate  — algorithm completed but produced 0 teams
--                 (powers the empty-state UX)
--   error       — algorithm crashed, error_message populated
--
-- Enforced via NAMED constraint `team_detection_runs_status_check`
-- using the DROP/ADD pattern (mirrors migration 013) so the value list
-- can expand cleanly in a future migration.
--
-- Index `idx_team_detection_runs_user_started` on (user_id,
-- started_at DESC) supports Phase 7's "latest run for user" lookup
-- (trust bundle DF-1).
--
-- No retention/cleanup logic in v1.1 (D-10): first-connect-only trigger
-- means at most 1–2 rows per user; volume is trivial. Pruning is
-- deferred until/unless replay tooling lands.
--
-- Phase 4 only lands the schema. Phase 6 owns the values that go into
-- `parameters_jsonb` and `algorithm_version` — Phase 4 treats them as
-- opaque blobs. There is intentionally no `version` column for the
-- JSONB blob (versioning belongs inside the JSON payload if Phase 6
-- needs it). There are no per-stage timing columns (fetch_ms, etc.) —
-- explicitly deferred. There is no `confidence` column — DF-2 is a
-- Phase 7 P1.5 ship-if-capacity item; if it ships, Phase 7 adds the
-- column then.
--
-- Idempotent: `CREATE TABLE IF NOT EXISTS` + DROP/ADD CHECK pattern +
-- `CREATE INDEX IF NOT EXISTS`.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

CREATE TABLE IF NOT EXISTS public.team_detection_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NULL,
    status TEXT NOT NULL,
    input_meeting_count INTEGER NULL,
    input_attendee_count INTEGER NULL,
    output_team_count INTEGER NULL,
    parameters_jsonb JSONB NULL,
    algorithm_version TEXT NULL,
    error_message TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'team_detection_runs'
          AND constraint_name = 'team_detection_runs_status_check'
    ) THEN
        ALTER TABLE public.team_detection_runs
            DROP CONSTRAINT team_detection_runs_status_check;
    END IF;

    ALTER TABLE public.team_detection_runs
        ADD CONSTRAINT team_detection_runs_status_check
        CHECK (
            status IN ('running', 'success', 'degenerate', 'error')
        );
END $$;

CREATE INDEX IF NOT EXISTS idx_team_detection_runs_user_started
    ON public.team_detection_runs (user_id, started_at DESC);

COMMIT;
