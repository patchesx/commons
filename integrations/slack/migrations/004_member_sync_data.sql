-- Tracks per-job results for the Slack member_sync (users.list) job.
-- One row per job; counts are written incrementally by users_sync.go.
CREATE SCHEMA IF NOT EXISTS slack;

CREATE TABLE IF NOT EXISTS slack.member_sync_data (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id  UUID NOT NULL REFERENCES integrations(id),
    members_added   INTEGER NOT NULL DEFAULT 0,
    members_updated INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
