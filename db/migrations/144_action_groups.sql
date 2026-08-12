-- Add action_group column for for_each loop body actions.
-- Body actions of a core.for_each action share the same action_group value.
-- NULL action_group = main flow action (default).
ALTER TABLE pipeline_actions ADD COLUMN IF NOT EXISTS action_group TEXT;
