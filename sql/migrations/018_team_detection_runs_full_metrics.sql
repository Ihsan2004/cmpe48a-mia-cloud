-- Migration 018 — Phase 6 v1.1 gap closure (OBS-01 full coverage)
--
-- Extends public.team_detection_runs with four additional metric
-- columns to satisfy ROADMAP Phase 6 success criterion #4 literally:
-- "input metrics (meeting/attendee/edge count), output metrics
-- (team/member/largest-team count), per-stage duration breakdown".
--
-- This REVERSES Phase 4 migration 017's deferral comment ("There are
-- no per-stage timing columns ... explicitly deferred"). The
-- verifier (gsd-verifier 2026-05-11) flagged the row as missing
-- input_edge_count, output_member_count, output_largest_team_size,
-- and per-stage durations on a literal reading of the must-have.
-- The user chose the full schema-extension resolution over the
-- alternative (amend ROADMAP wording to point at Prometheus).
--
-- New columns (all NULL-able; existing rows are not backfilled —
-- they predate this column set and remain valid as historical
-- run records with NULL in the new columns):
--   input_edge_count          INTEGER NULL  — g.Edges().Len() after BuildGraph
--   output_member_count       INTEGER NULL  — distinct emails union across all DetectedTeam.Members
--   output_largest_team_size  INTEGER NULL  — max(len(team.Members)) across the returned slice
--   stage_durations_jsonb     JSONB NULL    — {"fetch":<sec>,"graph_build":<sec>,"cluster":<sec>,"score":<sec>}
--                                            — keys verbatim match metrics.Stage* constants;
--                                              persist stage NOT included (self-referential — see service.go doc)
--
-- Idempotent: ADD COLUMN IF NOT EXISTS pattern. Safe to re-run.
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.
-- No retention/cleanup logic — see migration 017 D-10 rationale.

BEGIN;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS input_edge_count INTEGER NULL;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS output_member_count INTEGER NULL;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS output_largest_team_size INTEGER NULL;

ALTER TABLE public.team_detection_runs
    ADD COLUMN IF NOT EXISTS stage_durations_jsonb JSONB NULL;

COMMIT;
