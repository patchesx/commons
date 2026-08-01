-- Consolidated legislation schema, config, and permissions.
-- Combines: 025_legislation.sql + 077_bill_subjects.sql

-- ---------------------------------------------------------------------------
-- Legislative bodies: jurisdictions being tracked and their data source
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS legislative_bodies (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    TEXT NOT NULL,        -- "Missouri House", "Jackson County Legislature"
    level                   TEXT NOT NULL,        -- 'state' | 'county' | 'city' | 'court'
    state                   TEXT NOT NULL,        -- 'MO' | 'KS'
    data_source             TEXT NOT NULL,        -- 'openstates' | 'legistar' | 'manual'
    -- OpenStates-specific
    openstates_jurisdiction TEXT,                 -- e.g. "ocd-jurisdiction/country:us/state:mo/government"
    openstates_chamber      TEXT,                 -- 'upper' | 'lower' | NULL means sync both
    -- Legistar-specific
    legistar_client         TEXT,                 -- subdomain used in API URL, e.g. "jacksonco"
    legistar_body_id        INTEGER,              -- filter by MatterBodyId; NULL = all bodies
    active                  BOOLEAN DEFAULT TRUE,
    created_at              TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- Bills: unified table for bills, ordinances, and resolutions
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS bills (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    body_id             UUID NOT NULL REFERENCES legislative_bodies(id),
    external_id         TEXT,                 -- OpenStates ocd-bill/... ID or Legistar MatterId as text
    identifier          TEXT NOT NULL,        -- "HB 2780", "22183", "Ord. 2026-14"
    title               TEXT NOT NULL,
    type                TEXT,                 -- 'bill' | 'resolution' | 'ordinance' | 'other'
    chamber             TEXT,                 -- 'upper' | 'lower' | NULL for local/unicameral bodies
    session             TEXT,                 -- "2026"
    status              TEXT,
    latest_action       TEXT,
    latest_action_date  TIMESTAMPTZ,
    introduced_at       TIMESTAMPTZ,
    link                TEXT,                 -- canonical URL to full text
    -- Chapter tracking fields set by PJC admins
    chapter_position    TEXT,                 -- 'support' | 'oppose' | 'monitor' | 'neutral' | NULL (unreviewed)
    notes               TEXT,                 -- internal PJC notes, not shown to general members
    -- Import management
    auto_imported       BOOLEAN DEFAULT FALSE,
    following           BOOLEAN DEFAULT TRUE, -- false = dismissed by admin; skipped on future syncs
    synced_at           TIMESTAMPTZ,
    subjects            TEXT[] NOT NULL DEFAULT '{}',  -- from 077_bill_subjects
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    updated_at          TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (body_id, external_id)             -- prevents duplicate imports; NULL external_id = manual entry
);

-- Ensure columns added by later migrations exist even if the bills table was
-- created by an earlier migration before this consolidation (077_bill_subjects).
DO $$ BEGIN
    ALTER TABLE bills ADD COLUMN subjects TEXT[] NOT NULL DEFAULT '{}';
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

-- ---------------------------------------------------------------------------
-- Tags: issue tags applied to bills (housing, labor, criminal justice, etc.)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tags (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT UNIQUE NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS bill_tags (
    bill_id UUID REFERENCES bills(id) ON DELETE CASCADE,
    tag_id  UUID REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (bill_id, tag_id)
);

-- ---------------------------------------------------------------------------
-- Bill subscriptions: per-user per-bill notification opt-in
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS bill_subscriptions (
    user_id    UUID REFERENCES users(id) ON DELETE CASCADE,
    bill_id    UUID REFERENCES bills(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_id, bill_id)
);

-- ---------------------------------------------------------------------------
-- Bill updates: append-only log of field changes for notifications and audit
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS bill_updates (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bill_id    UUID NOT NULL REFERENCES bills(id) ON DELETE CASCADE,
    field      TEXT NOT NULL,   -- 'status' | 'latest_action' | 'chapter_position' | etc.
    old_value  TEXT,
    new_value  TEXT,
    source     TEXT NOT NULL,   -- 'openstates' | 'legistar' | 'manual'
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- Bill import filters: criteria used to auto-import bills during sync
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS bill_import_filters (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    body_id     UUID REFERENCES legislative_bodies(id) ON DELETE CASCADE,
    filter_type TEXT NOT NULL,  -- 'subject' for OpenStates | 'matter_type' for Legistar
    value       TEXT NOT NULL,  -- e.g. "HOUSING AND URBAN DEVELOPMENT" | "Ordinance"
    active      BOOLEAN DEFAULT TRUE,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- Seed initial legislative bodies
-- ---------------------------------------------------------------------------
INSERT INTO legislative_bodies (name, level, state, data_source, openstates_jurisdiction, openstates_chamber)
SELECT v.name, v.level, v.state, v.data_source, v.openstates_jurisdiction, v.openstates_chamber
FROM (VALUES
    ('Missouri House',   'state', 'MO', 'openstates', 'ocd-jurisdiction/country:us/state:mo/government', 'lower'),
    ('Missouri Senate',  'state', 'MO', 'openstates', 'ocd-jurisdiction/country:us/state:mo/government', 'upper'),
    ('Kansas House',     'state', 'KS', 'openstates', 'ocd-jurisdiction/country:us/state:ks/government', 'lower'),
    ('Kansas Senate',    'state', 'KS', 'openstates', 'ocd-jurisdiction/country:us/state:ks/government', 'upper')
) AS v(name, level, state, data_source, openstates_jurisdiction, openstates_chamber)
WHERE NOT EXISTS (SELECT 1 FROM legislative_bodies b WHERE b.name = v.name);

INSERT INTO legislative_bodies (name, level, state, data_source, legistar_client)
SELECT 'Jackson County Legislature', 'county', 'MO', 'legistar', 'jacksonco'
WHERE NOT EXISTS (SELECT 1 FROM legislative_bodies b WHERE b.name = 'Jackson County Legislature');

-- ---------------------------------------------------------------------------
-- Seed Legistar integration instance (moved from 104: depends on legislative_bodies)
-- ---------------------------------------------------------------------------
INSERT INTO integrations (type, name)
SELECT DISTINCT 'legistar', 'Legistar (' || legistar_client || ')'
FROM legislative_bodies
WHERE data_source = 'legistar' AND legistar_client IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM integrations i WHERE i.type = 'legistar');

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
INSERT INTO permissions (key, description) VALUES
    ('legislation.view',   'View tracked legislation and subscribe to bill updates'),
    ('legislation.manage', 'Add, edit, and tag tracked legislation')
ON CONFLICT DO NOTHING;

-- Grant both permissions to the owner role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'owner'
  AND p.key IN ('legislation.view', 'legislation.manage')
ON CONFLICT DO NOTHING;

-- Grant legislation.view to viewer role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'viewer'
  AND p.key = 'legislation.view'
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------------
-- Config for OpenStates API
-- ---------------------------------------------------------------------------
INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES
    ('openstates', 'api_key', 'API Key', 'OpenStates v3 API key — required for syncing KS and MO state legislature bills. Get one at openstates.org/accounts/signup/', TRUE, FALSE)
ON CONFLICT DO NOTHING;
