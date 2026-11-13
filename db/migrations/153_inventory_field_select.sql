-- Custom fields can be plain text (the default) or a dropdown ("select") whose values come from a fixed list,
-- so every item uses the same spelling. Values stay strings in items.metadata.

ALTER TABLE inventory.field_definitions
    ADD COLUMN IF NOT EXISTS kind    TEXT NOT NULL DEFAULT 'text' CHECK (kind IN ('text', 'select')),
    ADD COLUMN IF NOT EXISTS options TEXT[] NOT NULL DEFAULT '{}';
