-- 103: Roles & permissions — consolidated from 002, 011, 025, 031, 043, 044, 052, 058, 086, 095
-- Eliminates RENAME operations (zoom_admin→recordings_admin, youtube_admin→uploads_admin,
--   zoom.config.*→recordings.config.*, youtube.config.*→uploads.config.*, Web Admin→web_admin).
-- Eliminates DROP (jobs.view removed from viewer in 043 — simply not seeded here).
-- No web_admins table at all.

-- ---------------------------------------------------------------------------
-- roles
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT UNIQUE NOT NULL,
    description TEXT,
    system_role BOOLEAN NOT NULL DEFAULT FALSE
);

-- ---------------------------------------------------------------------------
-- permissions
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         TEXT UNIQUE NOT NULL,
    description TEXT
);

-- ---------------------------------------------------------------------------
-- role_permissions
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS role_permissions (
    role_id       UUID REFERENCES roles(id) ON DELETE CASCADE,
    permission_id UUID REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

-- ---------------------------------------------------------------------------
-- user_roles
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_roles (
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    role_id UUID REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

-- ===========================================================================
-- Seed roles (all system roles — cannot be deleted via UI)
-- ===========================================================================
INSERT INTO roles (name, description, system_role) VALUES
    ('owner',            'Full access to all features',                    TRUE),
    ('recordings_admin', 'Manage recording source configuration and view jobs', TRUE),
    ('uploads_admin',    'Manage video upload configuration and edit videos',  TRUE),
    ('channel_lead',     'Approve channel access requests',               TRUE),
    ('viewer',           'View jobs and resources, request channel access', TRUE),
    ('web_admin',        'Full access to admin section',                  TRUE)
ON CONFLICT DO NOTHING;

-- ===========================================================================
-- Seed permissions (final keys — integration-neutral, no legacy names)
-- ===========================================================================
INSERT INTO permissions (key, description) VALUES
    ('jobs.view',               'View upload job history'),
    ('videos.edit',             'Edit video metadata'),
    ('channels.approve_requests', 'Approve or decline channel access requests'),
    ('channels.request_access', 'Request access to private channels'),
    ('resources.view',          'View the resource library'),
    ('recordings.config.read',  'Read recording source configuration'),
    ('recordings.config.write', 'Update recording source configuration'),
    ('uploads.config.read',     'Read video upload configuration'),
    ('uploads.config.write',    'Update video upload configuration'),
    ('members.view',            'View member list resources'),
    ('legislation.view',        'View tracked legislation and subscribe to bill updates'),
    ('legislation.manage',      'Add, edit, and tag tracked legislation'),
    ('calendar.view',           'View calendars and events'),
    ('quick_links.view',        'View quick links'),
    ('resources.manage',        'Manage the resource library'),
    ('meetings.view',           'View meeting information'),
    ('meetings.schedule',       'Schedule and manage meetings'),
    ('meetings.manage',         'Edit and cancel scheduled meetings'),
    ('work_items.create',       'Submit issue reports and feature requests'),
    ('admin.access',            'Access the admin section of the web UI'),
    ('calendar.manage',         'Create, edit, and delete events on manually-managed calendars')
ON CONFLICT DO NOTHING;

-- ===========================================================================
-- Role → Permission assignments
-- ===========================================================================

-- owner: all permissions
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'owner'
ON CONFLICT DO NOTHING;

-- recordings_admin
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'recordings_admin'
  AND p.key IN ('recordings.config.read', 'recordings.config.write', 'jobs.view', 'videos.edit')
ON CONFLICT DO NOTHING;

-- uploads_admin
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'uploads_admin'
  AND p.key IN ('uploads.config.read', 'uploads.config.write', 'jobs.view', 'videos.edit')
ON CONFLICT DO NOTHING;

-- channel_lead
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'channel_lead'
  AND p.key IN ('channels.approve_requests', 'members.view', 'resources.view',
                'calendar.view', 'quick_links.view', 'meetings.schedule', 'meetings.manage')
ON CONFLICT DO NOTHING;

-- viewer: resources.view, channels.request_access, legislation.view, calendar.view,
--         quick_links.view, meetings.view, work_items.create
-- (jobs.view intentionally excluded — removed from viewer in 043)
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'viewer'
  AND p.key IN ('resources.view', 'channels.request_access', 'legislation.view',
                'calendar.view', 'quick_links.view', 'meetings.view', 'work_items.create')
ON CONFLICT DO NOTHING;

-- web_admin
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'web_admin'
  AND p.key = 'admin.access'
ON CONFLICT DO NOTHING;
