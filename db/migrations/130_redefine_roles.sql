-- 130: Redefine the system role set.
--   - Rename web_admin -> admin (broader; functionally equivalent to owner: all permissions).
--   - Rename viewer -> member (baseline member role with a narrowed permission set).
--   - Demote the integration-specific system roles (recordings_admin, uploads_admin,
--     channel_lead) to non-system: they stay visible/assignable in the UI but become
--     deletable. Admin can remove them if unused.
--
-- `name` remains the stable machine identifier; display_name is the UI label.
-- Permission checks in code use the admin.access permission key (not the role name),
-- so renaming web_admin -> admin does not affect IsWebAdmin / login gate logic.

-- Rename web_admin -> admin and broaden its scope.
UPDATE roles
SET name = 'admin',
    display_name = 'Admin',
    description = 'Full access to all features and the admin section'
WHERE name = 'web_admin';

-- Rename viewer -> member and re-scope as the baseline member role.
UPDATE roles
SET name = 'member',
    display_name = 'Member',
    description = 'Baseline access for members'
WHERE name = 'viewer';

-- Ensure owner has a display name (idempotent with 129).
UPDATE roles SET display_name = 'Owner' WHERE name = 'owner';

-- Demote integration-specific roles: no longer protected system roles.
-- They remain in the UI (visible, assignable, now deletable).
UPDATE roles SET system_role = FALSE
WHERE name IN ('recordings_admin', 'uploads_admin', 'channel_lead');

-- admin: grant all permissions (functionally same as owner).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'admin'
ON CONFLICT DO NOTHING;

-- member: reset to the baseline permission set.
-- (resources.view, channels.request_access, calendar.view, quick_links.view, library.view)
-- Removes legislation.view, meetings.view, work_items.create that earlier migrations
-- granted to the former viewer role.
DELETE FROM role_permissions WHERE role_id = (SELECT id FROM roles WHERE name = 'member');

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'member'
  AND p.key IN (
    'resources.view',
    'channels.request_access',
    'calendar.view',
    'quick_links.view',
    'library.view'
  )
ON CONFLICT DO NOTHING;
