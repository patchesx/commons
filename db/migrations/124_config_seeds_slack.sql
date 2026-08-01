-- Migration 124: Consolidated config seeds — Slack integration config.
-- Seeds config_schema entries for Slack bot credentials.

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('slack', 'bot_token',      'Bot Token',
     'Bot User OAuth Token from api.slack.com/apps (starts with xoxb-)',
     TRUE,  TRUE),

    ('slack', 'signing_secret', 'Signing Secret',
     'Signing Secret from the Slack app Basic Information page',
     TRUE,  TRUE)

ON CONFLICT DO NOTHING;

-- Copy Slack entries to integration_config_schema (migration 033 pattern).
INSERT INTO integration_config_schema (integration_type, key, label, description, sensitive, required)
SELECT 'slack', key, label, description, sensitive, required
FROM config_schema WHERE service = 'slack'
ON CONFLICT DO NOTHING;
