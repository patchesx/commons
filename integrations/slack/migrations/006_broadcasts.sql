-- Broadcast DMs: an admin writes one message and it is DM'd to every active, non-bot member with a Slack
-- identity. Recipients are snapshotted as 'pending' rows when the broadcast is created, so a restart can
-- resume by working only the rows still pending (the same idea as pipeline_runs.current_step).
CREATE SCHEMA IF NOT EXISTS slack;

CREATE TABLE IF NOT EXISTS slack.broadcasts (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    author_name    TEXT NOT NULL,           -- snapshot, so history survives the author being removed
    body           TEXT NOT NULL,           -- the template, before {{member_name}} substitution
    status         TEXT NOT NULL DEFAULT 'sending', -- sending | done
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ,
    claimed_at     TIMESTAMPTZ              -- set while a goroutine is sending; cleared on restart
);

CREATE INDEX IF NOT EXISTS idx_broadcasts_created ON slack.broadcasts (created_at DESC);

CREATE TABLE IF NOT EXISTS slack.broadcast_recipients (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    broadcast_id UUID NOT NULL REFERENCES slack.broadcasts(id) ON DELETE CASCADE,
    user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    display_name TEXT NOT NULL,
    slack_id     TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending', -- pending | sent | failed
    error        TEXT,
    sent_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_broadcast_recipients_broadcast
    ON slack.broadcast_recipients (broadcast_id, status);

INSERT INTO permissions (key, description)
VALUES ('broadcast.send', 'Write and send a Slack DM to every member, and view broadcast history')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name IN ('admin', 'owner')
  AND p.key = 'broadcast.send'
ON CONFLICT DO NOTHING;
