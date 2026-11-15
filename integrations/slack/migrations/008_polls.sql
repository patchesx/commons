-- Emoji straw polls. A member poses a question with 2-6 emoji options and a closing time; the bot posts it to a
-- channel or to every active member, and at the deadline tallies the reactions. Status is:
--   posting   messages are still being sent (poll_messages rows are pending)
--   open      posted and waiting for closes_at
--   closed    tallied and reported
--   cancelled stopped by the creator; no tally
-- poll_messages rows are created up front as pending so posting can resume after a restart, the same idea as
-- broadcast_recipients.
CREATE SCHEMA IF NOT EXISTS slack;

CREATE TABLE IF NOT EXISTS slack.polls (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    creator_name     TEXT NOT NULL,           -- snapshot, so history survives the creator being removed
    creator_slack_id TEXT NOT NULL,           -- who the report and controls are DM'd to
    question         TEXT NOT NULL,
    audience         TEXT NOT NULL,           -- channel | everyone
    channel_id       TEXT,                    -- set for the channel audience
    closes_at        TIMESTAMPTZ NOT NULL,
    status           TEXT NOT NULL DEFAULT 'posting',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at        TIMESTAMPTZ,
    claimed_at       TIMESTAMPTZ,             -- set while a goroutine is posting or closing; cleared on restart
    CHECK (audience IN ('channel', 'everyone')),
    CHECK (status IN ('posting', 'open', 'closed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_polls_created ON slack.polls (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_polls_due ON slack.polls (closes_at) WHERE status = 'open';

CREATE TABLE IF NOT EXISTS slack.poll_options (
    id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id  UUID NOT NULL REFERENCES slack.polls(id) ON DELETE CASCADE,
    position INT  NOT NULL,
    emoji    TEXT NOT NULL,                   -- shortcode without colons
    label    TEXT NOT NULL,
    UNIQUE (poll_id, position),
    UNIQUE (poll_id, emoji)
);

-- One row for a channel poll, one per member for an everyone poll.
CREATE TABLE IF NOT EXISTS slack.poll_messages (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id            UUID NOT NULL REFERENCES slack.polls(id) ON DELETE CASCADE,
    recipient_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    recipient_slack_id TEXT,
    channel_id         TEXT,                  -- where it was posted, known once posted
    ts                 TEXT,
    status             TEXT NOT NULL DEFAULT 'pending', -- pending | posted | failed
    error              TEXT
);

CREATE INDEX IF NOT EXISTS idx_poll_messages_poll ON slack.poll_messages (poll_id, status);

-- Written at close. One row per member per option they reacted with, so a member who picked several options
-- has several rows.
CREATE TABLE IF NOT EXISTS slack.poll_votes (
    poll_id       UUID NOT NULL REFERENCES slack.polls(id) ON DELETE CASCADE,
    option_id     UUID NOT NULL REFERENCES slack.poll_options(id) ON DELETE CASCADE,
    slack_user_id TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    PRIMARY KEY (option_id, slack_user_id)
);

INSERT INTO permissions (key, description)
VALUES ('poll.create', 'Create emoji straw polls from Slack and view poll history')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name IN ('admin', 'owner')
  AND p.key = 'poll.create'
ON CONFLICT DO NOTHING;
