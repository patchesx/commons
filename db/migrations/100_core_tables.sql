-- 100: Core tables — consolidated from 001, 005, 006, 007, 010, 011, 018, 019, 052, 090
-- Eliminates intermediate states (DROP COLUMN category, web_admins FK, etc.)
-- All FKs reference users(id), not web_admins(id).

-- ---------------------------------------------------------------------------
-- users — platform-neutral user record
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email               TEXT,
    display_name        TEXT NOT NULL,
    bot                 BOOLEAN NOT NULL DEFAULT FALSE,
    selected_calendar_id UUID,
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    updated_at          TIMESTAMPTZ DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS users_email_unique
    ON users (email) WHERE email IS NOT NULL;

-- ---------------------------------------------------------------------------
-- resource_categories
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS resource_categories (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT UNIQUE NOT NULL,
    required_permission TEXT NULL,
    position            INTEGER DEFAULT 0,
    created_at          TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- resource_library
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS resource_library (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    category_id UUID NOT NULL REFERENCES resource_categories(id),
    description TEXT,
    position    INTEGER DEFAULT 0,
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- channel_access_requests
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS channel_access_requests (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    requester_id       UUID REFERENCES users(id),
    slack_channel_id   TEXT NOT NULL,
    slack_channel_name TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending',
    reviewed_by        UUID REFERENCES users(id),
    reviewed_at        TIMESTAMPTZ,
    requested_at       TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- audit_log
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_log (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id   UUID REFERENCES users(id),
    action     TEXT NOT NULL,
    target     TEXT,
    detail     JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- quick_links
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS quick_links (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    label      TEXT NOT NULL,
    url        TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- contacts
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS contacts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- work_items
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS work_items (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    requester_id  UUID NOT NULL REFERENCES users(id),
    type          TEXT NOT NULL CHECK (type IN ('issue', 'feature_request')),
    title         TEXT NOT NULL,
    description   TEXT,
    status        TEXT NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open', 'acknowledged', 'in_progress', 'resolved', 'closed')),
    admin_notes   TEXT,
    resolved_by   UUID REFERENCES users(id),
    created_at    TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_work_items_status    ON work_items (status);
CREATE INDEX IF NOT EXISTS idx_work_items_requester ON work_items (requester_id);

-- ---------------------------------------------------------------------------
-- interaction_errors
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS interaction_errors (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    source        TEXT        NOT NULL,
    request_type  TEXT        NOT NULL,
    slack_user_id TEXT,
    error_message TEXT        NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_interaction_errors_created_at
    ON interaction_errors (created_at DESC);

-- ---------------------------------------------------------------------------
-- Seed default resource categories
-- ---------------------------------------------------------------------------
INSERT INTO resource_categories (name, position, required_permission) VALUES
    ('Meeting Recording', 0, NULL),
    ('Training Video',    1, NULL),
    ('Member List',       2, 'members.view'),
    ('How-to Manual',     3, NULL),
    ('Document',          4, NULL)
ON CONFLICT DO NOTHING;
