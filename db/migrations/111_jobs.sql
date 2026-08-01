-- Consolidated migration: Jobs system and integration-specific job extension tables.
-- Replaces migrations: 003, 009, 022, 026 (table parts), 034, 035, 036, 039, 074.
-- Produces the final state directly — no intermediate tables, DROPs, or RENAMEs.

--------------------------------------------------------------------
-- Generic pipeline jobs table (public schema).
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS jobs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    error_message TEXT,
    feature       TEXT NOT NULL DEFAULT 'recording_pipeline',
    trigger       TEXT NOT NULL DEFAULT 'webhook',
    phase         TEXT,
    started_at    TIMESTAMPTZ DEFAULT NOW(),
    completed_at  TIMESTAMPTZ
);

--------------------------------------------------------------------
-- Zoom recording pipeline source data (zoom schema).
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS zoom.recording_data (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id                   UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id           UUID NOT NULL REFERENCES integrations(id),
    meeting_uuid             TEXT UNIQUE NOT NULL,
    meeting_topic            TEXT NOT NULL,
    meeting_date             TIMESTAMPTZ NOT NULL,
    host_email               TEXT,
    duration_secs            INTEGER,
    recording_deleted_at     TIMESTAMPTZ,
    scheduled_occurrence_id  UUID
    -- FK to zoom.meeting_occurrences(id) added in 112 (table created there).
);

--------------------------------------------------------------------
-- YouTube upload output data (youtube schema).
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS youtube.upload_data (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id           UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id   UUID NOT NULL REFERENCES integrations(id),
    video_id         TEXT,
    title            TEXT,
    meeting_topic    TEXT,
    meeting_date     TIMESTAMPTZ,
    duration_secs    INT
);

--------------------------------------------------------------------
-- Google Drive backup output data (gdrive schema).
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS gdrive.backup_data (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id           UUID NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    integration_id   UUID NOT NULL REFERENCES integrations(id),
    folder_id        TEXT,
    folder_url       TEXT,
    created_at       TIMESTAMPTZ DEFAULT NOW()
);
