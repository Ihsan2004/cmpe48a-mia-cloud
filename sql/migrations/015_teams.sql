-- Migration 015 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Creates `public.teams` — the user-scoped team table that holds the
-- output of the v1.1 onboarding team-detection flow (and any manual
-- create/edit/merge operations the user performs from the edit page).
--
-- Schema contract for v1.1 (Phase 4 CONTEXT.md D-01/D-02 + REQUIREMENTS
-- TEAMS-01):
--   id          UUID PK, server-generated
--   user_id     UUID FK → profiles(id) ON DELETE CASCADE — owner scope
--   name        TEXT NOT NULL — display label, user-editable
--   created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--   updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--
-- UNIQUE (user_id, name) — same user cannot have two teams sharing a
-- name; different users may legitimately reuse names. Enforced at the
-- DB layer with a NAMED constraint (`teams_user_id_name_unique`) so
-- repository unit tests in plan 04-04 can detect the violation by
-- constraint name and translate it deterministically (e.g., to a 409).
--
-- ON DELETE CASCADE on user_id — deleting a profile hard-deletes the
-- user's teams (which then cascades into team_members via migration
-- 016). This realises PRIVACY-03's "delete teams ⇒ delete members"
-- spirit at the FK layer.
--
-- `gen_random_uuid()` is available in Supabase by default (pgcrypto
-- preloaded), so no `CREATE EXTENSION` line is needed — matches the
-- prevailing project pattern.
--
-- Updated_at is set explicitly by the repository layer (no DB trigger)
-- — matches existing pattern in `meetings`, `meeting_summaries`.
--
-- Idempotent: `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`.
-- Re-running the migration on a database that already has the table is
-- a no-op.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

CREATE TABLE IF NOT EXISTS public.teams (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT teams_user_id_name_unique UNIQUE (user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_teams_user_id ON public.teams (user_id);

COMMIT;
