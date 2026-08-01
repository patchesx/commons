-- Migration 125: Consolidated config seeds — UI appearance config.
-- Seeds config_schema entries for the admin web UI theme and branding.

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    -- Live UI settings
    ('ui', 'org_name',    'Organization Name',  'Displayed in the sidebar and page titles',                             FALSE, FALSE),
    ('ui', 'accent_hex',  'Brand Color',        'Primary accent color as a CSS hex string, e.g. #EC1C24',               FALSE, FALSE),
    ('ui', 'favicon_url', 'Favicon',            'URL to favicon image, or base64 data URI for an uploaded image file',  FALSE, FALSE),

    ('ui', 'sidebar_hex',      'Sidebar Background Color', '6-digit hex color for the admin sidebar background', FALSE, FALSE),
    ('ui', 'sidebar_text_hex', 'Sidebar Text Color',       '6-digit hex color for sidebar text and links',       FALSE, FALSE),

    ('ui', 'bg_hex',      'Page Background (Light)', '6-digit hex color for the admin page background in light mode', FALSE, FALSE),
    ('ui', 'bg_dark_hex', 'Page Background (Dark)',  '6-digit hex color for the admin page background in dark mode',  FALSE, FALSE),

    -- Draft UI settings (unsaved changes)
    ('ui_draft', 'org_name',         'Organization Name (draft)',          NULL, FALSE, FALSE),
    ('ui_draft', 'accent_hex',       'Brand Color (draft)',                NULL, FALSE, FALSE),
    ('ui_draft', 'favicon_url',      'Favicon (draft)',                    NULL, FALSE, FALSE),
    ('ui_draft', 'sidebar_hex',      'Sidebar Background Color (draft)',   NULL, FALSE, FALSE),
    ('ui_draft', 'sidebar_text_hex', 'Sidebar Text Color (draft)',         NULL, FALSE, FALSE),
    ('ui_draft', 'bg_hex',           'Page Background Light (draft)',      NULL, FALSE, FALSE),
    ('ui_draft', 'bg_dark_hex',      'Page Background Dark (draft)',       NULL, FALSE, FALSE)

ON CONFLICT DO NOTHING;
