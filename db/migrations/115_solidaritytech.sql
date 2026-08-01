-- Consolidated Solidarity.Tech integration — Comrade Relationship Manager.
-- Combines: 054_solidaritytech_integration.sql (schema + integration config)

CREATE SCHEMA IF NOT EXISTS solidaritytech;

-- Integration registration
INSERT INTO integrations (type, name)
SELECT 'solidaritytech', 'Solidarity.Tech'
WHERE NOT EXISTS (SELECT 1 FROM integrations WHERE type = 'solidaritytech');

-- Notification channel config for CRM webhook-driven notifications
INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES (
    'solidaritytech',
    'new_member_channel',
    'Notification Channel',
    'Slack channel ID for Comrade Relationship Manager notifications (new members and contacts)',
    false,
    false
)
ON CONFLICT DO NOTHING;
