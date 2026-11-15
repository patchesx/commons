-- Broadcasts written in Slack. The author clicks Broadcast in App Home, the app DMs a prompt, and the next
-- message they send becomes the draft. A draft is 'awaiting' the message, then 'ready' to be sent or scheduled.
-- It expires 10 minutes after the last step, and an author has at most one at a time.
CREATE SCHEMA IF NOT EXISTS slack;

CREATE TABLE IF NOT EXISTS slack.broadcast_drafts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_user_id  UUID REFERENCES users(id) ON DELETE CASCADE,
    author_slack_id TEXT NOT NULL UNIQUE,
    author_name     TEXT NOT NULL,
    state           TEXT NOT NULL DEFAULT 'awaiting', -- awaiting | ready
    body            TEXT,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (state IN ('awaiting', 'ready'))
);
