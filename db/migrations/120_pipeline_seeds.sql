-- Consolidated pipeline seed data.
--
-- Sources consolidated:
--   061_webhook_processor_types.sql   table only (no seed data — populated at runtime)
--   093_seed_default_pipelines.sql    scheduler.meeting_reminder pipeline

-- ---------------------------------------------------------------------------
-- webhook_processor_types
-- No static seed data here. The table is populated at runtime by each
-- integration's plugin.Init() calling store.SeedWebhookProcessorType().
-- Known processor types registered by bundled integrations:
--   - zoom:           "zoom.webhook"           (Zoom Webhook)
--   - discord:        "discord.webhook"        (Discord Webhook)
--   - slack/events:   "slack_events"           (Slack Events API)
--   - slack/slash:    "slash_commands"         (Slack Slash Commands)
--   - slack/interact: "interactivity"          (Slack Interactivity)
-- These are registered on startup and are idempotent (ON CONFLICT DO UPDATE).
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- scheduler.meeting_reminder
-- Replicates the previously hardcoded notification that sends a Slack DM to
-- meeting creators reminding them of upcoming meetings.
-- The pipeline runs when the meeting_reminder scheduler job fires.
-- Action: slack.dm to {{user_slack_id}} with a meeting details template.
-- ---------------------------------------------------------------------------

-- Seed the trigger source (idempotent — safe to re-run).
INSERT INTO trigger_sources (type, name, enabled, created_at, updated_at)
SELECT 'scheduler.meeting_reminder', 'Meeting reminder', TRUE, NOW(), NOW()
WHERE NOT EXISTS (
    SELECT 1 FROM trigger_sources WHERE type = 'scheduler.meeting_reminder'
);

-- Seed the pipeline action (idempotent — safe to re-run).
INSERT INTO pipeline_actions (trigger_id, type, params, position, run_on)
SELECT
    ts.id,
    'slack.dm',
    jsonb_build_object(
        'user_id',          '{{user_slack_id}}',
        'message_template', 'Reminder: Your meeting starts in about an hour.' || E'\n' ||
                            '{{topic}}' || E'\n' ||
                            '{{start_time}}' || E'\n' ||
                            'Start link (do not share): {{start_url}}' || E'\n' ||
                            'Join link: {{join_url}}'
    ),
    0,
    'success'
FROM trigger_sources ts
WHERE ts.type = 'scheduler.meeting_reminder'
AND NOT EXISTS (
    SELECT 1 FROM pipeline_actions pa
    WHERE pa.trigger_id = ts.id AND pa.type = 'slack.dm' AND pa.position = 0
);
