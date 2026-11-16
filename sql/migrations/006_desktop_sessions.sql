-- Migration 006: desktop WebSocket command channel (Phase 6)
-- Supports: scheduler-pushed start/stop/force_stop commands from backend to
-- the syncrun-recorder desktop app, with idempotent command log and per-user
-- session heartbeat tracking.
--
-- All statements are idempotent.

-- ============================================================
-- 1) desktop_sessions — one row per open WebSocket connection
-- ============================================================
--
-- Lifecycle: INSERT on handshake -> UPDATE last_heartbeat_at on every ping ->
-- UPDATE disconnected_at on close / 90s heartbeat timeout. Rows are kept
-- indefinitely for audit; the index below is tuned for "is this user online
-- right now?" lookups (disconnected_at IS NULL).

CREATE TABLE IF NOT EXISTS public.desktop_sessions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    connected_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    disconnected_at      TIMESTAMPTZ,
    client_version       TEXT
);

CREATE INDEX IF NOT EXISTS idx_desktop_sessions_user_active
    ON public.desktop_sessions (user_id, disconnected_at);

-- ============================================================
-- 2) desktop_command_log — idempotent record of backend->desktop commands
-- ============================================================
--
-- UNIQUE (meeting_id, command_type) is the idempotency primitive: the scheduler
-- calls InsertCommandIfMissing which ON CONFLICT DO NOTHING, so a repeated tick
-- in the same time window becomes a no-op. client_ack_status transitions from
-- NULL -> 'accepted'|'declined'|'no_ack' when the client responds (or the
-- tracker gives up).

CREATE TABLE IF NOT EXISTS public.desktop_command_log (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id         UUID NOT NULL REFERENCES public.meetings(id) ON DELETE CASCADE,
    user_id            UUID NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    command_type       TEXT NOT NULL,
    sent_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    client_ack_status  TEXT,
    client_ack_at      TIMESTAMPTZ,
    UNIQUE (meeting_id, command_type)
);

CREATE INDEX IF NOT EXISTS idx_desktop_command_log_meeting
    ON public.desktop_command_log (meeting_id);

CREATE INDEX IF NOT EXISTS idx_desktop_command_log_user
    ON public.desktop_command_log (user_id);
