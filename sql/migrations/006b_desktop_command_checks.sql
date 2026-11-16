-- Migration 006b: defense-in-depth CHECK constraints on desktop_command_log
-- Complements 006_desktop_sessions.sql (which created the table).
--
-- Phase 6 fix 2026-04-20 — the handler now validates command_type + ack
-- status before calling MarkCommandAck, but DB-level CHECKs close the
-- door against future code that bypasses the handler (bulk SQL scripts,
-- admin tools, migrations, ops fixes). The allowed values come from
-- repository/desktop_repository.go:{ValidDesktopCommandTypes,
-- ValidDesktopAckStatuses}; keep these in sync.
--
-- Idempotent: drops + recreates so re-runs are safe.

-- ============================================================
-- 1) command_type enum
-- ============================================================

ALTER TABLE public.desktop_command_log
    DROP CONSTRAINT IF EXISTS desktop_command_log_command_type_check;

ALTER TABLE public.desktop_command_log
    ADD CONSTRAINT desktop_command_log_command_type_check
    CHECK (command_type IN ('start_recording','stop_recording','force_stop'));

-- ============================================================
-- 2) client_ack_status enum (nullable — NULL = "sent, no ack yet")
-- ============================================================

ALTER TABLE public.desktop_command_log
    DROP CONSTRAINT IF EXISTS desktop_command_log_ack_status_check;

ALTER TABLE public.desktop_command_log
    ADD CONSTRAINT desktop_command_log_ack_status_check
    CHECK (client_ack_status IS NULL
        OR client_ack_status IN ('accepted','declined','no_ack'));

-- ============================================================
-- 3) Verification (run manually)
-- ============================================================
-- SELECT conname, pg_get_constraintdef(oid)
--   FROM pg_constraint
--  WHERE conrelid = 'public.desktop_command_log'::regclass
--    AND conname LIKE 'desktop_command_log_%_check';
