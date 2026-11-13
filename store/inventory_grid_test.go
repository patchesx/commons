package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func gridNames(rows []InventoryGridRow) []string {
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

func TestListInventoryGrid(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice Anderson")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, alice)
	bob := invUser(t, pool, "Bob Brown")

	camera := invItem(t, pool, "CAM222")
	invExec(t, pool, `UPDATE inventory.items SET name = 'Camera', location = 'Studio', description = 'Mirrorless, 50% off' WHERE id = $1`, camera.ID)
	projector := invItem(t, pool, "K7M4QX")
	invExec(t, pool, `UPDATE inventory.items SET name = 'projector', location = 'Closet' WHERE id = $1`, projector.ID)
	laptop := invItem(t, pool, "LAP222")
	invExec(t, pool, `UPDATE inventory.items SET name = 'Laptop', location = NULL WHERE id = $1`, laptop.ID)
	retired := invItem(t, pool, "OLD222")
	invExec(t, pool, `UPDATE inventory.items SET name = 'Old tripod' WHERE id = $1`, retired.ID)
	require.NoError(t, ArchiveInventoryItem(ctx, pool, retired.ID))

	_, err := CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial")
	require.NoError(t, err)
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, camera.ID, map[string]string{"serial": "SN-300"}))
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, projector.ID, map[string]string{"serial": "SN-100"}))
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, laptop.ID, map[string]string{"serial": "sn-200"}))

	invClaim(t, pool, projector.ID, alice)
	invClaim(t, pool, laptop.ID, bob)
	_, err = RecordPendingClaim(ctx, pool, camera.ID, "Robert", "robert@elsewhere.example", nil, nil)
	require.NoError(t, err)

	list := func(q InventoryGridQuery) ([]string, int) {
		t.Helper()
		rows, total, err := ListInventoryGrid(ctx, pool, q)
		require.NoError(t, err)
		return gridNames(rows), total
	}

	t.Run("archived items are hidden unless asked for", func(t *testing.T) {
		names, total := list(InventoryGridQuery{})
		assert.Equal(t, []string{"Camera", "Laptop", "projector"}, names, "case-insensitive name order")
		assert.Equal(t, 3, total)
		names, total = list(InventoryGridQuery{IncludeArchived: true})
		assert.Equal(t, 4, total)
		assert.Contains(t, names, "Old tripod")
	})

	t.Run("rows carry the holder and the claim pending marker", func(t *testing.T) {
		rows, _, err := ListInventoryGrid(ctx, pool, InventoryGridQuery{})
		require.NoError(t, err)
		byName := map[string]InventoryGridRow{}
		for _, r := range rows {
			byName[r.Name] = r
		}
		assert.Equal(t, "Alice Anderson", byName["projector"].HolderName)
		assert.False(t, byName["projector"].ClaimPending)
		assert.Equal(t, "", byName["Camera"].HolderName)
		assert.True(t, byName["Camera"].ClaimPending)
		assert.Equal(t, "SN-300", byName["Camera"].Metadata["serial"])
	})

	t.Run("search covers every column, case-insensitively", func(t *testing.T) {
		for search, want := range map[string]string{
			"CAMERA":        "Camera",    // name
			"k7m-4qx":       "projector", // code, with the dash
			"k7m4":          "projector", // code
			"closet":        "projector", // location
			"mirrorless":    "Camera",    // description
			"anderson":      "projector", // holder name
			"ALICE@EXAMPLE": "projector", // holder email
			"sn-300":        "Camera",    // custom field value
			"SN-200":        "Laptop",    // custom field value, other case
		} {
			names, _ := list(InventoryGridQuery{Search: search})
			assert.Equal(t, []string{want}, names, "search %q", search)
		}
		names, _ := list(InventoryGridQuery{Search: "  nothing like this "})
		assert.Empty(t, names)
	})

	t.Run("wildcards in the search are literal", func(t *testing.T) {
		names, _ := list(InventoryGridQuery{Search: "50%"})
		assert.Equal(t, []string{"Camera"}, names)
		names, _ = list(InventoryGridQuery{Search: "%"})
		assert.Equal(t, []string{"Camera"}, names, "a bare % matches only text containing a percent sign")
		names, _ = list(InventoryGridQuery{Search: "_"})
		assert.Empty(t, names, "an underscore is not a wildcard")
	})

	t.Run("claim pending filter", func(t *testing.T) {
		names, total := list(InventoryGridQuery{PendingOnly: true})
		assert.Equal(t, []string{"Camera"}, names)
		assert.Equal(t, 1, total)
	})

	t.Run("sorting", func(t *testing.T) {
		// Codes: Camera CAM222, projector K7M4QX, Laptop LAP222. Holders: Camera none, projector Alice, Laptop Bob.
		// Locations: Camera Studio, projector Closet, Laptop none. Serials: Camera sn-300, projector sn-100, Laptop sn-200.
		cases := []struct {
			name string
			q    InventoryGridQuery
			want []string
		}{
			{"name, descending", InventoryGridQuery{SortKey: "name", Desc: true}, []string{"projector", "Laptop", "Camera"}},
			{"code", InventoryGridQuery{SortKey: "code"}, []string{"Camera", "projector", "Laptop"}},
			{"location puts no location first", InventoryGridQuery{SortKey: "location"}, []string{"Laptop", "projector", "Camera"}},
			{"holder", InventoryGridQuery{SortKey: "holder"}, []string{"Camera", "projector", "Laptop"}},
			{"holder, descending", InventoryGridQuery{SortKey: "holder", Desc: true}, []string{"Laptop", "projector", "Camera"}},
			{"custom field", InventoryGridQuery{SortKey: "field:serial"}, []string{"projector", "Laptop", "Camera"}},
			{"custom field, descending", InventoryGridQuery{SortKey: "field:serial", Desc: true}, []string{"Camera", "Laptop", "projector"}},
			{"a field nobody filled in falls back to name order", InventoryGridQuery{SortKey: "field:unknown"}, []string{"Camera", "Laptop", "projector"}},
			{"an unknown sort key falls back to name order", InventoryGridQuery{SortKey: "bogus"}, []string{"Camera", "Laptop", "projector"}},
		}
		for _, c := range cases {
			names, _ := list(c.q)
			assert.Equal(t, c.want, names, c.name)
		}
	})

	t.Run("last verified sorts never-verified items last either way", func(t *testing.T) {
		invExec(t, pool, `UPDATE inventory.items SET last_verified_at = NOW() - INTERVAL '5 days' WHERE id = $1`, projector.ID)
		invExec(t, pool, `UPDATE inventory.items SET last_verified_at = NOW() - INTERVAL '1 day' WHERE id = $1`, laptop.ID)
		names, _ := list(InventoryGridQuery{SortKey: "verified"})
		assert.Equal(t, []string{"projector", "Laptop", "Camera"}, names)
		names, _ = list(InventoryGridQuery{SortKey: "verified", Desc: true})
		assert.Equal(t, []string{"Laptop", "projector", "Camera"}, names)
	})

	t.Run("held by one member", func(t *testing.T) {
		names, total := list(InventoryGridQuery{HeldBy: alice})
		assert.Equal(t, []string{"projector"}, names)
		assert.Equal(t, 1, total)
		names, _ = list(InventoryGridQuery{HeldBy: bob})
		assert.Equal(t, []string{"Laptop"}, names)
		names, _ = list(InventoryGridQuery{HeldBy: "00000000-0000-0000-0000-000000000000"})
		assert.Empty(t, names)
		names, _ = list(InventoryGridQuery{HeldBy: alice, Search: "laptop"})
		assert.Empty(t, names, "filters combine")
	})

	t.Run("awaiting verification means reminded and not verified since", func(t *testing.T) {
		names, _ := list(InventoryGridQuery{AwaitingVerification: true})
		assert.Empty(t, names, "nobody has been reminded yet")

		invExec(t, pool, `UPDATE inventory.items SET last_reminded_at = NOW() WHERE id = $1`, projector.ID)
		names, total := list(InventoryGridQuery{AwaitingVerification: true})
		assert.Equal(t, []string{"projector"}, names)
		assert.Equal(t, 1, total)
		names, _ = list(InventoryGridQuery{AwaitingVerification: true, HeldBy: bob})
		assert.Empty(t, names, "combined with held-by")

		invExec(t, pool, `UPDATE inventory.items SET last_reminded_at = NOW() WHERE id = $1`, camera.ID)
		names, _ = list(InventoryGridQuery{AwaitingVerification: true})
		assert.Equal(t, []string{"projector"}, names, "an item nobody holds is not awaiting anyone")

		_, err := RecordVerification(ctx, pool, projector.ID, nil)
		require.NoError(t, err)
		names, _ = list(InventoryGridQuery{AwaitingVerification: true})
		assert.Empty(t, names, "verifying ends the wait")
		invExec(t, pool, `UPDATE inventory.items SET last_reminded_at = NULL WHERE id = $1`, camera.ID)
	})

	t.Run("paging reports the full total", func(t *testing.T) {
		names, total := list(InventoryGridQuery{Limit: 2})
		assert.Equal(t, []string{"Camera", "Laptop"}, names)
		assert.Equal(t, 3, total)
		names, total = list(InventoryGridQuery{Limit: 2, Offset: 2})
		assert.Equal(t, []string{"projector"}, names)
		assert.Equal(t, 3, total)
		names, _ = list(InventoryGridQuery{Limit: 2, Offset: 50})
		assert.Empty(t, names)
		names, _ = list(InventoryGridQuery{Limit: 0})
		assert.Len(t, names, 3, "no limit returns everything")
	})

	t.Run("one item by ID, archived or not", func(t *testing.T) {
		rows, total, err := ListInventoryGrid(ctx, pool, InventoryGridQuery{ID: retired.ID, IncludeArchived: true})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, 1, total)
		assert.Equal(t, "Old tripod", rows[0].Name)
		rows, _, err = ListInventoryGrid(ctx, pool, InventoryGridQuery{ID: retired.ID})
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}

func TestListInventoryGridPagesLargeLists(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	for i := 0; i < 25; i++ {
		_, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: fmt.Sprintf("Item %02d", i)}, fmt.Sprintf("ITM%03d", i+200))
		require.NoError(t, err)
	}
	rows, total, err := ListInventoryGrid(ctx, pool, InventoryGridQuery{Limit: 20})
	require.NoError(t, err)
	assert.Len(t, rows, 20)
	assert.Equal(t, 25, total)
	rows, _, err = ListInventoryGrid(ctx, pool, InventoryGridQuery{Limit: 20, Offset: 20})
	require.NoError(t, err)
	assert.Len(t, rows, 5)
	assert.Equal(t, "Item 20", rows[0].Name)
}

func TestSetInventoryItemBuiltinField(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	it := invItem(t, pool, "EDT222")

	require.NoError(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "name", "  Renamed  "))
	require.NoError(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "location", "Back room"))
	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, "Renamed", got.Name)
	assert.Equal(t, "Back room", *got.Location)

	require.NoError(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "location", "   "))
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Nil(t, got.Location, "an empty location clears it")

	assert.ErrorIs(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "name", "  "), ErrInventoryInvalidValue)
	assert.ErrorIs(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "code", "ZZZZZZ"), ErrInventoryInvalidValue, "only name and location are editable")
	assert.ErrorIs(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "description", "x"), ErrInventoryInvalidValue)
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, "Renamed", got.Name)
	assert.Equal(t, "EDT222", got.Code)

	assert.ErrorIs(t, SetInventoryItemBuiltinField(ctx, pool, "00000000-0000-0000-0000-000000000000", "name", "x"), ErrNotFound)
	require.NoError(t, ArchiveInventoryItem(ctx, pool, it.ID))
	assert.ErrorIs(t, SetInventoryItemBuiltinField(ctx, pool, it.ID, "name", "x"), ErrInventoryItemArchived)
}
