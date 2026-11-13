-- Databases that ran 152 before it had the seq column need it added; on a fresh database this does nothing.
ALTER TABLE inventory.accessories
    ADD COLUMN IF NOT EXISTS seq BIGINT GENERATED ALWAYS AS IDENTITY;
