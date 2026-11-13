package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Limits on an item's accessories.
const (
	MaxInventoryAccessoryNameLen = 200
	MaxInventoryAccessories      = 100
)

// ErrInventoryAccessoryExists is returned when the item already has an accessory with that name (any case).
var ErrInventoryAccessoryExists = errors.New("inventory accessory already exists")

// ErrInventoryAccessoryInvalid is returned for a blank or over-long accessory name.
var ErrInventoryAccessoryInvalid = errors.New("inventory accessory name is invalid")

// ErrInventoryTooManyAccessories is returned when an item is at MaxInventoryAccessories.
var ErrInventoryTooManyAccessories = errors.New("inventory item has too many accessories")

// InventoryAccessory is a named part of an item. It has no code or holder of its own.
type InventoryAccessory struct {
	ID        string
	ItemID    string
	Name      string
	CreatedAt time.Time
}

// ListInventoryAccessories returns an item's accessories in the order they were added.
func ListInventoryAccessories(ctx context.Context, pool *pgxpool.Pool, itemID string) ([]InventoryAccessory, error) {
	m, err := ListInventoryAccessoriesFor(ctx, pool, []string{itemID})
	return m[itemID], err
}

// ListInventoryAccessoriesFor returns the accessories of several items at once, keyed by item ID.
func ListInventoryAccessoriesFor(ctx context.Context, pool *pgxpool.Pool, itemIDs []string) (map[string][]InventoryAccessory, error) {
	out := map[string][]InventoryAccessory{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT id, item_id, name, created_at FROM inventory.accessories
		WHERE item_id = ANY($1::uuid[])
		ORDER BY seq`, itemIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a InventoryAccessory
		if err := rows.Scan(&a.ID, &a.ItemID, &a.Name, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[a.ItemID] = append(out[a.ItemID], a)
	}
	return out, rows.Err()
}

// AddInventoryAccessory adds an accessory to an active item. The name is trimmed. Returns ErrNotFound if the
// item is missing or archived.
func AddInventoryAccessory(ctx context.Context, pool *pgxpool.Pool, itemID, name string) (*InventoryAccessory, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxInventoryAccessoryNameLen {
		return nil, ErrInventoryAccessoryInvalid
	}
	var count int
	var archived bool
	err := pool.QueryRow(ctx, `
		SELECT archived_at IS NOT NULL, (SELECT COUNT(*) FROM inventory.accessories WHERE item_id = i.id)
		FROM inventory.items i WHERE i.id = $1`, itemID).Scan(&archived, &count)
	if err != nil || archived {
		return nil, ErrNotFound
	}
	if count >= MaxInventoryAccessories {
		return nil, ErrInventoryTooManyAccessories
	}
	var a InventoryAccessory
	err = pool.QueryRow(ctx, `
		INSERT INTO inventory.accessories (item_id, name) VALUES ($1, $2)
		RETURNING id, item_id, name, created_at`, itemID, name).Scan(&a.ID, &a.ItemID, &a.Name, &a.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, ErrInventoryAccessoryExists
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// DeleteInventoryAccessory removes an accessory from an active item. Returns ErrNotFound if it isn't there or
// the item is archived.
func DeleteInventoryAccessory(ctx context.Context, pool *pgxpool.Pool, itemID, accessoryID string) error {
	tag, err := pool.Exec(ctx, `
		DELETE FROM inventory.accessories a USING inventory.items i
		WHERE a.id = $2 AND a.item_id = $1 AND i.id = a.item_id AND i.archived_at IS NULL`, itemID, accessoryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
