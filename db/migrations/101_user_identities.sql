-- 101: User identities — consolidated from 030, 051
-- Decouples user identity from any single chat platform.
-- Each provider gets one row per user in user_identities.
-- Eliminates intermediate state (slack_id/slack_name/slack_display_name on users).

-- ---------------------------------------------------------------------------
-- user_identities
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_identities (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,
    external_id     TEXT NOT NULL,
    external_name   TEXT,
    external_email  TEXT,
    platform_status TEXT NOT NULL DEFAULT 'unknown',
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (provider, external_id)
);
