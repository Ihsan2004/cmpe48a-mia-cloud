-- Migration 016 — Phase 4 v1.1 (Team Detection & Onboarding)
--
-- Creates `public.team_members` — the per-team membership table that
-- holds the strictly-minimal PII set chosen for v1.1 (REQUIREMENTS
-- TEAMS-02 + PRIVACY-04 + research pitfall P4 — KVKK / shadow contact
-- DB).
--
-- Schema contract (seven columns, no others):
--   id          UUID PK, server-generated
--   team_id     UUID FK → teams(id) ON DELETE CASCADE
--   name        TEXT NOT NULL — display name (from calendar attendee)
--   title       TEXT NULL    — user-typed on edit page (EDIT-08 says
--                              "field is optional and empty by default")
--   email       TEXT NOT NULL — deduplication key
--   created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--   updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
--
-- UNIQUE (team_id, email) — the same email may legitimately appear
-- across different teams of the same user (DETECT-01 allows
-- overlapping team membership), but never twice within one team.
-- Enforced via the NAMED constraint `team_members_team_id_email_unique`
-- so repository tests can detect the violation deterministically by
-- constraint name.
--
-- ON DELETE CASCADE on team_id realises PRIVACY-03 ("deleting a team
-- hard-deletes its members") at the database layer. Repository tests
-- in plan 04-04 verify this contract.
--
-- Privacy contract (PRIVACY-04) — PERMANENT, NOT A DEFERRAL:
--   Phone, photo, manager-of, LinkedIn-style fields are explicitly
--   out of scope. They cannot leak because they cannot be stored.
--   Adding any of these requires a milestone-level decision, not a
--   future migration follow-up. This schema is a hard ceiling, not
--   a starting point.
--
-- Idempotent: `CREATE TABLE IF NOT EXISTS` + `CREATE INDEX IF NOT EXISTS`.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

CREATE TABLE IF NOT EXISTS public.team_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES public.teams(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    title TEXT NULL,
    email TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT team_members_team_id_email_unique UNIQUE (team_id, email)
);

CREATE INDEX IF NOT EXISTS idx_team_members_team_id ON public.team_members (team_id);

COMMIT;
