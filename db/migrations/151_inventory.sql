-- Inventory tracking: items, custody history, custom field definitions,
-- permissions, and config.
-- NOTE: 151 leaves room for migrations numbered 147-150 on other branches.

CREATE SCHEMA IF NOT EXISTS inventory;

-- ---------------------------------------------------------------------------
-- Items: tracked physical things. Soft-deleted via archived_at.
-- code is the 6-character human-readable identifier printed on labels; it is
-- unique across archived items too so a retired code is never reused.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory.items (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    TEXT NOT NULL,
    description             TEXT,
    location                TEXT,
    code                    TEXT NOT NULL UNIQUE
                            CHECK (char_length(code) = 6 AND code = upper(code)),
    metadata                JSONB NOT NULL DEFAULT '{}'::jsonb,  -- custom fields, string values only
    verify_every_days       INT DEFAULT 7
                            CHECK (verify_every_days IS NULL OR verify_every_days > 0), -- NULL = no reminders
    verify_grace_days       INT
                            CHECK (verify_grace_days IS NULL OR verify_grace_days > 0), -- NULL = config default
    last_verified_at        TIMESTAMPTZ,
    last_reminded_at        TIMESTAMPTZ,
    last_escalated_at       TIMESTAMPTZ,  -- admins alerted that possession is unconfirmed; cleared on verify or custody change
    current_holder_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    archived_at             TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS inventory_items_holder_idx
    ON inventory.items (current_holder_user_id) WHERE current_holder_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS inventory_items_active_idx
    ON inventory.items (name) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS inventory_items_metadata_idx
    ON inventory.items USING GIN (metadata);

-- ---------------------------------------------------------------------------
-- Custody events: history of possession changes and claims.
-- Rows are not deleted. status moves from pending_unmatched to linked when an
-- admin links a claim to a member.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory.custody_events (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    item_id                 UUID NOT NULL REFERENCES inventory.items(id),
    user_id                 UUID REFERENCES users(id) ON DELETE SET NULL,
    previous_holder_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    raw_name                TEXT,
    raw_email               TEXT,
    source                  TEXT NOT NULL
                            CHECK (source IN ('slack', 'web_form', 'admin', 'verify', 'revert')),
    status                  TEXT NOT NULL
                            CHECK (status IN ('applied', 'pending_unmatched', 'linked', 'disputed')),
    ip_address              TEXT,
    user_agent              TEXT,
    note                    TEXT,
    applied_at              TIMESTAMPTZ,  -- when the holder actually changed; NULL while a claim is pending
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS inventory_custody_events_item_idx
    ON inventory.custody_events (item_id, created_at DESC);
CREATE INDEX IF NOT EXISTS inventory_custody_events_pending_idx
    ON inventory.custody_events (created_at) WHERE status = 'pending_unmatched';

-- ---------------------------------------------------------------------------
-- Custom field definitions: names and orders the keys used in items.metadata.
-- No types, options, or required flag; values are always optional strings.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS inventory.field_definitions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         TEXT NOT NULL UNIQUE,
    label       TEXT NOT NULL,
    sort_order  INT NOT NULL DEFAULT 0,
    active      BOOL NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- Email fallback for the web claim form: match on user_identities.external_email.
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS user_identities_external_email_lower_idx
    ON user_identities (lower(external_email)) WHERE external_email IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Config
-- ---------------------------------------------------------------------------
INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES
  ('inventory', 'default_grace_days',         'Default Verification Grace (days)',  'Days after a verification reminder before inventory admins are alerted that possession is unconfirmed. Items can override this.', FALSE, FALSE),
  ('inventory', 'claim_rate_limit_per_hour',  'Web Claim Rate Limit (per hour)',     'Maximum claim form submissions per IP address per hour on the public inventory page.',                                              FALSE, FALSE),
  ('jobs',      'inventory_verify_enabled',          'Inventory Verification Reminders',          'Send DM reminders asking holders to confirm they still have their items.', FALSE, FALSE),
  ('jobs',      'inventory_verify_interval_minutes', 'Inventory Verification Interval (minutes)', 'How often to check for items due for verification.',                       FALSE, FALSE)
ON CONFLICT (service, key) DO NOTHING;

INSERT INTO config_store (service, key, value, sensitive)
VALUES
  ('inventory', 'default_grace_days',                 '5',    FALSE),
  ('inventory', 'claim_rate_limit_per_hour',          '10',   FALSE),
  ('jobs',      'inventory_verify_enabled',           'true', FALSE),
  ('jobs',      'inventory_verify_interval_minutes',  '1440', FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
INSERT INTO permissions (key, description)
VALUES
  ('inventory.view',   'View inventory items, holders, and the inventory grid'),
  ('inventory.manage', 'Create and edit inventory items, custom fields, QR labels, and verification cadence'),
  ('inventory.admin',  'Link unmatched claims, revert custody, set per-item grace, and receive inventory alerts')
ON CONFLICT (key) DO NOTHING;

-- admin and owner get every inventory permission. member gets none: claiming
-- and verifying items need no permission.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name IN ('admin', 'owner')
  AND p.key IN ('inventory.view', 'inventory.manage', 'inventory.admin')
ON CONFLICT DO NOTHING;
