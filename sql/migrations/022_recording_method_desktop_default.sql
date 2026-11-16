-- 022_recording_method_desktop_default.sql
--
-- Soft-hide the bot recording path: desktop becomes the surfaced
-- option, bot stays in the schema as a dormant value (UI no longer
-- lets users pick it; backend dispatcher / scheduler / watchdog
-- still wired but receive no triggers). Reasoning + history live in
-- the project notes.
--
-- Idempotent:
--   * `ALTER COLUMN ... SET DEFAULT` is safe to re-run.
--   * The back-fill UPDATE only touches rows whose explicit value is
--     still 'bot'; rows already on 'desktop' (or any future value)
--     are untouched.
--
-- Note: the meetings.recording_method_override CHECK list keeps
-- 'bot' in the allowed set so per-meeting overrides keep parsing
-- old data, and so re-enabling the bot path in the future doesn't
-- need a column-rebuild migration. Same logic for the profiles
-- CHECK list — we are NOT dropping 'bot' from the enum, only
-- changing what gets written by default.

BEGIN;

ALTER TABLE public.profiles
    ALTER COLUMN recording_method SET DEFAULT 'desktop';

UPDATE public.profiles
SET recording_method = 'desktop'
WHERE recording_method = 'bot';

COMMIT;
