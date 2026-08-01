-- Migration 122: Consolidated config seeds — recordings feature config.
-- Seeds config_schema entries for the recording pipeline.

INSERT INTO config_schema (service, key, label, description, sensitive, required, filterable) VALUES

    -- Feature toggles (moved from zoom/youtube during feature reorganize, migration 042)
    ('recordings', 'delete_after_upload', 'Delete After Upload',
     'Trash Zoom cloud recordings after a successful upload (recoverable for 30 days).', FALSE, FALSE, FALSE),

    ('recordings', 'youtube_enabled', 'Enable YouTube Upload',
     'Set to false to skip YouTube uploads and use storage as the sole destination.', FALSE, FALSE, FALSE),

    ('recordings', 'storage_location_id', 'Storage Destination',
     'Storage location to archive recording files to. Select from configured storage locations.', FALSE, FALSE, FALSE),

    -- Recording pipeline options
    ('recordings', 'min_duration_minutes', 'Minimum Recording Duration',
     'Recordings shorter than this many minutes will fail immediately (no upload or backup). Leave empty to allow any duration.',
     FALSE, FALSE, TRUE),

    ('recordings', 'upload_notification_channel', 'Upload Notification Channel',
     'Slack channel ID to post upload-complete notifications. When set, a single channel message is sent instead of individual DMs to all members with the jobs.view permission.',
     FALSE, FALSE, FALSE),

    ('recordings', 'skip_private_meetings', 'Skip Private Meetings',
     'Meetings marked as Private in Zoom will be ignored by the recording pipeline — no upload, backup, or job record will be created.',
     FALSE, FALSE, TRUE)

ON CONFLICT DO NOTHING;
