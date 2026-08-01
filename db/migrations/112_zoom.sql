-- Consolidated migration: Zoom meeting scheduling, occurrences, and sync state.
-- Replaces migrations: 044, 045, 046 (schema parts), 048, 049 (schema parts), 053, 064.
-- Produces the final state directly — no intermediate tables, DROPs, or RENAMEs.

--------------------------------------------------------------------
-- Scheduled meetings: one row per Zoom meeting or recurring series.
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS zoom.scheduled_meetings (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id           UUID NOT NULL REFERENCES integrations(id),
    meeting_uuid             TEXT UNIQUE NOT NULL,
    meeting_topic            TEXT NOT NULL,
    meeting_date             TIMESTAMPTZ NOT NULL,
    host_email               TEXT NOT NULL,
    scheduled_occurrence_id  UUID,
    is_private               BOOLEAN NOT NULL DEFAULT FALSE,
    skip_upload              BOOLEAN NOT NULL DEFAULT FALSE,
    send_reminder            BOOLEAN NOT NULL DEFAULT TRUE,
    reminder_sent_at         TIMESTAMPTZ,
    created_at               TIMESTAMPTZ DEFAULT NOW(),
    updated_at               TIMESTAMPTZ DEFAULT NOW()
);

--------------------------------------------------------------------
-- Occurrences: one row per time slot (including single slot for one-off meetings).
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS zoom.meeting_occurrences (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    scheduled_meeting_id  UUID NOT NULL REFERENCES zoom.scheduled_meetings(id) ON DELETE CASCADE,
    occurrence_id         TEXT,
    start_time            TIMESTAMPTZ NOT NULL,
    end_time              TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ DEFAULT NOW()
);

--------------------------------------------------------------------
-- Sync state: one row per Zoom integration instance.
--------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS zoom.meeting_sync_data (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id    UUID NOT NULL REFERENCES integrations(id),
    last_sync_at      TIMESTAMPTZ,
    sync_token        TEXT,
    sentinel_user_id  UUID REFERENCES users(id),
    created_at        TIMESTAMPTZ DEFAULT NOW(),
    updated_at        TIMESTAMPTZ DEFAULT NOW()
);

--------------------------------------------------------------------
-- FK from recording_data.scheduled_occurrence_id → meeting_occurrences.
-- Column was created in 111; FK is added here because meeting_occurrences
-- is created in this migration (runs after 111).
--------------------------------------------------------------------
DO $$ BEGIN
    ALTER TABLE zoom.recording_data
        ADD CONSTRAINT fk_recording_data_scheduled_occurrence
        FOREIGN KEY (scheduled_occurrence_id) REFERENCES zoom.meeting_occurrences(id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
