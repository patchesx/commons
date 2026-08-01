-- Per-job sync results for the OpenStates and Legistar bill sync jobs.
-- Referenced by legislation/sync_store.go (Create/Get/Update for each source).
-- Schemas are created by core migration 104; guarded here for safety.
-- Mirrors the slack.member_sync_data / zoom.recording_data job-data pattern.
CREATE SCHEMA IF NOT EXISTS openstates;
CREATE SCHEMA IF NOT EXISTS legistar;

CREATE TABLE IF NOT EXISTS openstates.sync_data (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id  UUID NOT NULL REFERENCES integrations(id),
    bills_imported  INTEGER NOT NULL DEFAULT 0,
    bills_updated   INTEGER NOT NULL DEFAULT 0,
    bills_skipped   INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS legistar.sync_data (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id  UUID NOT NULL REFERENCES integrations(id),
    bills_imported  INTEGER NOT NULL DEFAULT 0,
    bills_updated   INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
