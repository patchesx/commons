-- Schema repair for legacy databases: bring calendars and calendar_events to the
-- final state declared by migration 105.
--
-- Migration 105 consolidated earlier migrations using CREATE TABLE IF NOT EXISTS,
-- which is a no-op on databases that predate the consolidation. Those databases
-- kept their original (pre-105) shapes and never gained the columns 105 declares.
-- Every statement below is idempotent: on databases created from 105 onward this
-- migration does nothing.
--
--   calendars.updated_at          — declared by 105, missing on legacy DBs
--   calendar_events.uid           — declared by 105, missing on legacy DBs
--   calendar_events.updated_at    — declared by 105, missing on legacy DBs
--   calendars.slug NOT NULL       — 105 declares it nullable; personal calendars
--                                    ("My Calendar") store NULL slugs

ALTER TABLE calendars ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW();
ALTER TABLE calendars ALTER COLUMN slug DROP NOT NULL;

ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS uid TEXT;
ALTER TABLE calendar_events ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW();
