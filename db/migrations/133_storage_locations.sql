-- 133: Storage locations — named destinations for recording file backups.
-- Each location maps a human-readable name to a path on a storage integration
-- (Google Drive, S3, Nextcloud). Referenced by the recording pipeline config
-- and pipeline action parameters (storage_location_select).

CREATE TABLE IF NOT EXISTS storage_locations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id  UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    path            TEXT NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);
