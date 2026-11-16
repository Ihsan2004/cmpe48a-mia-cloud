-- Auto-confirm extraction: items are now created as "confirmed" by default.
-- user_edited tracks whether the user has interacted with an item (edit/complete/dismiss).
-- Re-extraction deletes only user_edited=false items and preserves user-touched ones.

ALTER TABLE decision_items ADD COLUMN IF NOT EXISTS user_edited BOOLEAN NOT NULL DEFAULT FALSE;

-- Backfill: mark existing confirmed/completed/dismissed items as user-edited
-- (they went through the old manual confirm flow, so the user interacted with them)
UPDATE decision_items SET user_edited = TRUE WHERE status IN ('confirmed', 'completed', 'dismissed');

-- Promote stranded proposed items to confirmed (the confirm route no longer exists)
UPDATE decision_items SET status = 'confirmed', confirmed_at = NOW() WHERE status = 'proposed';
