-- Scheduled trigger configuration. Peer to http_trigger_config.
-- Each row pairs with a trigger_sources row (type = 'scheduled').
-- The schedule column stores either an interval ("1m", "5m", "1h") or a
-- cron expression ("0 9 * * MON"). The format is parsed at runtime.
CREATE TABLE IF NOT EXISTS scheduled_trigger_config (
    trigger_id    UUID        PRIMARY KEY REFERENCES trigger_sources(id) ON DELETE CASCADE,
    schedule      TEXT        NOT NULL,
    timezone      TEXT        NOT NULL DEFAULT 'UTC',
    last_fired_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_scheduled_trigger_config_last_fired
    ON scheduled_trigger_config (last_fired_at);
