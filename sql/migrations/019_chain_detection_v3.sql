-- Migration 019: chain detection v3 — schema foundation (Phase 1)
-- Roadmap: docs/CHAIN_DETECTION_ROADMAP.md, Phase 1 Schema Foundation.
--
-- Adds the persistence contract for the v3 chain detection algorithm
-- without changing existing matcher behaviour. Splits into 4 logical
-- groups:
--
--   1) meeting_chains.user_id  — tenant-scoped chain ownership.
--      Today the table is global; the legacy FindBySeriesID(seriesID)
--      lookup can return another user's chain if both happen to share a
--      Google calendar_series_id. Backfill picks the earliest meeting's
--      user_id per chain; chains shared across users are split per user
--      with new ids and meetings repointed.
--
--   2) meetings.chain_assignment_*  — assignment provenance + manual
--      lock. `chain_assignment_method` records WHY a chain was assigned
--      (series_id / auto_score / llm_suggestion / decision_match /
--      manual_user / manual_removed). `manual_*` rows have a non-null
--      `chain_assignment_locked_at` and bypass the auto matcher so a
--      false positive doesn't keep getting re-attached.
--
--   3) chain_decisions_v2  — single-source-of-truth log for shadow-mode
--      decisions, pending LLM suggestions, and suppression counters.
--      State transitions UPDATE the existing row in place; UNIQUE
--      prevents duplicate attempts, not state mutation. Audit history
--      is out of scope for v1.
--
--   4) chain_decision_centroids  — Stage 3 cache (post-meeting decision
--      embeddings). Created here so Phase 5 doesn't need a separate
--      migration; centroid table stays empty until the post-meeting job
--      ships.
--
-- All statements idempotent. Backfill wrapped in a single transaction
-- with a pg_advisory_xact_lock so concurrent migration runs can't
-- double-split chains.

BEGIN;

SELECT pg_advisory_xact_lock(7400190);  -- arbitrary unique migration id

-- ============================================================
-- 1) meeting_chains.user_id + tenant unique index
-- ============================================================

ALTER TABLE public.meeting_chains
    ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES public.users(id) ON DELETE CASCADE;

-- ----- Backfill phase A: assign user_id to chains attached to a single user.
-- A chain whose meetings all belong to the same user can be claimed by
-- that user directly — pick any meeting (oldest is convenient for FK
-- stability since downstream chain_id references stay valid).

-- Postgres has no MIN()/MAX() aggregate for UUID — but HAVING COUNT(DISTINCT) = 1
-- already guarantees a single user, so we just pick the only element of the
-- DISTINCT-aggregated array.
UPDATE public.meeting_chains mc
SET user_id = sub.user_id
FROM (
    SELECT chain_id, (array_agg(DISTINCT user_id))[1] AS user_id
    FROM public.meetings
    WHERE chain_id IS NOT NULL
    GROUP BY chain_id
    HAVING COUNT(DISTINCT user_id) = 1
) sub
WHERE mc.id = sub.chain_id
  AND mc.user_id IS NULL;

-- ----- Backfill phase B: split chains shared across multiple users.
-- For each (chain_id, user_id) pair where the chain's meetings span >1
-- user, materialise a fresh chain row owned by that user, then repoint
-- the user's meetings to it. The original chain row keeps the FIRST
-- (oldest) user so we don't dangle anyone's old chain_id references in
-- decision_items / meeting_briefs / meeting_summaries / etc.

WITH multi_user_chains AS (
    SELECT chain_id
    FROM public.meetings
    WHERE chain_id IS NOT NULL
    GROUP BY chain_id
    HAVING COUNT(DISTINCT user_id) > 1
),
chain_users AS (
    -- All (chain, user) pairs from multi-user chains
    SELECT DISTINCT m.chain_id, m.user_id
    FROM public.meetings m
    JOIN multi_user_chains muc ON muc.chain_id = m.chain_id
),
chain_owner AS (
    -- The FIRST user (by oldest meeting start) keeps the original chain row.
    SELECT DISTINCT ON (m.chain_id) m.chain_id, m.user_id AS owner_user_id
    FROM public.meetings m
    JOIN multi_user_chains muc ON muc.chain_id = m.chain_id
    ORDER BY m.chain_id, m.start_time ASC
),
new_chain_map AS (
    -- For every NON-owner user of a multi-user chain, precompute the
    -- exact old_chain_id -> new_chain_id mapping. The previous shape only
    -- joined on user_id after INSERT ... RETURNING, which becomes
    -- nondeterministic if the same user is a non-owner in multiple shared
    -- chains.
    SELECT
        cu.chain_id AS old_chain_id,
        cu.user_id,
        gen_random_uuid() AS new_chain_id,
        mc.chain_type,
        mc.calendar_series_id,
        mc.participant_hash,
        mc.display_name
    FROM chain_users cu
    JOIN chain_owner co ON co.chain_id = cu.chain_id
    JOIN public.meeting_chains mc ON mc.id = cu.chain_id
    WHERE cu.user_id <> co.owner_user_id
),
inserted_chains AS (
    INSERT INTO public.meeting_chains
        (id, chain_type, calendar_series_id, participant_hash, display_name, user_id)
    SELECT
        new_chain_id,
        chain_type,
        calendar_series_id,
        participant_hash,
        display_name,
        user_id
    FROM new_chain_map
    RETURNING id
),
repoint_meetings AS (
    UPDATE public.meetings m
    SET chain_id = ncm.new_chain_id
    FROM new_chain_map ncm
    JOIN inserted_chains ic ON ic.id = ncm.new_chain_id
    WHERE m.chain_id = ncm.old_chain_id
      AND m.user_id  = ncm.user_id
    RETURNING m.id AS meeting_id, ncm.old_chain_id, ncm.new_chain_id
),
repoint_decision_items AS (
    UPDATE public.decision_items di
    SET chain_id = rm.new_chain_id
    FROM repoint_meetings rm
    WHERE di.meeting_id = rm.meeting_id
      AND di.chain_id = rm.old_chain_id
    RETURNING di.id
),
repoint_meeting_briefs AS (
    UPDATE public.meeting_briefs mb
    SET chain_id = rm.new_chain_id
    FROM repoint_meetings rm
    WHERE mb.target_meeting_id = rm.meeting_id
      AND mb.chain_id = rm.old_chain_id
    RETURNING mb.id
)
-- Finally, claim ownership of the original chain row for the owner user.
UPDATE public.meeting_chains mc
SET user_id = co.owner_user_id
FROM chain_owner co
WHERE mc.id = co.chain_id
  AND mc.user_id IS NULL;

-- ----- Backfill phase C: clean up truly-orphan chains (no meetings).
-- After Phase A (single-user) and Phase B (multi-user split), every
-- chain *attached to a meeting* has its user_id set. Anything still
-- NULL is an orphan chain row — no meeting references it, so it's
-- dead weight from earlier dev/test cycles or from cascade-deleted
-- meetings. Sever any dangling FK references from decision_items /
-- meeting_briefs (those are themselves inconsistent state, since
-- their chain has no meetings) then drop the orphan rows.
--
-- Done inside this migration so re-runs converge to a clean schema
-- without ops having to hand-write cleanup queries between failed
-- attempts. Wrapped in the same transaction so a referential issue
-- still rolls everything back cleanly.

UPDATE public.decision_items
SET chain_id = NULL
WHERE chain_id IN (
    SELECT mc.id
    FROM public.meeting_chains mc
    WHERE mc.user_id IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM public.meetings m WHERE m.chain_id = mc.id
      )
);

UPDATE public.meeting_briefs
SET chain_id = NULL
WHERE chain_id IN (
    SELECT mc.id
    FROM public.meeting_chains mc
    WHERE mc.user_id IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM public.meetings m WHERE m.chain_id = mc.id
      )
);

DELETE FROM public.meeting_chains mc
WHERE mc.user_id IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM public.meetings m WHERE m.chain_id = mc.id
  );

-- ----- Backfill phase D: collapse duplicate per-user recurring-series chains.
-- The new unique index below is the desired invariant, but legacy data may
-- contain duplicate chain rows for the same (user_id, calendar_series_id)
-- due to old non-unique lookups or retry races. Keep the chain attached to
-- the oldest meeting, repoint dependent rows, then delete the duplicate
-- shells before the unique index is created.

WITH ranked_series_chains AS (
    SELECT
        mc.id,
        FIRST_VALUE(mc.id) OVER (
            PARTITION BY mc.user_id, mc.calendar_series_id
            ORDER BY fm.first_start NULLS LAST, mc.created_at NULLS LAST, mc.id
        ) AS keep_id
    FROM public.meeting_chains mc
    LEFT JOIN LATERAL (
        SELECT MIN(m.start_time) AS first_start
        FROM public.meetings m
        WHERE m.chain_id = mc.id
    ) fm ON TRUE
    WHERE mc.calendar_series_id IS NOT NULL
),
dupes AS (
    SELECT id AS dup_id, keep_id
    FROM ranked_series_chains
    WHERE id <> keep_id
),
repoint_meetings AS (
    UPDATE public.meetings m
    SET chain_id = d.keep_id
    FROM dupes d
    WHERE m.chain_id = d.dup_id
    RETURNING m.id
),
repoint_decision_items AS (
    UPDATE public.decision_items di
    SET chain_id = d.keep_id
    FROM dupes d
    WHERE di.chain_id = d.dup_id
    RETURNING di.id
),
repoint_meeting_briefs AS (
    UPDATE public.meeting_briefs mb
    SET chain_id = d.keep_id
    FROM dupes d
    WHERE mb.chain_id = d.dup_id
    RETURNING mb.id
)
DELETE FROM public.meeting_chains mc
USING dupes d
WHERE mc.id = d.dup_id;

-- ----- Indexes (created with IF NOT EXISTS, safe on re-run)

CREATE INDEX IF NOT EXISTS idx_meeting_chains_user
    ON public.meeting_chains (user_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_meeting_chains_user_series_unique
    ON public.meeting_chains (user_id, calendar_series_id)
    WHERE calendar_series_id IS NOT NULL;

-- ----- Tighten the column AFTER backfill + orphan cleanup.
-- Phase C above guarantees no NULL rows remain; if this still fires,
-- something exotic is going on (concurrent INSERT, partial backfill
-- failure) and the whole transaction rolls back cleanly.

ALTER TABLE public.meeting_chains
    ALTER COLUMN user_id SET NOT NULL;

-- ============================================================
-- 2) meetings.chain_assignment_* (provenance + manual lock)
-- ============================================================

ALTER TABLE public.meetings
    ADD COLUMN IF NOT EXISTS chain_assignment_method TEXT,
    ADD COLUMN IF NOT EXISTS chain_assignment_score NUMERIC(5, 4),
    ADD COLUMN IF NOT EXISTS chain_assignment_reason JSONB,
    ADD COLUMN IF NOT EXISTS chain_assignment_locked_at TIMESTAMPTZ;

-- CHECK constraint added separately (idempotent via DROP IF EXISTS).
-- Phase 1 enum: series_id / auto_score / llm_suggestion / decision_match /
-- manual_user / manual_removed. New methods need a follow-up migration
-- so we never accept a NULL constraint accidentally.
ALTER TABLE public.meetings
    DROP CONSTRAINT IF EXISTS meetings_chain_assignment_method_check;

ALTER TABLE public.meetings
    ADD CONSTRAINT meetings_chain_assignment_method_check
    CHECK (chain_assignment_method IS NULL OR chain_assignment_method IN (
        'series_id',
        'auto_score',
        'llm_suggestion',
        'decision_match',
        'manual_user',
        'manual_removed'
    ));

-- ----- Backfill: existing chain_id assignments get a method tag so the
-- new matcher can reason about them. Series id beats auto_score; chains
-- with no series id are best-effort attributed to legacy auto_score.
-- (Phase 1's old matcher used a single hard threshold, no concept of
-- llm_suggestion or decision_match; backfilling to those values would
-- lie about provenance.)

UPDATE public.meetings m
SET chain_assignment_method = 'series_id'
WHERE m.chain_id IS NOT NULL
  AND m.chain_assignment_method IS NULL
  AND EXISTS (
      SELECT 1 FROM public.meeting_chains mc
      WHERE mc.id = m.chain_id
        AND mc.calendar_series_id IS NOT NULL
  );

UPDATE public.meetings
SET chain_assignment_method = 'auto_score'
WHERE chain_id IS NOT NULL
  AND chain_assignment_method IS NULL;

-- ============================================================
-- 3) chain_decisions_v2 — shadow + suggestion log
-- ============================================================
--
-- Single source of truth for:
--   - shadow-mode v3 decisions logged before cutover (Phase 2)
--   - pending LLM suggestions waiting on user accept/decline (Phase 4)
--   - suppression counters that mute repeat-rejected patterns
--
-- State machine: each row is one (user, meeting, candidate, algo
-- version) attempt. Status transitions UPDATE the row in place
-- (shadow_only → pending → accepted | declined | expired | suppressed |
-- applied). The UNIQUE constraint prevents duplicate INSERT for the
-- same logical attempt; it does NOT prevent state mutation.
--
-- candidate_key encodes the candidate IDENTITY at decision time:
--   "chain:<uuid>"   → matched against an existing chain
--   "meeting:<uuid>" → matched against an orphan meeting (which may
--                       later become a chain — old key not rewritten;
--                       a new attempt produces a new row).
--
-- Append-only event log philosophy (Kafka / event sourcing): old keys
-- record what we knew at decision time, never historically rewritten.

CREATE TABLE IF NOT EXISTS public.chain_decisions_v2 (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    meeting_id           UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    candidate_chain_id   UUID REFERENCES public.meeting_chains(id) ON DELETE SET NULL,
    matched_meeting_id   UUID REFERENCES public.meetings(id) ON DELETE SET NULL,
    candidate_key        TEXT NOT NULL,
    algorithm_version    TEXT NOT NULL DEFAULT 'chain-v3',
    mode                 TEXT NOT NULL CHECK (mode IN ('shadow', 'suggestion', 'applied')),
    status               TEXT NOT NULL CHECK (status IN (
        'shadow_only',
        'pending',
        'accepted',
        'declined',
        'expired',
        'suppressed',
        'applied'
    )),
    -- Stage 1+2 features (NULL for Stage 3 rows that don't compute them)
    participant_overlap  NUMERIC(5, 4),
    title_similarity     NUMERIC(5, 4),
    weighted_score       NUMERIC(5, 4),
    -- Stage 3 feature (NULL for Stage 1+2 rows)
    decision_cosine      NUMERIC(5, 4),
    -- Per-feature breakdown (typed Go struct on the app side, not enforced
    -- via JSON Schema here — see roadmap §Persistence Contract).
    feature_scores       JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Optional LLM judge result (nullable when LLM wasn't invoked)
    llm_decision         TEXT CHECK (llm_decision IS NULL OR llm_decision IN ('related', 'not_related')),
    llm_confidence       NUMERIC(5, 4),
    -- Suppression key namespaces: "metadata:..." for Stage 2,
    -- "decision:..." for Stage 3. Counter logic enforced in app code.
    suppression_key      TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at           TIMESTAMPTZ,
    UNIQUE (user_id, meeting_id, candidate_key, algorithm_version)
);

-- Hot path: pending suggestion list per user (for the inbox UI).
CREATE INDEX IF NOT EXISTS idx_chain_decisions_v2_pending
    ON public.chain_decisions_v2 (user_id, created_at)
    WHERE status = 'pending';

-- Hot path: suppression counter lookup ("how many declines for this key?").
CREATE INDEX IF NOT EXISTS idx_chain_decisions_v2_suppression
    ON public.chain_decisions_v2 (user_id, suppression_key, status, created_at)
    WHERE suppression_key IS NOT NULL;

-- Read path for shadow-mode comparison (Phase 2 → Phase 4 cutover):
-- "show me v3 decisions for this user's meetings during the shadow window".
CREATE INDEX IF NOT EXISTS idx_chain_decisions_v2_shadow
    ON public.chain_decisions_v2 (user_id, meeting_id, created_at)
    WHERE mode = 'shadow';

-- ============================================================
-- 4) chain_decision_centroids — Stage 3 cache (created early, used in Phase 5)
-- ============================================================
--
-- Empty until Phase 5 ships the post-meeting embedding job. Created
-- here so Phase 1 owns the entire schema contract; Phase 5 only adds
-- application code.
--
-- pgvector(1536) matches text-embedding-3-small dimensions used
-- throughout the codebase (transcript_chat_blocks.embedding etc.).
-- Lazy invalidation: app rebuilds the centroid when computed_at is
-- older than the chain's most recent decision_item.created_at.

CREATE TABLE IF NOT EXISTS public.chain_decision_centroids (
    chain_id              UUID PRIMARY KEY REFERENCES public.meeting_chains(id) ON DELETE CASCADE,
    centroid              VECTOR(1536) NOT NULL,
    source_decision_count INT NOT NULL CHECK (source_decision_count > 0),
    computed_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_chain_decision_centroids_computed_at
    ON public.chain_decision_centroids (computed_at DESC);

COMMIT;
