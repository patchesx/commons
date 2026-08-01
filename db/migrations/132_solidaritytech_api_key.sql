-- Migration 132: Solidarity.Tech API integration config.
-- Removes the orphaned new_member_channel config key (the dead webhook handlers that used it
-- have been removed; the integration is now API-driven) and adds the API key used by the
-- SolidarityTech plugin's pipeline actions.

-- Drop the dead notification-channel config key everywhere it was seeded (115/128).
DELETE FROM config_store        WHERE service = 'solidaritytech' AND key = 'new_member_channel';
DELETE FROM integration_config_schema WHERE integration_type = 'solidaritytech' AND key = 'new_member_channel';
DELETE FROM config_schema       WHERE service = 'solidaritytech' AND key = 'new_member_channel';

-- SolidarityTech API key (Bearer token). Generated under Settings → API Keys in Solidarity Tech.
INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES
    ('solidaritytech', 'api_key', 'API Key',
     'Solidarity Tech API key (Bearer token). Generate one under Settings → API Keys in your Solidarity Tech account. Used by the SolidarityTech pipeline actions to look up profiles and update custom properties.',
     TRUE, FALSE)
ON CONFLICT DO NOTHING;

-- Surface the API key on the Solidarity Tech integration detail page (Zoom/Slack pattern).
INSERT INTO integration_config_schema (integration_type, key, label, description, sensitive, required)
SELECT 'solidaritytech', key, label, description, sensitive, required
FROM config_schema WHERE service = 'solidaritytech'
ON CONFLICT DO NOTHING;
