-- Migration 121: Consolidated config seeds — bot and Google feature config.
-- Seeds config_schema entries for the Slack bot UI and Google OAuth credentials.

-- ============================================================================
-- Bot / App Home config
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES
    ('bot', 'title',           'Bot Title',       'Heading displayed at the top of the App Home tab. Default: "Org Operations Bot"',                                               FALSE, FALSE),
    ('bot', 'welcome_message', 'Welcome Message', 'Subtitle shown below the title in the App Home tab. Default: "Access team resources, upcoming events, legislation tracking, and more."', FALSE, FALSE),
    ('bot', 'announcement',    'Announcement',    'Optional message shown prominently to all members in App Home. Leave blank to hide.',                                       FALSE, FALSE),
    ('bot', 'admin_url',       'Admin Panel URL', 'Full URL to the web admin panel (e.g. https://your-domain.com/admin). If set, an Admin button appears in App Home for users with admin permissions.', FALSE, FALSE)
ON CONFLICT DO NOTHING;

-- ============================================================================
-- Google OAuth feature config
-- Consolidated from youtube/* during feature reorganize (migration 042).
-- 'google' service keys remain in config_store at the feature level;
-- only google/allowed_domain is also registered in integration_config_schema.
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES
    ('google', 'web_client_id',     'Web OAuth Client ID',     'OAuth 2.0 Client ID from a Web application credential in GCP — used for the web-based Google auth flow',     FALSE, FALSE),
    ('google', 'web_client_secret', 'Web OAuth Client Secret', 'OAuth 2.0 Client Secret from a Web application credential in GCP — used for the web-based Google auth flow', TRUE,  FALSE),
    ('google', 'client_id',         'Client ID',               'Google OAuth 2.0 Client ID from Google Cloud Console',                                                       FALSE, FALSE),
    ('google', 'client_secret',     'Client Secret',           'Google OAuth 2.0 Client Secret from Google Cloud Console',                                                   TRUE,  FALSE),
    ('google', 'refresh_token',     'Refresh Token',           'OAuth refresh token — obtained via Google OAuth flow',                                                       TRUE,  FALSE),
    ('google', 'connected_email',   'Connected Account',       'Google account authorized for YouTube and Drive access (set automatically via OAuth flow)',                   FALSE, FALSE),
    ('google', 'allowed_domain',    'Allowed OAuth Domain',    'If set, only Google accounts from this domain can complete the OAuth flow (e.g. org.org)',                    FALSE, FALSE)
ON CONFLICT DO NOTHING;

-- ============================================================================
-- Integration config schema for Google
-- ============================================================================

INSERT INTO integration_config_schema (integration_type, key, label, description, sensitive, required)
VALUES ('google', 'allowed_domain', 'Allowed OAuth Domain',
        'Only Google accounts from this domain can complete the OAuth flow (e.g. org.org)',
        FALSE, FALSE)
ON CONFLICT DO NOTHING;
