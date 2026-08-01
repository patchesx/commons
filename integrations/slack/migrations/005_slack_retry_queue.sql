-- Retry queue for Slack messages that failed to send due to transient errors
-- (rate limits / HTTP 429, 5xx). A background drainer retries them with
-- exponential backoff; rows survive process restarts so messages are not lost.
CREATE SCHEMA IF NOT EXISTS slack;

CREATE TABLE IF NOT EXISTS slack.retry_queue (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    destination     TEXT NOT NULL,         -- Slack channel ID, or user ID for DMs
    is_dm           BOOLEAN NOT NULL,      -- true: DM to destination (user ID); false: channel post
    text            TEXT NOT NULL,
    blocks          JSONB,                 -- nullable; Block Kit blocks for DMs
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 5,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Drainer looks up due, non-exhausted rows ordered by next attempt time.
CREATE INDEX IF NOT EXISTS idx_retry_queue_next_attempt
    ON slack.retry_queue (next_attempt_at)
    WHERE attempt_count < max_attempts;
