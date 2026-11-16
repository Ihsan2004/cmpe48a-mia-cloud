-- Migration 021: user quotas — soft monthly USD cap per user (Faz 1)
-- Roadmap: docs/quota-limits-roadmap.md, Issue #39 (Kullanıcı Limitleme).
--
-- Faz 1 only ships the persistence + cycle math. No app-side enforcement
-- yet — middleware and HTTP responses land in Faz 2/3.
--
-- 4 logical groups:
--
--   1) user_quotas — tier ('free' | 'poc' | 'internal') + optional cap
--      override + per-user signup cycle anchor day (Stripe
--      `billing_cycle_anchor` pattern). One row per user. Trigger on
--      public.users keeps it auto-populated for new signups.
--
--   2) usage_quota_periods — materialized counter (user_id, cycle_start)
--      → used_usd. Read path is 1 PK lookup; SUM(cost_usd) on every
--      quota check would be an N+1 anti-pattern. Atomic UPSERT bumps
--      used_usd in the same async tx as the usage_logs INSERT.
--
--   3) user_quota_events — append-only admin audit. Tier/override
--      changes write old+new JSON snapshots; slog alone is not the
--      source of truth.
--
--   4) quota_cycle_for(anchor_day, at) — IMMUTABLE PL/pgSQL function
--      that returns the active (start, end) cycle for any (anchor, t).
--      Handles EOM clamping: anchor=31 + Feb → 28/29; anchor=29 +
--      non-leap Feb → 28. Same function used by app code and the
--      drift reconciliation cron (Faz 5), so cycle boundaries always
--      agree.
--
-- All statements idempotent. Backfill marks every pre-existing user as
-- 'internal' so the verification rollout (when public signups open with
-- tier='free') doesn't accidentally lock out the early-access cohort.
-- Wrapped in pg_advisory_xact_lock so concurrent migrate runs can't
-- race on the same backfill.

BEGIN;

SELECT pg_advisory_xact_lock(7400210);  -- unique migration id

-- ============================================================
-- 1) quota_cycle_for — IMMUTABLE cycle math
-- ============================================================
--
-- Returns (cycle_start, cycle_end) covering p_at for a user whose
-- signup landed on p_anchor_day. Days clamped to month length so
-- anchor=31 in February resolves to 28/29 without rotating to March.
--
-- IMMUTABLE because output depends only on inputs (no now()/random/etc.).
-- Lets PostgreSQL fold the call in LATERAL joins inside drift cron.

CREATE OR REPLACE FUNCTION public.quota_cycle_for(
    p_anchor_day SMALLINT,
    p_at         TIMESTAMPTZ
) RETURNS TABLE(cycle_start TIMESTAMPTZ, cycle_end TIMESTAMPTZ) AS $$
DECLARE
    at_utc       TIMESTAMP;
    month_start  DATE;
    eom_day      INT;
    start_date   DATE;
    end_month    DATE;
    end_eom_day  INT;
    end_date     DATE;
BEGIN
    -- Defense in depth: user_quotas.cycle_anchor_day CHECK already
    -- enforces 1..31, but a bad call site (raw SQL, future migration
    -- relaxing the constraint) would otherwise produce silently wrong
    -- cycle windows. p_anchor_day=0 → start_date = month_start - 1 day
    -- (previous month's last day) which the rest of the function then
    -- treats as a valid cycle anchor. Fail loud instead.
    IF p_anchor_day IS NULL OR p_anchor_day < 1 OR p_anchor_day > 31 THEN
        RAISE EXCEPTION 'quota_cycle_for: anchor_day must be 1..31, got %', p_anchor_day
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    at_utc      := (p_at AT TIME ZONE 'UTC');
    month_start := date_trunc('month', at_utc)::DATE;

    -- Days in current month: last day = month_start + 1 month - 1 day.
    eom_day := EXTRACT(DAY FROM
        (month_start + INTERVAL '1 month' - INTERVAL '1 day')
    )::INT;
    start_date := month_start + (LEAST(p_anchor_day::INT, eom_day) - 1);

    -- If anchored start is still in the future this calendar month,
    -- the active cycle began last month.
    IF start_date::TIMESTAMP > at_utc THEN
        month_start := (month_start - INTERVAL '1 month')::DATE;
        eom_day := EXTRACT(DAY FROM
            (month_start + INTERVAL '1 month' - INTERVAL '1 day')
        )::INT;
        start_date := month_start + (LEAST(p_anchor_day::INT, eom_day) - 1);
    END IF;

    -- Next cycle's first day: anchor clamped to next month's length.
    end_month := date_trunc(
        'month',
        (start_date::TIMESTAMP + INTERVAL '1 month')
    )::DATE;
    end_eom_day := EXTRACT(DAY FROM
        (end_month + INTERVAL '1 month' - INTERVAL '1 day')
    )::INT;
    end_date := end_month + (LEAST(p_anchor_day::INT, end_eom_day) - 1);

    RETURN QUERY SELECT
        (start_date::TIMESTAMP AT TIME ZONE 'UTC'),
        (end_date::TIMESTAMP   AT TIME ZONE 'UTC');
END;
$$ LANGUAGE plpgsql IMMUTABLE;

COMMENT ON FUNCTION public.quota_cycle_for(SMALLINT, TIMESTAMPTZ) IS
    'Active billing cycle (start, end) for a user anchored on p_anchor_day. EOM-clamped.';

-- ============================================================
-- 2) user_quotas — tier + override + cycle anchor
-- ============================================================

CREATE TABLE IF NOT EXISTS public.user_quotas (
    user_id                   UUID PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    tier                      TEXT NOT NULL DEFAULT 'free',
    monthly_usd_cap_override  NUMERIC(10, 4)
                                CHECK (monthly_usd_cap_override IS NULL OR monthly_usd_cap_override >= 0),
    cycle_anchor_day          SMALLINT NOT NULL CHECK (cycle_anchor_day BETWEEN 1 AND 31),
    notes                     TEXT,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.user_quotas.tier IS
    'Plan tier key (free|poc|internal). Validated app-side via plan registry, not via DB CHECK, so new tiers ship without a migration.';
COMMENT ON COLUMN public.user_quotas.monthly_usd_cap_override IS
    'Per-user cap override in USD. NULL → use plan default. 0 → effectively blocked.';
COMMENT ON COLUMN public.user_quotas.cycle_anchor_day IS
    'Day of month the cycle resets on. EOM-clamped by quota_cycle_for.';

-- Partial index: free is the dominant tier post-launch; admin lookups
-- ("show me all POC/internal users") need the small set only.
CREATE INDEX IF NOT EXISTS idx_user_quotas_tier
    ON public.user_quotas (tier)
    WHERE tier <> 'free';

-- updated_at auto-bump.
CREATE OR REPLACE FUNCTION public.update_user_quotas_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_quotas_set_updated_at ON public.user_quotas;
CREATE TRIGGER user_quotas_set_updated_at
    BEFORE UPDATE ON public.user_quotas
    FOR EACH ROW
    EXECUTE FUNCTION public.update_user_quotas_updated_at();

-- ============================================================
-- 3) Auto-create user_quotas on new signup
-- ============================================================
--
-- App code creates users in 3 paths (native signup, Google OAuth,
-- desktop OAuth). DB trigger is safer than 3 hand-wired call sites:
-- one of them WILL be forgotten. ON CONFLICT DO NOTHING tolerates
-- race against this migration's own backfill.

CREATE OR REPLACE FUNCTION public.create_user_quota_on_signup()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO public.user_quotas (user_id, tier, cycle_anchor_day)
    VALUES (
        NEW.id,
        'free',
        GREATEST(1, LEAST(31,
            EXTRACT(DAY FROM COALESCE(NEW.created_at, now()) AT TIME ZONE 'UTC')::INT
        ))::SMALLINT
    )
    ON CONFLICT (user_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_create_quota ON public.users;
CREATE TRIGGER users_create_quota
    AFTER INSERT ON public.users
    FOR EACH ROW
    EXECUTE FUNCTION public.create_user_quota_on_signup();

-- ============================================================
-- 4) usage_quota_periods — materialized counter
-- ============================================================

CREATE TABLE IF NOT EXISTS public.usage_quota_periods (
    user_id          UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    cycle_start_at   TIMESTAMPTZ NOT NULL,
    cycle_end_at     TIMESTAMPTZ NOT NULL,
    used_usd         NUMERIC(12, 6) NOT NULL DEFAULT 0,
    last_event_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (cycle_end_at > cycle_start_at),
    CHECK (used_usd >= 0),
    PRIMARY KEY (user_id, cycle_start_at)
);

COMMENT ON TABLE public.usage_quota_periods IS
    'Materialized monthly counter per user. Atomic UPSERT in the same async tx as usage_logs INSERT; drift reconciled by daily cron (Faz 5).';

-- Active-cycle lookup hot path: WHERE user_id=? AND cycle_start_at<=now() AND cycle_end_at>now().
-- DESC on cycle_end_at lets PK + this index together cover the worst case.
CREATE INDEX IF NOT EXISTS idx_usage_quota_periods_active
    ON public.usage_quota_periods (user_id, cycle_end_at DESC);

-- ============================================================
-- 5) user_quota_events — admin audit
-- ============================================================

CREATE TABLE IF NOT EXISTS public.user_quota_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    actor          TEXT NOT NULL,
    old_snapshot   JSONB,
    new_snapshot   JSONB NOT NULL,
    reason         TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.user_quota_events.actor IS
    'Admin key label, migration id, or system source. Free-form, app-validated.';

CREATE INDEX IF NOT EXISTS idx_user_quota_events_user
    ON public.user_quota_events (user_id, created_at DESC);

-- ============================================================
-- 6) Backfill: existing users
-- ============================================================
--
-- Pre-launch cohort = team + early access. Default 'internal' so they
-- don't get blocked when the Faz 3 middleware lands. After OAuth
-- verification opens public signups, ops will hand-pick which to
-- demote to 'free' via PATCH /api/admin/users/:id/quota (Faz 5).

INSERT INTO public.user_quotas (user_id, tier, cycle_anchor_day, notes)
SELECT
    u.id,
    'internal',
    GREATEST(1, LEAST(31,
        EXTRACT(DAY FROM COALESCE(u.created_at, now()) AT TIME ZONE 'UTC')::INT
    ))::SMALLINT,
    'backfill:021 — pre-launch cohort'
FROM public.users u
ON CONFLICT (user_id) DO NOTHING;

-- ============================================================
-- 7) Backfill: usage_quota_periods from usage_logs
-- ============================================================
--
-- For every user with a quota row, materialize the counter for their
-- *current* cycle by summing usage_logs in [cycle_start, cycle_end).
-- Past cycles intentionally NOT backfilled: the counter table is only
-- consulted for the active window, and history lives in usage_logs.

INSERT INTO public.usage_quota_periods
    (user_id, cycle_start_at, cycle_end_at, used_usd, last_event_at)
SELECT
    uq.user_id,
    qc.cycle_start,
    qc.cycle_end,
    COALESCE(SUM(ul.cost_usd), 0)::NUMERIC(12, 6),
    now()
FROM public.user_quotas uq
CROSS JOIN LATERAL public.quota_cycle_for(uq.cycle_anchor_day, now()) qc
LEFT JOIN public.usage_logs ul
    ON ul.user_id     = uq.user_id
   AND ul.created_at >= qc.cycle_start
   AND ul.created_at <  qc.cycle_end
GROUP BY uq.user_id, qc.cycle_start, qc.cycle_end
ON CONFLICT (user_id, cycle_start_at) DO UPDATE SET
    cycle_end_at  = EXCLUDED.cycle_end_at,
    used_usd      = EXCLUDED.used_usd,
    last_event_at = now();

-- ============================================================
-- 8) Backfill: audit event for the backfill itself
-- ============================================================

INSERT INTO public.user_quota_events
    (user_id, actor, old_snapshot, new_snapshot, reason)
SELECT
    uq.user_id,
    'migration:021',
    NULL,
    jsonb_build_object(
        'tier',                     uq.tier,
        'monthly_usd_cap_override', uq.monthly_usd_cap_override,
        'cycle_anchor_day',         uq.cycle_anchor_day
    ),
    'Initial quota row created by migration 021 backfill'
FROM public.user_quotas uq
WHERE NOT EXISTS (
    SELECT 1 FROM public.user_quota_events e
    WHERE e.user_id = uq.user_id
      AND e.actor   = 'migration:021'
);

COMMIT;
