package store

import (
	"context"
	"crypto/rand"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// inventoryCodeAlphabet has 32 characters (no I, O, 0, or 1) so a random byte maps to a
// character without modulo bias.
const inventoryCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	inventoryCodeLen      = 6
	inventoryCodeAttempts = 10
)

// NewInventoryCode returns a random 6-character item code from an unambiguous alphabet.
func NewInventoryCode() string {
	b := make([]byte, inventoryCodeLen)
	rand.Read(b)
	for i := range b {
		b[i] = inventoryCodeAlphabet[b[i]&31]
	}
	return string(b)
}

// NormalizeInventoryCode turns user input into the stored form: uppercase, with spaces and dashes removed.
func NormalizeInventoryCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(s))
}

// FormatInventoryCode renders a stored code for display, for example K7M4QX as K7M-4QX.
func FormatInventoryCode(code string) string {
	if len(code) != inventoryCodeLen {
		return code
	}
	return code[:3] + "-" + code[3:]
}

// RotateInventoryItemCodeRandom replaces an item's code with a freshly generated one and returns it,
// retrying on collisions. The old code stops working immediately.
func RotateInventoryItemCodeRandom(ctx context.Context, pool *pgxpool.Pool, id string) (string, error) {
	var err error
	for range inventoryCodeAttempts {
		code := NewInventoryCode()
		err = RotateInventoryItemCode(ctx, pool, id, code)
		if err == nil {
			return code, nil
		}
		if err != ErrInventoryCodeTaken {
			return "", err
		}
	}
	return "", err
}

// CreateInventoryItemWithNewCode creates an item with a freshly generated code, retrying on collisions.
// Returns ErrInventoryCodeTaken only if every attempt collided.
func CreateInventoryItemWithNewCode(ctx context.Context, pool *pgxpool.Pool, in InventoryItemInput) (*InventoryItem, error) {
	var err error
	for range inventoryCodeAttempts {
		var it *InventoryItem
		it, err = CreateInventoryItem(ctx, pool, in, NewInventoryCode())
		if err != ErrInventoryCodeTaken {
			return it, err
		}
	}
	return nil, err
}
