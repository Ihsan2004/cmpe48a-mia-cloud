-- Migration 007: brief delivery via email + Slack (Phase 1 foundation)
-- Roadmap: ROADMAP_brief_delivery.md, issue #14.
--
-- Adds:
--   1) profiles.brief_deliver_via_email / _via_slack preference toggles
--      (default false — user must explicitly opt-in on
--      /settings/notifications per roadmap Karar #12), plus
--      email_unsubscribed_at for signed-link opt-out.
--   2) slack_integrations — per-user Slack workspace bot tokens
--      (AES-GCM encrypted on the Go side). Schema allows multi-workspace
--      via UNIQUE (user_id, team_id); UI enforces a single active one
--      for POC simplicity (Karar #11).
--   3) brief_deliveries — per-channel audit + idempotency.
--      UNIQUE (meeting_id, channel) guarantees a retry/cron restart
--      never double-delivers (Karar #15). scheduled_for is evaluated
--      by the worker's dispatch window (Phase 1.5 two-phase scheduler).
--
-- All statements are idempotent so the migration can be re-run safely.

-- ============================================================
-- 1) profiles: preference columns
-- ============================================================

ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS brief_deliver_via_email BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS brief_deliver_via_slack BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE public.profiles
  ADD COLUMN IF NOT EXISTS email_unsubscribed_at TIMESTAMPTZ;

-- ============================================================
-- 2) slack_integrations
-- ============================================================

CREATE TABLE IF NOT EXISTS public.slack_integrations (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    team_id              TEXT NOT NULL,              -- Slack workspace ID (T0XXXX)
    team_name            TEXT,
    bot_user_id          TEXT NOT NULL,              -- U0XXXX (Syncrun's bot in that workspace)
    bot_token_encrypted  TEXT NOT NULL,              -- AES-GCM ciphertext, base64 encoded
    dm_channel_id        TEXT,                       -- resolved on install or first delivery, cached
    matched_email        TEXT,                       -- Slack user email used to resolve (may differ from profiles.email)
    scope                TEXT NOT NULL,              -- granted OAuth scopes (comma-joined)
    installed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at           TIMESTAMPTZ,                -- auto-set on 401 invalid_auth from Slack
    UNIQUE (user_id, team_id)
);

-- Hot path: "find the active integration for this user"
CREATE INDEX IF NOT EXISTS idx_slack_integrations_user_active
    ON public.slack_integrations (user_id)
    WHERE revoked_at IS NULL;

-- ============================================================
-- 3) brief_deliveries
-- ============================================================

CREATE TABLE IF NOT EXISTS public.brief_deliveries (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id           UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    user_id              UUID NOT NULL REFERENCES public.users(id),
    channel              TEXT NOT NULL CHECK (channel IN ('email','slack')),
    status               TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','sent','failed','skipped')),
    attempts             INT NOT NULL DEFAULT 0,
    last_error           TEXT,
    provider_message_id  TEXT,                       -- Google SMTP Message-ID or Slack message ts
    scheduled_for        TIMESTAMPTZ NOT NULL,       -- = meeting.start_time - BriefLeadTimeMinutes
    sent_at              TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (meeting_id, channel)
);

-- Worker dispatch query uses this:
--   SELECT ... FROM brief_deliveries
--   WHERE status='pending' AND scheduled_for <= NOW()
CREATE INDEX IF NOT EXISTS idx_brief_deliveries_pending
    ON public.brief_deliveries (scheduled_for)
    WHERE status = 'pending';
