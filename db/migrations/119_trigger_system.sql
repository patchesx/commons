-- Consolidated trigger/pipeline system schema.
--
-- Sources consolidated (13 → 1 schema file):
--   055_generic_webhooks.sql          schema reference (old webhook system, not created)
--   061_webhook_processor_types.sql   webhook_processor_types table
--   065_webhook_filters.sql           pipeline_filters schema (value_scale column)
--   067_seed_is_private_filter.sql    schema reference only
--   068_filter_duration_minutes.sql   value_scale column reference
--   069_drop_webhook_allowed_methods.sql  nullified (column not created)
--   082_managed_by_webhooks.sql       managed_by column
--   087_webhook_action_run_on.sql     run_on column
--   091_unified_trigger_schema.sql    trigger_sources, http_trigger_config,
--                                     pipeline_actions, pipeline_filters, trigger_fires
--   094_message_variants.sql          variant_cursor column
--
-- Design: Skips the old webhook tables (webhooks, webhook_actions, webhook_filters)
-- entirely. Goes straight to the unified trigger model where trigger_sources is the
-- common parent for all event source types (HTTP webhooks, Slack events, schedulers).

-- ---------------------------------------------------------------------------
-- webhook_processor_types
-- Lookup table referenced by http_trigger_config.processor_type.
-- Populated at runtime by plugins via store.SeedWebhookProcessorType.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS webhook_processor_types (
    type  TEXT PRIMARY KEY,
    label TEXT NOT NULL
);

-- ---------------------------------------------------------------------------
-- trigger_sources
-- Common parent for all event sources. A "trigger" can be anything that fires
-- a pipeline: an HTTP webhook, a Slack event, an internal scheduler, etc.
-- The type column describes the source kind (e.g. "http.webhook",
-- "slack.team_join", "scheduler.meeting_reminder").
-- managed_by is set when a plugin owns the trigger and manages its lifecycle
-- (prevents admin deletion/editing of plugin-managed triggers).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trigger_sources (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    type       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    managed_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- http_trigger_config
-- HTTP-webhook-specific configuration. One row per HTTP-based trigger source.
-- slug is the URL path segment. verification_mode controls HMAC/mutual-TLS type
-- verification. secret and secret_header are only used for HMAC modes.
-- processor_type links to the webhook processor plugin that handles parsing.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS http_trigger_config (
    trigger_id        UUID  PRIMARY KEY REFERENCES trigger_sources(id) ON DELETE CASCADE,
    slug              TEXT  NOT NULL UNIQUE,
    description       TEXT,
    verification_mode TEXT  NOT NULL DEFAULT 'none',
    secret            TEXT,
    secret_header     TEXT,
    processor_type    TEXT  REFERENCES webhook_processor_types(type) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_http_trigger_config_slug ON http_trigger_config (slug);

-- ---------------------------------------------------------------------------
-- pipeline_actions
-- Ordered steps executed when a trigger fires. Each action is a typed operation
-- (slack.dm, slack.channel, discord.message, etc.) with JSONB params.
-- run_on controls when this action fires relative to previous action outcomes:
--   'success' — only if all prior actions succeeded (default)
--   'failure' — only if a prior action failed
--   'always'  — regardless of prior outcome
-- variant_cursor tracks round-robin selection across multiple message_template
--   variants stored in the params JSONB.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS pipeline_actions (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_id     UUID        NOT NULL REFERENCES trigger_sources(id) ON DELETE CASCADE,
    type           TEXT        NOT NULL,
    params         JSONB       NOT NULL DEFAULT '{}',
    position       INT         NOT NULL DEFAULT 0,
    run_on         TEXT        NOT NULL DEFAULT 'success',
    variant_cursor INT         NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pipeline_actions_trigger ON pipeline_actions (trigger_id, run_on, position);

-- ---------------------------------------------------------------------------
-- pipeline_filters
-- Declarative filter chain evaluated before pipeline actions run.
-- All filters for a trigger are AND'd — every filter must pass for the
-- pipeline to fire. Supported operators: eq, neq, gt, gte, lt, lte, contains,
-- not_contains, exists, not_exists.
-- config_ref is a "service.key" reference resolved from config_store at eval
-- time. When set, it overrides the literal value.
-- value_scale is a multiplier applied to config_ref values before comparison
-- (e.g. 60 to convert config-store minutes to seconds).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS pipeline_filters (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_id  UUID        NOT NULL REFERENCES trigger_sources(id) ON DELETE CASCADE,
    position    INTEGER     NOT NULL,
    field       TEXT        NOT NULL,
    operator    TEXT        NOT NULL,
    value       TEXT,
    config_ref  TEXT,
    value_scale NUMERIC     NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pipeline_filters_trigger ON pipeline_filters (trigger_id);

-- ---------------------------------------------------------------------------
-- trigger_fires
-- Deduplication records ensuring a trigger only fires once per entity.
-- Composite primary key on (trigger_id, entity_id) enforces the uniqueness
-- constraint. Used for "fire once" semantics (e.g. Slack welcome messages
-- should not be sent to the same user more than once).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trigger_fires (
    trigger_id UUID        NOT NULL REFERENCES trigger_sources(id) ON DELETE CASCADE,
    entity_id  TEXT        NOT NULL,
    fired_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trigger_id, entity_id)
);
