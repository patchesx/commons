-- 102: Auth — consolidated from 008, 012, 029, 058, 060, 081, 088
-- Eliminates DROP CONSTRAINT / re-point FK patterns (all FKs reference users.id initially).
-- Eliminates RENAME operations (Web Admin → web_admin handled in 103).
-- NOTE: web_admins table is still used by store/admins.go for bootstrap/install wizard flows.
--       It will be removed when those flows are fully migrated to the users-based auth system.

-- ---------------------------------------------------------------------------
-- web_admins — legacy admin accounts, still used by bootstrap/install wizard
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS web_admins (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email                TEXT NOT NULL UNIQUE,
    password_hash         TEXT NOT NULL,
    force_password_reset BOOLEAN NOT NULL DEFAULT FALSE,
    user_id              UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- user_credentials — bcrypt credentials for web login
-- password_hash is NULLable for pending-activation member accounts (081)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_credentials (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_hash           TEXT,
    force_password_reset    BOOLEAN NOT NULL DEFAULT FALSE,
    last_login_at           TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (user_id)
);

-- ---------------------------------------------------------------------------
-- password_reset_tokens — also used for account activation (081)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS password_reset_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_hash
    ON password_reset_tokens (token_hash) WHERE used = FALSE;

CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user
    ON password_reset_tokens (user_id, created_at DESC);
