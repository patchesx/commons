-- Add per-action condition column.
-- condition is a single JSON filter object evaluated before the action runs:
--   {"field": "has_profile", "operator": "eq", "value": "true"}
-- NULL condition = always run (default, preserves existing behavior).
ALTER TABLE pipeline_actions ADD COLUMN IF NOT EXISTS condition JSONB;
