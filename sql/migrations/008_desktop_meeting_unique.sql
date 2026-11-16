-- Migration 008 — Desktop meeting idempotency hardening.
--
-- Phase 1 (B1) makes `POST /api/meetings/desktop` idempotent on the
-- recorder-owned `local_session_id` by deriving the synthetic
-- `google_event_id` deterministically as "desktop_<local_session_id>".
-- The handler does atomic upsert via `MeetingRepository.UpsertDesktopMeeting`
-- (`INSERT … ON CONFLICT (user_id, google_event_id) DO NOTHING`), but
-- without a database-level uniqueness constraint two concurrent create
-- calls (e.g. orchestrator's Start-time create racing the upload
-- worker's idempotent fallback) can both succeed and produce duplicate
-- desktop meetings for the same (user_id, local_session_id).
--
-- This migration:
--   1. Dedupes any pre-existing duplicates by keeping the OLDEST row per
--      (user_id, google_event_id) and remapping FKs from the rest. The
--      oldest stays so existing FK targets (transcript segments,
--      summaries, decisions, briefs, etc.) remain valid.
--   2. Adds a UNIQUE INDEX on (user_id, google_event_id), which is what
--      the repository's ON CONFLICT clause needs to be atomic.
--
-- Safe to run multiple times: the dedupe is a no-op when there are no
-- duplicates, and the UNIQUE INDEX is created with `IF NOT EXISTS`.
--
-- Supabase SQL Editor compatible: pure SQL, no psql metacommands.

BEGIN;

-- 1. Build a temp table of (dup_id, keep_id) pairs.
--
-- A CTE only lives for one statement, but we need to use the dupe set
-- across many UPDATEs and a final DELETE, so we materialize it as a
-- session-local temp table that is dropped automatically on COMMIT.
CREATE TEMP TABLE _dupes ON COMMIT DROP AS
WITH ranked AS (
    SELECT
        id,
        user_id,
        google_event_id,
        ROW_NUMBER() OVER (
            PARTITION BY user_id, google_event_id
            ORDER BY created_at ASC, id ASC
        ) AS rn
    FROM meetings
    WHERE google_event_id IS NOT NULL
      AND google_event_id <> ''
),
keepers AS (
    SELECT user_id, google_event_id, id AS keep_id
    FROM ranked
    WHERE rn = 1
)
SELECT r.id AS dup_id, k.keep_id
FROM ranked r
JOIN keepers k
  ON k.user_id = r.user_id
 AND k.google_event_id = r.google_event_id
WHERE r.rn > 1;

-- 2. Remap dependent rows from each duplicate onto its keeper. Order
-- matters: remap children BEFORE deleting parents so we never orphan a
-- transcript segment, summary, or decision item.
UPDATE transcript_segments    s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE transcript_chat_blocks s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE meeting_summaries      s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE summary_errors         s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE decision_items         s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE meeting_notes          s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE upload_sessions        s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE brief_deliveries       s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;
UPDATE desktop_command_log    s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;

-- meeting_briefs and brief_feedback use target_meeting_id.
UPDATE meeting_briefs s SET target_meeting_id = d.keep_id FROM _dupes d WHERE s.target_meeting_id = d.dup_id;
UPDATE brief_feedback s SET target_meeting_id = d.keep_id FROM _dupes d WHERE s.target_meeting_id = d.dup_id;

-- usage_logs.meeting_id is nullable; remap so cost reporting attributes
-- to the surviving meeting.
UPDATE usage_logs s SET meeting_id = d.keep_id FROM _dupes d WHERE s.meeting_id = d.dup_id;

-- 3. Delete the duplicate meeting rows themselves.
DELETE FROM meetings m USING _dupes d WHERE m.id = d.dup_id;

-- 4. Unique index. Partial-style WHERE keeps NULL/empty google_event_id
-- out of the constraint (defensive — current schema has NOT NULL on
-- google_event_id, but safer to be explicit).
CREATE UNIQUE INDEX IF NOT EXISTS meetings_user_google_event_unique
    ON meetings (user_id, google_event_id)
    WHERE google_event_id IS NOT NULL AND google_event_id <> '';

COMMIT;
