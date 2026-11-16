-- Faz 2: ADR context fields + temporal metadata for decision extraction
-- Adds "why" (context, alternatives) and "when" (timestamps) to extracted items

ALTER TABLE decision_items ADD COLUMN IF NOT EXISTS context TEXT;
ALTER TABLE decision_items ADD COLUMN IF NOT EXISTS alternatives_considered TEXT;
ALTER TABLE decision_items ADD COLUMN IF NOT EXISTS decision_timestamp_start FLOAT;
ALTER TABLE decision_items ADD COLUMN IF NOT EXISTS decision_timestamp_end FLOAT;
