-- Feed sync: track when each syndicated calendar's feed was last synced.
--
-- The sync job (jobs/calendar_feed_sync, every 30m) fetches each feed calendar's
-- ICS and reconciles stored rows by source UID. Feed calendars keep rendering
-- from calendar_events; the feed URL remains the source of truth.
--
-- Idempotent throughout (see 147 for the legacy-DB rationale).

ALTER TABLE calendars ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ;

-- Reconciliation looks up rows by calendar_id, which the existing
-- UNIQUE (calendar_id, title, start_time) index already serves (leading column);
-- no additional index is needed.
