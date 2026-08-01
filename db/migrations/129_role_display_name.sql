-- 129: Add display_name to roles for human-readable labels in the UI.
-- `name` remains the stable machine identifier (referenced in code/migrations by string);
-- display_name is what the UI shows, falling back to name when empty.
ALTER TABLE roles ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';

-- Backfill system roles with readable labels.
UPDATE roles SET display_name = 'Owner'            WHERE name = 'owner';
UPDATE roles SET display_name = 'Recordings Admin' WHERE name = 'recordings_admin';
UPDATE roles SET display_name = 'Uploads Admin'    WHERE name = 'uploads_admin';
UPDATE roles SET display_name = 'Channel Lead'     WHERE name = 'channel_lead';
UPDATE roles SET display_name = 'Viewer'           WHERE name = 'viewer';
UPDATE roles SET display_name = 'Web Admin'        WHERE name = 'web_admin';
