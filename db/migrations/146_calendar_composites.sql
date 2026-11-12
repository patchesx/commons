-- Composite calendars: calendar kinds, membership graph, and color chips.
-- kind: 'standard' (manual or syndicated feed), 'composite' (merged view of members),
--       'personal' (per-user composite, Phase 2 — created from Slack).

ALTER TABLE calendars
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'
        CHECK (kind IN ('standard','composite','personal')),
    ADD COLUMN color TEXT;

-- Composites and personal calendars never carry a feed URL of their own.
ALTER TABLE calendars
    ADD CONSTRAINT calendars_kind_ical_check
    CHECK (kind = 'standard' OR ical_url IS NULL);

CREATE TABLE calendar_members (
    composite_id UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    member_id    UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (composite_id, member_id),
    CHECK (composite_id <> member_id)
);

CREATE INDEX idx_calendar_members_member ON calendar_members(member_id);

-- One personal calendar per user.
CREATE UNIQUE INDEX idx_personal_calendar_per_user
    ON calendars(created_by) WHERE kind = 'personal';
