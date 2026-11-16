-- Migration 012 — Phase 2.4 R9b
--
-- Extend `desktop_command_log.client_ack_status` to accept `'extended'`
-- alongside the existing `accepted | declined | no_ack | NULL` set.
--
-- Rationale: the recorder's force_stop extension prompt (R9b) returns
-- one of three outcomes:
--   * Devam ettir → ack=extended (new — recording continues for another
--                   30 minutes)
--   * Şimdi durdur → ack=accepted (immediate Terminating)
--   * 2-min timeout → either silent_cap-driven Terminate (ack=accepted)
--                     or another silent extension (ack=extended)
--
-- Without this constraint update the backend's WS reader silently drops
-- the `extended` ack with "unknown ack status", and the command_log
-- can't distinguish a normal accepted Stop from a user-driven extension.
--
-- Idempotent: drops + recreates the CHECK constraint with the wider set.
-- Safe to re-run.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'desktop_command_log'
          AND constraint_name = 'desktop_command_log_ack_status_check'
    ) THEN
        ALTER TABLE public.desktop_command_log
            DROP CONSTRAINT desktop_command_log_ack_status_check;
    END IF;

    ALTER TABLE public.desktop_command_log
        ADD CONSTRAINT desktop_command_log_ack_status_check
        CHECK (
            client_ack_status IS NULL
            OR client_ack_status IN ('accepted', 'declined', 'no_ack', 'extended')
        );
END $$;

COMMIT;
