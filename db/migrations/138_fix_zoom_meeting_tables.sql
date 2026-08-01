-- Fix zoom.scheduled_meetings and zoom.meeting_occurrences schema mismatch.
--
-- Migration 112 (before it was corrected) created these tables with column
-- names and a column set that don't match what store/meetings.go expects.
-- On pre-consolidation installs, 112 was a no-op (CREATE TABLE IF NOT EXISTS)
-- and the correct schema from the old migrations is already in place.
-- On new installs that ran the broken 112, the tables have the wrong schema.
--
-- This migration detects the broken schema (presence of 'scheduled_meeting_id'
-- on meeting_occurrences) and rebuilds both tables. On installs with the
-- correct schema, every statement is a no-op.
--
-- Also adds the missing 'phase' column to zoom.recording_data, which was
-- omitted from consolidated migration 111 but is used by integrations/zoom/store.go.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'zoom'
          AND table_name = 'meeting_occurrences'
          AND column_name = 'scheduled_meeting_id'
    ) THEN
        -- Broken 112 schema detected. The app cannot have successfully stored
        -- any data (all inserts/queries reference non-existent columns), so
        -- it is safe to drop and recreate.
        ALTER TABLE IF EXISTS zoom.recording_data
            DROP CONSTRAINT IF EXISTS fk_recording_data_scheduled_occurrence;
        DROP TABLE IF EXISTS zoom.meeting_occurrences;
        DROP TABLE IF EXISTS zoom.scheduled_meetings;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS zoom.scheduled_meetings (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id        UUID NOT NULL REFERENCES integrations(id),
    zoom_meeting_id       BIGINT NOT NULL,
    host_email            TEXT NOT NULL,
    topic                 TEXT NOT NULL,
    agenda                TEXT NOT NULL DEFAULT '',
    duration_minutes      INTEGER NOT NULL DEFAULT 0,
    timezone              TEXT NOT NULL DEFAULT 'UTC',
    password              TEXT,
    join_url              TEXT NOT NULL DEFAULT '',
    start_url             TEXT,
    is_recurring          BOOLEAN NOT NULL DEFAULT FALSE,
    is_private            BOOLEAN NOT NULL DEFAULT FALSE,
    recurrence_pattern    JSONB,
    settings              JSONB,
    created_by            UUID REFERENCES users(id),
    send_reminder         BOOLEAN NOT NULL DEFAULT TRUE,
    skip_upload           BOOLEAN NOT NULL DEFAULT FALSE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS zoom.meeting_occurrences (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meeting_id            UUID NOT NULL REFERENCES zoom.scheduled_meetings(id) ON DELETE CASCADE,
    zoom_occurrence_id    TEXT,
    start_time            TIMESTAMPTZ NOT NULL,
    duration_minutes      INTEGER NOT NULL DEFAULT 0,
    status                TEXT NOT NULL DEFAULT 'available',
    reminder_sent_at      TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Recreate FK from recording_data (dropped above if broken schema was detected).
DO $$ BEGIN
    ALTER TABLE zoom.recording_data
        ADD CONSTRAINT fk_recording_data_scheduled_occurrence
        FOREIGN KEY (scheduled_occurrence_id) REFERENCES zoom.meeting_occurrences(id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Add missing phase column to recording_data (omitted from migration 111).
ALTER TABLE zoom.recording_data ADD COLUMN IF NOT EXISTS phase TEXT;
