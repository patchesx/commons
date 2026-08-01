-- Migration 134: Config seed for app.base_url.
-- The Settings page General card and the Slack setup handler both read
-- app.base_url, but no migration ever seeded the config_schema row.
-- Without it the General card renders an unlabeled field.

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES
    ('app', 'base_url', 'Base URL',
     'Public URL where this instance is reachable (e.g. https://commons.example.org). Used to build webhook URLs for integrations like Slack.',
     FALSE, TRUE)
ON CONFLICT DO NOTHING;
