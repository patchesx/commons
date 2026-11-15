-- Polls written in Slack. The author clicks Emoji Straw Poll in App Home and the app walks them through it in a DM:
-- the question, the emoji options, who receives it, and how long it stays open. The draft holds their answers
-- between messages, and lapses 30 minutes after the last step. An author has at most one at a time.
CREATE SCHEMA IF NOT EXISTS slack;

CREATE TABLE IF NOT EXISTS slack.poll_drafts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_user_id  UUID REFERENCES users(id) ON DELETE CASCADE,
    author_slack_id TEXT NOT NULL UNIQUE,
    author_name     TEXT NOT NULL,
    step            TEXT NOT NULL DEFAULT 'question', -- question | options | audience | duration | confirm
    question        TEXT NOT NULL DEFAULT '',
    options         JSONB NOT NULL DEFAULT '[]',      -- [{"Emoji": "pizza", "Label": "Pizza"}]
    audience        TEXT NOT NULL DEFAULT '',         -- channel | everyone, once chosen
    channel_id      TEXT NOT NULL DEFAULT '',
    duration_secs   INT NOT NULL DEFAULT 0,           -- a length of time from when the poll starts, or
    closes_at       TIMESTAMPTZ,                      -- an exact time, whichever the author gave
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (step IN ('question', 'options', 'audience', 'duration', 'confirm'))
);
