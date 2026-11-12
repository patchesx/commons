-- Recurring events for manually-managed calendars.
--
-- A series is a calendar_events row whose rrule holds an iCal RRULE string
-- (no DTSTART — the row's start_time is the first occurrence). Per-occurrence
-- edits and skips (RECURRENCE-ID / EXDATE semantics) live in their own table
-- so the series template stays clean.
--
-- Idempotent throughout (see 147: legacy DBs may predate consolidated shapes).

ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS rrule TEXT;

CREATE TABLE IF NOT EXISTS calendar_event_overrides (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    series_id         UUID NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
    occurrence_start  TIMESTAMPTZ NOT NULL,   -- the series occurrence being overridden (RECURRENCE-ID)
    title             TEXT,
    description       TEXT,
    location          TEXT,
    url               TEXT,
    start_time        TIMESTAMPTZ,            -- override fields are full replacements
    end_time          TIMESTAMPTZ,
    all_day           BOOLEAN,
    deleted           BOOLEAN NOT NULL DEFAULT FALSE,  -- true = skipped occurrence (EXDATE)
    created_at        TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (series_id, occurrence_start)
);
