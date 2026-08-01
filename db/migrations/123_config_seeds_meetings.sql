-- Migration 123: Consolidated config seeds — meetings, Zoom, and scheduler config.
-- Seeds config_schema entries for Zoom credentials, meeting scheduling limits,
-- meeting reminders, and background job scheduling.
-- Also seeds default config_store values for scheduler jobs.

-- ============================================================================
-- Zoom integration config_schema entries
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    -- Zoom Server-to-Server OAuth credentials
    ('zoom', 'webhook_secret',    'Webhook App Secret Token',
     'Secret Token from the Zoom Marketplace Webhook Only app — used to verify incoming recording.completed webhook request signatures',
     TRUE, FALSE),

    ('zoom', 'account_id',        'S2S App Account ID',
     'Account ID from the Zoom Marketplace Server-to-Server OAuth app credentials page',
     FALSE, FALSE),

    ('zoom', 'api_client_id',     'S2S App Client ID',
     'Client ID from the Zoom Marketplace Server-to-Server OAuth app credentials page',
     FALSE, FALSE),

    ('zoom', 'api_client_secret', 'S2S App Client Secret',
     'Client Secret from the Zoom Marketplace Server-to-Server OAuth app credentials page',
     TRUE, FALSE),

    ('zoom', 's2s_secret_token',  'S2S App Secret Token',
     'Secret Token from the Feature tab of the Zoom Marketplace S2S OAuth app. Not used by current webhook validation (which uses the Webhook Only app secret). Store here for reference in case event subscriptions are added to the S2S app later.',
     TRUE, FALSE),

    -- Feature toggles
    ('zoom', 'delete_after_upload', 'Delete After Upload',
     'Set to "true" to trash Zoom cloud recordings after a successful YouTube upload. Default: disabled. Uses action=trash (recoverable from Zoom trash for 30 days).',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- Copy Zoom entries to integration_config_schema (migration 033 pattern).
INSERT INTO integration_config_schema (integration_type, key, label, description, sensitive, required)
SELECT 'zoom', key, label, description, sensitive, required
FROM config_schema WHERE service = 'zoom'
ON CONFLICT DO NOTHING;

-- ============================================================================
-- Meeting scheduling limits
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('meetings', 'max_scheduled',        'Maximum Scheduled Events',
     'Maximum number of upcoming scheduled events (one-off meetings and individual occurrences of recurring meetings combined). Leave blank for no limit.',
     FALSE, FALSE),

    ('meetings', 'min_advance_minutes',  'Minimum Advance Notice (minutes)',
     'Minimum minutes before start time required to schedule (default 15).',
     FALSE, FALSE),

    ('meetings', 'max_duration_minutes', 'Maximum Duration (minutes)',
     'Maximum allowed duration in minutes (default 1440 = 24h).',
     FALSE, FALSE),

    ('meetings', 'allow_overlap',        'Allow Overlapping Meetings',
     'Set to "true" to allow scheduling meetings whose times overlap with existing ones.',
     FALSE, FALSE),

    ('meetings', 'max_overlap',          'Maximum Concurrent Events',
     'When overlapping meetings are allowed, the maximum number of other meetings that may overlap a new one (default 1, meaning 2 total concurrent). Set to 0 to require exclusive time slots even when overlap is enabled. Has no effect when Allow Overlapping Meetings is off.',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- Meeting reminders (sync-imported meetings)
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('meetings', 'sync_reminder_enabled', 'Reminders for Sync-Imported Meetings',
     'Set to "true" to send reminders for meetings imported from Zoom (not scheduled via the bot).',
     FALSE, FALSE),

    ('meetings', 'sync_reminder_channel', 'Sync Reminder Channel',
     'Slack channel ID to receive reminders for sync-imported meetings. Required when sync reminders are enabled.',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- Background job scheduler config (schema + defaults)
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('jobs', 'zoom_meeting_sync_enabled',          'Zoom Meeting Sync',
     'Enable automatic syncing of Zoom meetings on schedule.', FALSE, FALSE),
    ('jobs', 'zoom_meeting_sync_interval_minutes', 'Zoom Meeting Sync Interval',
     'How often to sync Zoom meetings (minutes). Default: 15.', FALSE, FALSE),

    ('jobs', 'legislation_sync_enabled',           'Legislation Sync',
     'Enable automatic legislation sync on schedule.', FALSE, FALSE),
    ('jobs', 'legislation_sync_interval_minutes',  'Legislation Sync Interval',
     'How often to run legislation sync (minutes). Default: 1440 (24h).', FALSE, FALSE),

    ('jobs', 'slack_user_sync_enabled',            'Slack User Sync',
     'Enable automatic Slack user sync on schedule.', FALSE, FALSE),
    ('jobs', 'slack_user_sync_interval_minutes',   'Slack User Sync Interval',
     'How often to sync Slack users (minutes). Default: 1440 (24h).', FALSE, FALSE),

    ('jobs', 'meeting_reminders_enabled',          'Meeting Reminders',
     'Enable automatic meeting reminder checks.', FALSE, FALSE),
    ('jobs', 'meeting_reminders_interval_minutes', 'Meeting Reminders Interval',
     'How often to check for upcoming meeting reminders (minutes). Default: 5.', FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- Default config_store values for scheduler jobs.
INSERT INTO config_store (service, key, value, sensitive) VALUES
    ('jobs', 'zoom_meeting_sync_enabled',          'true',  FALSE),
    ('jobs', 'zoom_meeting_sync_interval_minutes', '15',    FALSE),
    ('jobs', 'legislation_sync_enabled',           'true',  FALSE),
    ('jobs', 'legislation_sync_interval_minutes',  '1440',  FALSE),
    ('jobs', 'slack_user_sync_enabled',            'true',  FALSE),
    ('jobs', 'slack_user_sync_interval_minutes',   '1440',  FALSE),
    ('jobs', 'meeting_reminders_enabled',          'true',  FALSE),
    ('jobs', 'meeting_reminders_interval_minutes', '5',     FALSE)
ON CONFLICT (service, key) DO NOTHING;
