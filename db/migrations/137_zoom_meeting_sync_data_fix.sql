-- Fix zoom.meeting_sync_data schema mismatch.
--
-- Migration 112 created this table as a per-integration sync-state table
-- (last_sync_at, sync_token, sentinel_user_id), but integrations/zoom/store.go
-- uses it as a per-job results table (job_id, meetings_imported, etc.).
-- The 112 columns are never read by any code; the store.go columns are active.
-- The INSERT in CreateMeetingSyncData fails because its columns don't exist.
--
-- Drop and recreate with the schema the code expects. The dropped columns hold
-- no data the application reads. Safe on fresh installs (112 creates, 137 fixes)
-- and existing installs (table is dropped and rebuilt).
DROP TABLE IF EXISTS zoom.meeting_sync_data;

CREATE TABLE IF NOT EXISTS zoom.meeting_sync_data (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id              UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id      UUID NOT NULL REFERENCES integrations(id),
    meetings_imported   INTEGER NOT NULL DEFAULT 0,
    meetings_updated    INTEGER NOT NULL DEFAULT 0,
    meetings_deleted    INTEGER NOT NULL DEFAULT 0,
    occurrences_synced  INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
