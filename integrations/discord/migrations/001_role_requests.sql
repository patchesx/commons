CREATE SCHEMA IF NOT EXISTS discord;

CREATE TABLE IF NOT EXISTS discord.role_requests (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id),
    role_id     TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    reason      TEXT,
    status      TEXT NOT NULL DEFAULT 'pending',
    reviewed_by UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS discord_role_requests_user_id ON discord.role_requests (user_id);
CREATE INDEX IF NOT EXISTS discord_role_requests_status ON discord.role_requests (status);
