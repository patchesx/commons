-- Consolidated migration: Calendar system and events.
-- Replaces migrations: 032, 083, 085, 086.
-- Produces the final state directly — no intermediate tables, DROPs, or RENAMEs.

--------------------------------------------------------------------
-- Calendars (public schema).
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS calendars (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL,
    slug          TEXT UNIQUE,
    description   TEXT,
    ical_url      TEXT,
    timezone      TEXT NOT NULL DEFAULT '',
    display_order INT NOT NULL DEFAULT 0,
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW()
);

--------------------------------------------------------------------
-- Calendar events.
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS calendar_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    calendar_id UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    uid         TEXT,
    title       TEXT NOT NULL,
    description TEXT,
    location    TEXT,
    url         TEXT,
    start_time  TIMESTAMPTZ NOT NULL,
    end_time    TIMESTAMPTZ,
    all_day     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (calendar_id, title, start_time)
);

--------------------------------------------------------------------
-- Permission: calendar.manage
--------------------------------------------------------------------
INSERT INTO permissions (key, description) VALUES
    ('calendar.manage', 'Create, edit, and delete events on manually-managed calendars')
ON CONFLICT (key) DO NOTHING;

-- Assign calendar.manage to the owner role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'owner'
  AND p.key = 'calendar.manage'
ON CONFLICT DO NOTHING;
