-- Add retry config and timeout columns to pipeline_actions.
-- retry_config is a JSON object: {"max_attempts": 3, "backoff": "exponential", "initial_delay": "5s", "max_delay": "60s"}
-- NULL = no retry (default, current behavior).
-- timeout_seconds: NULL = no timeout (default).
ALTER TABLE pipeline_actions ADD COLUMN IF NOT EXISTS retry_config JSONB;
ALTER TABLE pipeline_actions ADD COLUMN IF NOT EXISTS timeout_seconds INT;
