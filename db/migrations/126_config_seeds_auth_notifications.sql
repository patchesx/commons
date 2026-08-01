-- Migration 126: Consolidated config seeds — auth and notifications config.
-- Seeds config_schema entries and default config_store values for
-- self-registration and notification routing.

-- ============================================================================
-- Auth config
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('auth', 'allow_registration', 'Allow Self-Registration',
     'When enabled, anyone can create an account at /register. Disable for admin-provisioned-only orgs.',
     FALSE, FALSE),

    ('auth', 'default_role_id', 'Default Member Role',
     'Role assigned to new self-registered users. Leave empty to assign no role (admin assigns roles later).',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;

INSERT INTO config_store (service, key, value, sensitive) VALUES
    ('auth', 'allow_registration', 'false', FALSE),
    ('auth', 'default_role_id',    '',      FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- ============================================================================
-- Notifications config
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('notifications', 'routing', 'Notification Routing',
     'Where notifications are sent: web_only (portal inbox only) or web_and_chat (portal + configured chat app).',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;

INSERT INTO config_store (service, key, value, sensitive) VALUES
    ('notifications', 'routing', 'web_and_chat', FALSE)
ON CONFLICT (service, key) DO NOTHING;
