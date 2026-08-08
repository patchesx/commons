-- Legistar client subdomains as a configurable integration setting.
-- Previously hard-coded to 'jacksonco' via the legislative_bodies seed in 114;
-- the collection of subdomains is now managed on the integrations page.

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES
    ('legistar', 'clients', 'Client Subdomains', 'Legistar client subdomains to track, one per row (e.g. jacksonco, kcmo).', FALSE, FALSE)
ON CONFLICT DO NOTHING;

-- Seed default clients from existing legislative bodies for backward compatibility
-- so deployments already syncing Legistar continue without manual reconfiguration.
INSERT INTO config_store (service, key, value, sensitive)
SELECT 'legistar', 'clients', agg.clients, false
FROM (
    SELECT string_agg(legistar_client, E'\n' ORDER BY legistar_client) AS clients
    FROM legislative_bodies
    WHERE data_source = 'legistar' AND legistar_client IS NOT NULL
) agg
WHERE agg.clients IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM config_store WHERE service = 'legistar' AND key = 'clients');
