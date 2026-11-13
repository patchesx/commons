-- Accessories: the named parts of one inventory item (a PA system's speaker, stand, cables, ...).
-- They have no code, holder, or reminders of their own; they live on the item's detail page only.

CREATE TABLE IF NOT EXISTS inventory.accessories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq         BIGINT GENERATED ALWAYS AS IDENTITY,  -- insertion order; created_at ties inside one transaction
    item_id     UUID NOT NULL REFERENCES inventory.items(id) ON DELETE CASCADE,
    name        TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS inventory_accessories_item_name_idx
    ON inventory.accessories (item_id, lower(name));
