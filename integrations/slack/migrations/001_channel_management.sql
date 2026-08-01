-- Consolidated from 002_channel_approvers.sql and 003_channel_request_notifications.sql.
-- Schema is created by core migration 033_integration_schemas.sql; guard for safety.
CREATE SCHEMA IF NOT EXISTS slack;

-- Slack workspace channels synced from the Slack API.
CREATE TABLE IF NOT EXISTS slack.channels (
    slack_channel_id TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    is_archived      BOOLEAN NOT NULL DEFAULT FALSE,
    synced_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Junction: which users can approve channel access requests for a given Slack channel.
CREATE TABLE IF NOT EXISTS channel_approvers (
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slack_channel_id TEXT NOT NULL REFERENCES slack.channels(slack_channel_id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, slack_channel_id)
);

-- Tracks DM notifications sent to approvers about pending channel access requests.
CREATE TABLE IF NOT EXISTS slack.channel_request_notifications (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id       UUID NOT NULL REFERENCES channel_access_requests(id) ON DELETE CASCADE,
    slack_user_id    TEXT NOT NULL,
    dm_channel_id    TEXT NOT NULL,
    message_ts       TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_crn_request_id ON slack.channel_request_notifications (request_id);
