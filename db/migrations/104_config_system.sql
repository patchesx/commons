-- Migration 104: Consolidated config system infrastructure.
-- Creates config_store, config_schema (with filterable), integrations,
-- integration_config, and integration_config_schema tables.
-- Seeds default integration instances for core integration types.

-- 1. Key-value config store for feature-level and integration-level settings.
--    Sensitive values are stored with enc:v1: prefix.
CREATE TABLE IF NOT EXISTS config_store (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service    TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    sensitive  BOOLEAN DEFAULT FALSE,
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (service, key)
);

-- 2. Config schema defines valid keys, labels, and constraints per service.
--    filterable: when TRUE, this key appears in the webhook filter config_ref dropdown.
--    Only numeric/boolean operational values should be marked filterable.
CREATE TABLE IF NOT EXISTS config_schema (
    service     TEXT NOT NULL,
    key         TEXT NOT NULL,
    label       TEXT NOT NULL,
    description TEXT,
    sensitive   BOOLEAN NOT NULL DEFAULT FALSE,
    required    BOOLEAN NOT NULL DEFAULT FALSE,
    filterable  BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (service, key)
);

-- 3. Per-integration PostgreSQL schemas.
--    Each integration owns its schema: extension tables and store functions live here.
CREATE SCHEMA IF NOT EXISTS zoom;
CREATE SCHEMA IF NOT EXISTS youtube;
CREATE SCHEMA IF NOT EXISTS gdrive;
CREATE SCHEMA IF NOT EXISTS slack;
CREATE SCHEMA IF NOT EXISTS openstates;
CREATE SCHEMA IF NOT EXISTS legistar;

-- 4. Integration instance registry.
--    One row per configured integration; supports multiple instances of the same type
--    (e.g., two Zoom accounts, two Slack workspaces).
CREATE TABLE IF NOT EXISTS integrations (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type       TEXT NOT NULL,
    name       TEXT NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 5. Seed default integration instances for core types.
--    Legistar is seeded per distinct client subdomain configured in legislative_bodies.
--    No unique constraint on type, so guard with NOT EXISTS.
INSERT INTO integrations (type, name)
SELECT v.type, v.name
FROM (VALUES
    ('zoom',       'Zoom'),
    ('youtube',    'YouTube'),
    ('gdrive',     'Google Drive'),
    ('slack',      'Slack'),
    ('openstates', 'OpenStates')
) AS v(type, name)
WHERE NOT EXISTS (SELECT 1 FROM integrations i WHERE i.type = v.type);

INSERT INTO integrations (type, name)
SELECT DISTINCT 'legistar', 'Legistar (' || legistar_client || ')'
FROM legislative_bodies
WHERE data_source = 'legistar' AND legistar_client IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM integrations i WHERE i.type = 'legistar');

-- 6. Per-instance credentials and settings.
--    Sensitive values stored with enc:v1: prefix (same as config_store).
CREATE TABLE IF NOT EXISTS integration_config (
    integration_id UUID    NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    key            TEXT    NOT NULL,
    value          TEXT,
    sensitive      BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by     UUID    REFERENCES users(id) ON DELETE SET NULL,
    updated_at     TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (integration_id, key)
);

-- 7. Valid config keys per integration type — drives admin UI forms.
--    Replaces config_schema rows for integration services.
CREATE TABLE IF NOT EXISTS integration_config_schema (
    integration_type TEXT    NOT NULL,
    key              TEXT    NOT NULL,
    label            TEXT    NOT NULL,
    description      TEXT,
    sensitive        BOOLEAN NOT NULL DEFAULT FALSE,
    required         BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (integration_type, key)
);

-- 8. Auth config seeds — self-registration flag (058) and default role (060).
--    Moved from 102_auth.sql: config_schema/config_store are created above,
--    so these INSERTs must run after this migration, not before.
INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES
    ('auth', 'allow_registration', 'Allow Self-Registration',
     'When enabled, anyone can create an account at /register. Disable for admin-provisioned-only orgs.',
     FALSE, FALSE),
    ('auth', 'default_role_id', 'Default Member Role',
     'Role assigned to new self-registered users. Leave empty to assign no role (admin assigns roles later).',
     FALSE, FALSE)
ON CONFLICT (service, key) DO NOTHING;

INSERT INTO config_store (service, key, value, sensitive)
VALUES
    ('auth', 'allow_registration', 'false', FALSE),
    ('auth', 'default_role_id', '', FALSE)
ON CONFLICT (service, key) DO NOTHING;
