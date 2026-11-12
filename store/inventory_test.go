package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
)

func TestInventoryPermissionsSeeded(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	keys := []string{permissions.InventoryView, permissions.InventoryManage, permissions.InventoryAdmin}

	for _, role := range []string{"admin", "owner"} {
		for _, key := range keys {
			var n int
			err := pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM role_permissions rp
				JOIN roles r ON r.id = rp.role_id
				JOIN permissions p ON p.id = rp.permission_id
				WHERE r.name = $1 AND p.key = $2`, role, key).Scan(&n)
			require.NoError(t, err)
			assert.Equal(t, 1, n, "role %s should have %s", role, key)
		}
	}

	var memberGrants int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE r.name = 'member' AND p.key LIKE 'inventory.%'`).Scan(&memberGrants)
	require.NoError(t, err)
	assert.Zero(t, memberGrants, "member role must not get inventory permissions")
}

func TestInventoryConfigSeeded(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	want := map[[2]string]string{
		{"inventory", "default_grace_days"}:           "5",
		{"inventory", "claim_rate_limit_per_hour"}:    "10",
		{"jobs", "inventory_verify_enabled"}:          "true",
		{"jobs", "inventory_verify_interval_minutes"}: "1440",
	}
	for k, v := range want {
		var got string
		err := pool.QueryRow(ctx,
			`SELECT value FROM config_store WHERE service = $1 AND key = $2`, k[0], k[1]).Scan(&got)
		require.NoError(t, err, "%v", k)
		assert.Equal(t, v, got, "%v", k)

		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM config_schema WHERE service = $1 AND key = $2`, k[0], k[1]).Scan(&n))
		assert.Equal(t, 1, n, "config_schema row for %v", k)
	}
}

func TestInventoryItemDefaultsAndConstraints(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	var verifyDays int
	var metadata string
	var graceNull bool
	err := pool.QueryRow(ctx, `
		INSERT INTO inventory.items (name, code) VALUES ('Projector', 'K7M4QX')
		RETURNING verify_every_days, metadata::text, verify_grace_days IS NULL`).Scan(&verifyDays, &metadata, &graceNull)
	require.NoError(t, err)
	assert.Equal(t, 7, verifyDays)
	assert.JSONEq(t, `{}`, metadata)
	assert.True(t, graceNull, "grace days default to NULL so the config default applies")

	_, err = pool.Exec(ctx, `INSERT INTO inventory.items (name, code) VALUES ('Dup', 'K7M4QX')`)
	assert.Error(t, err, "duplicate code must be rejected")

	_, err = pool.Exec(ctx, `INSERT INTO inventory.items (name, code) VALUES ('Short', 'ABC')`)
	assert.Error(t, err, "code must be 6 characters")

	_, err = pool.Exec(ctx, `INSERT INTO inventory.items (name, code) VALUES ('Lower', 'abcdef')`)
	assert.Error(t, err, "code must be uppercase")

	_, err = pool.Exec(ctx, `INSERT INTO inventory.items (name, code, verify_grace_days) VALUES ('Zero', 'ZZZZZZ', 0)`)
	assert.Error(t, err, "grace days must be positive")

	// Archived items keep their code reserved.
	_, err = pool.Exec(ctx, `UPDATE inventory.items SET archived_at = NOW() WHERE code = 'K7M4QX'`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO inventory.items (name, code) VALUES ('Reuse', 'K7M4QX')`)
	assert.Error(t, err, "archived item must still reserve its code")
}

func TestInventoryCustodyEventConstraints(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	var itemID, userID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO inventory.items (name, code) VALUES ('Laptop', 'LAP2TP') RETURNING id`).Scan(&itemID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (display_name) VALUES ('Holder') RETURNING id`).Scan(&userID))

	for _, status := range []string{"applied", "pending_unmatched", "linked", "disputed"} {
		_, err := pool.Exec(ctx, `
			INSERT INTO inventory.custody_events (item_id, user_id, source, status)
			VALUES ($1, $2, 'slack', $3)`, itemID, userID, status)
		assert.NoError(t, err, "status %s", status)
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO inventory.custody_events (item_id, source, status) VALUES ($1, 'slack', 'bogus')`, itemID)
	assert.Error(t, err, "unknown status must be rejected")

	_, err = pool.Exec(ctx, `
		INSERT INTO inventory.custody_events (item_id, source, status) VALUES ($1, 'carrier_pigeon', 'applied')`, itemID)
	assert.Error(t, err, "unknown source must be rejected")

	// Unmatched web-form claims have no user.
	_, err = pool.Exec(ctx, `
		INSERT INTO inventory.custody_events (item_id, raw_name, raw_email, source, status)
		VALUES ($1, 'Pat', 'pat@example.com', 'web_form', 'pending_unmatched')`, itemID)
	assert.NoError(t, err)

	// History is kept: an item with events cannot be hard-deleted.
	_, err = pool.Exec(ctx, `DELETE FROM inventory.items WHERE id = $1`, itemID)
	assert.Error(t, err, "items with history must be archived, not deleted")
}

func TestInventoryFieldDefinitionsAndIndexes(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := pool.Exec(ctx, `INSERT INTO inventory.field_definitions (key, label) VALUES ('serial', 'Serial Number')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO inventory.field_definitions (key, label) VALUES ('serial', 'Again')`)
	assert.Error(t, err, "field keys are unique")

	for _, idx := range []string{
		"user_identities_external_email_lower_idx",
		"inventory_items_metadata_idx",
	} {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM pg_indexes WHERE indexname = $1`, idx).Scan(&n))
		assert.Equal(t, 1, n, "index %s", idx)
	}
}
