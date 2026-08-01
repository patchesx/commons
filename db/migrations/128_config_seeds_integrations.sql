-- Migration 128: Consolidated config seeds — optional integration services.
-- Seeds integration instances (disabled by default), config_schema entries,
-- and integration_config_schema entries for Discord, Matrix, Nextcloud, S3,
-- Vimeo, and Solidarity.Tech.

-- ============================================================================
-- Integration instances (disabled by default)
-- ============================================================================

INSERT INTO integrations (type, name, enabled)
SELECT 'solidaritytech', 'Solidarity.Tech', FALSE
WHERE NOT EXISTS (SELECT 1 FROM integrations WHERE type = 'solidaritytech');

INSERT INTO integrations (type, name, enabled) VALUES ('nextcloud', 'Nextcloud', FALSE)
ON CONFLICT DO NOTHING;

INSERT INTO integrations (type, name, enabled) VALUES ('s3', 'S3 Storage', FALSE)
ON CONFLICT DO NOTHING;

INSERT INTO integrations (type, name, enabled) VALUES ('vimeo', 'Vimeo', FALSE)
ON CONFLICT DO NOTHING;

-- ============================================================================
-- Discord config_schema
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('discord', 'enabled',        'Enable Discord',
     'Set to "true" to activate the Discord integration.', FALSE, FALSE),
    ('discord', 'bot_token',      'Bot Token',
     'Discord bot token from the Developer Portal (Bot → Token).', TRUE, FALSE),
    ('discord', 'application_id', 'Application ID',
     'Discord application/client ID, used for slash command registration.', FALSE, FALSE),
    ('discord', 'public_key',     'Public Key',
     'Ed25519 public key for verifying incoming interaction requests.', FALSE, FALSE),
    ('discord', 'guild_id',       'Guild (Server) ID',
     'ID of the Discord server (guild) the bot manages. Right-click the server icon to copy it.', FALSE, FALSE),
    ('discord', 'command_name',   'Slash Command Name',
     'Name of the slash command members use to open the portal (e.g. "home"). Lowercase, no slash. Register it from the Integrations page after saving.', FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- Matrix config_schema
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('matrix', 'enabled',        'Enable Matrix Integration',
     'Set to "true" to enable the Matrix bot', FALSE, FALSE),
    ('matrix', 'homeserver',     'Homeserver URL',
     'Matrix homeserver URL, e.g. https://matrix.org', FALSE, FALSE),
    ('matrix', 'user_id',        'Bot User ID',
     'Matrix bot user ID, e.g. @orgbot:matrix.org', FALSE, FALSE),
    ('matrix', 'access_token',   'Access Token',
     'Bot access token (generated when bot account is created)', TRUE, FALSE),
    ('matrix', 'home_room_id',   'Home Room ID',
     'Room whose members are synced as Matrix users', FALSE, FALSE),
    ('matrix', 'command_prefix', 'Command Prefix',
     'Prefix character for bot commands (default: !)', FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- Nextcloud config_schema
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('nextcloud', 'enabled',      'Enable Nextcloud',
     'Set to "true" to activate the Nextcloud integration.', FALSE, FALSE),
    ('nextcloud', 'server_url',   'Server URL',
     'URL of your Nextcloud instance, e.g. https://cloud.example.org', FALSE, FALSE),
    ('nextcloud', 'username',     'Username',
     'Nextcloud username for the bot/service account.', FALSE, FALSE),
    ('nextcloud', 'app_password', 'App Password',
     'App password generated in Nextcloud (Settings → Security → App passwords). Not your login password.', TRUE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- S3 Storage config_schema
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('s3', 'enabled',           'Enable S3 Storage',
     'Set to "true" to activate the S3 storage integration.', FALSE, FALSE),
    ('s3', 'endpoint_url',      'Endpoint URL',
     'Custom endpoint for S3-compatible services (e.g. MinIO, Cloudflare R2). Leave blank for AWS S3.', FALSE, FALSE),
    ('s3', 'bucket',            'Bucket',
     'S3 bucket name where recordings will be uploaded.', FALSE, FALSE),
    ('s3', 'region',            'Region',
     'AWS region (e.g. us-east-1). Required for AWS; most S3-compatible services accept any non-empty value.', FALSE, FALSE),
    ('s3', 'access_key_id',     'Access Key ID',
     'AWS access key ID or equivalent credential for your S3-compatible service.', TRUE, FALSE),
    ('s3', 'secret_access_key', 'Secret Access Key',
     'AWS secret access key or equivalent credential for your S3-compatible service.', TRUE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- Vimeo config_schema
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('vimeo', 'access_token', 'Access Token',
     'Vimeo personal access token with upload scope.', TRUE, FALSE)

ON CONFLICT DO NOTHING;

-- ============================================================================
-- Solidarity.Tech config_schema
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('solidaritytech', 'new_member_channel', 'Notification Channel',
     'Slack channel ID for Comrade Relationship Manager notifications (new members and contacts)',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;
