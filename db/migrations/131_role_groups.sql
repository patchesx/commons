-- 131: Role groups — replace direct role assignment with group-based assignment.
-- Users get ONE role group; the group carries a set of roles; roles carry permissions.
-- user_roles is no longer read or written (table kept as vestigial for safety).

CREATE TABLE IF NOT EXISTS role_groups (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT UNIQUE NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT,
    system_group BOOLEAN NOT NULL DEFAULT FALSE,
    sort_order   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS role_group_members (
    group_id UUID REFERENCES role_groups(id) ON DELETE CASCADE,
    role_id  UUID REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, role_id)
);

-- One role group per user (user_id is the PK, enforcing the single-group rule).
CREATE TABLE IF NOT EXISTS user_role_groups (
    user_id  UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    group_id UUID NOT NULL REFERENCES role_groups(id) ON DELETE CASCADE
);

-- Seed system groups.
INSERT INTO role_groups (name, display_name, description, system_group, sort_order) VALUES
    ('administrators', 'Administrators', 'Full access to all features and the admin section', TRUE, 0),
    ('members',        'Members',        'Baseline access for members',                     TRUE, 1)
ON CONFLICT (name) DO NOTHING;

-- Administrators group contains the owner and admin roles.
INSERT INTO role_group_members (group_id, role_id)
SELECT g.id, r.id FROM role_groups g, roles r
WHERE g.name = 'administrators' AND r.name IN ('owner', 'admin')
ON CONFLICT DO NOTHING;

-- Members group contains the member role.
INSERT INTO role_group_members (group_id, role_id)
SELECT g.id, r.id FROM role_groups g, roles r
WHERE g.name = 'members' AND r.name = 'member'
ON CONFLICT DO NOTHING;

-- Migrate existing user_roles assignments to user_role_groups (one group per user).
-- Users with the owner or admin role -> administrators.
INSERT INTO user_role_groups (user_id, group_id)
SELECT DISTINCT u.id, g.id
FROM users u
JOIN user_roles ur ON ur.user_id = u.id
JOIN roles r ON r.id = ur.role_id
JOIN role_groups g ON g.name = 'administrators'
WHERE r.name IN ('owner', 'admin') AND NOT u.bot
ON CONFLICT (user_id) DO NOTHING;

-- All remaining non-bot users -> members (the default for everyone).
INSERT INTO user_role_groups (user_id, group_id)
SELECT u.id, g.id
FROM users u
JOIN role_groups g ON g.name = 'members'
WHERE NOT u.bot
ON CONFLICT (user_id) DO NOTHING;

-- Config: default_group_id replaces default_role_id for self-registration.
INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES ('auth', 'default_group_id', 'Default Member Group',
        'Role group assigned to new self-registered users. Leave empty to assign no group (admin assigns later).',
        FALSE, FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- Default to the Members group.
INSERT INTO config_store (service, key, value, sensitive)
SELECT 'auth', 'default_group_id', g.id::text, FALSE
FROM role_groups g WHERE g.name = 'members'
ON CONFLICT (service, key) DO NOTHING;
