-- Consolidated final state of Slack event handlers.
-- Originally created as slack_event_handlers in public (core 089), moved to slack schema
-- and renamed by plugin 005. Created directly in slack schema here.
-- 004 (welcome_dm) is fully nullified and excluded.
-- 005's event_handler_fires was replaced by event_fires in 006; excluded.

CREATE TABLE IF NOT EXISTS slack.event_handlers (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type  TEXT        NOT NULL,
    action_type TEXT        NOT NULL,
    params      JSONB       NOT NULL DEFAULT '{}',
    filters     JSONB,
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    position    INT         NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_event_handlers_event_type
    ON slack.event_handlers (event_type, position);

-- Tracks which (event_type, user) pairs have already fired for fire-once Slack events
-- such as team_join welcome messages. Replaced per-handler tracking (event_handler_fires)
-- in favour of per-event-type tracking.
CREATE TABLE IF NOT EXISTS slack.event_fires (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type       TEXT        NOT NULL,
    user_external_id TEXT        NOT NULL,
    fired_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(event_type, user_external_id)
);
