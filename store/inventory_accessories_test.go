package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func accessoryNames(list []InventoryAccessory) []string {
	out := []string{}
	for _, a := range list {
		out = append(out, a.Name)
	}
	return out
}

func TestInventoryAccessories(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	item, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "PA system"}, "PAS222")
	require.NoError(t, err)
	other, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Table"}, "TBL222")
	require.NoError(t, err)

	t.Run("add, trim, and list in the order added", func(t *testing.T) {
		for _, n := range []string{"  Speaker ", "Stand", "Power cable"} {
			_, err := AddInventoryAccessory(ctx, pool, item.ID, n)
			require.NoError(t, err)
		}
		got, err := ListInventoryAccessories(ctx, pool, item.ID)
		require.NoError(t, err)
		assert.Equal(t, []string{"Speaker", "Stand", "Power cable"}, accessoryNames(got))
		none, err := ListInventoryAccessories(ctx, pool, other.ID)
		require.NoError(t, err)
		assert.Empty(t, none)
	})

	t.Run("names are unique per item, ignoring case", func(t *testing.T) {
		_, err := AddInventoryAccessory(ctx, pool, item.ID, "SPEAKER")
		assert.ErrorIs(t, err, ErrInventoryAccessoryExists)
		_, err = AddInventoryAccessory(ctx, pool, other.ID, "Speaker")
		assert.NoError(t, err, "another item may use the same name")
	})

	t.Run("blank and over-long names are refused", func(t *testing.T) {
		_, err := AddInventoryAccessory(ctx, pool, item.ID, "   ")
		assert.ErrorIs(t, err, ErrInventoryAccessoryInvalid)
		_, err = AddInventoryAccessory(ctx, pool, item.ID, strings.Repeat("x", MaxInventoryAccessoryNameLen+1))
		assert.ErrorIs(t, err, ErrInventoryAccessoryInvalid)
	})

	t.Run("several items at once", func(t *testing.T) {
		m, err := ListInventoryAccessoriesFor(ctx, pool, []string{item.ID, other.ID})
		require.NoError(t, err)
		assert.Len(t, m[item.ID], 3)
		assert.Len(t, m[other.ID], 1)
	})

	t.Run("delete only within the item", func(t *testing.T) {
		got, _ := ListInventoryAccessories(ctx, pool, item.ID)
		assert.ErrorIs(t, DeleteInventoryAccessory(ctx, pool, other.ID, got[0].ID), ErrNotFound)
		require.NoError(t, DeleteInventoryAccessory(ctx, pool, item.ID, got[0].ID))
		after, _ := ListInventoryAccessories(ctx, pool, item.ID)
		assert.Equal(t, []string{"Stand", "Power cable"}, accessoryNames(after))
	})

	t.Run("archived items are read-only and keep their accessories", func(t *testing.T) {
		require.NoError(t, ArchiveInventoryItem(ctx, pool, item.ID))
		_, err := AddInventoryAccessory(ctx, pool, item.ID, "Mic")
		assert.ErrorIs(t, err, ErrNotFound)
		got, _ := ListInventoryAccessories(ctx, pool, item.ID)
		assert.ErrorIs(t, DeleteInventoryAccessory(ctx, pool, item.ID, got[0].ID), ErrNotFound)
		assert.Len(t, got, 2)
	})

	t.Run("an item can't have more than the maximum", func(t *testing.T) {
		full, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Kit"}, "KIT222")
		require.NoError(t, err)
		for i := 0; i < MaxInventoryAccessories; i++ {
			_, err := AddInventoryAccessory(ctx, pool, full.ID, strings.Repeat("a", i+1))
			require.NoError(t, err)
		}
		_, err = AddInventoryAccessory(ctx, pool, full.ID, "one more")
		assert.ErrorIs(t, err, ErrInventoryTooManyAccessories)
	})
}

func TestInventoryImportAccessories(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	run := func(csv string) (*InventoryImportPlan, *InventoryImportResult) {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), true)
		require.NoError(t, err)
		if len(p.Errors) > 0 {
			return p, nil
		}
		r, err := ApplyInventoryImport(ctx, pool, p)
		require.NoError(t, err)
		return p, r
	}

	t.Run("a quoted comma-separated cell is split, trimmed, and de-duplicated", func(t *testing.T) {
		p, r := run("Name,Code,Accessories\nPA system,PAS222,\"Speaker, Stand ,, power cable,SPEAKER\"\n")
		require.Empty(t, p.Errors)
		assert.Equal(t, []string{"Speaker", "Stand", "power cable"}, p.Rows[0].Accessories)
		assert.Equal(t, 3, r.AccessoriesAdded)
		item, err := GetInventoryItemByCode(ctx, pool, "PAS222")
		require.NoError(t, err)
		got, _ := ListInventoryAccessories(ctx, pool, item.ID)
		assert.Equal(t, []string{"Speaker", "Stand", "power cable"}, accessoryNames(got))
	})

	t.Run("re-importing adds only what is missing and never removes", func(t *testing.T) {
		_, r := run("Name,Code,Accessories\nPA system,PAS222,\"stand, Mic\"\n")
		assert.Equal(t, 1, r.AccessoriesAdded)
		item, _ := GetInventoryItemByCode(ctx, pool, "PAS222")
		got, _ := ListInventoryAccessories(ctx, pool, item.ID)
		assert.Equal(t, []string{"Speaker", "Stand", "power cable", "Mic"}, accessoryNames(got))
		_, r = run("Name,Code,Accessories\nPA system,PAS222,\n")
		assert.Equal(t, 0, r.AccessoriesAdded)
		got, _ = ListInventoryAccessories(ctx, pool, item.ID)
		assert.Len(t, got, 4, "a blank cell leaves them alone")
	})

	t.Run("accessories never become items", func(t *testing.T) {
		items, err := ListInventoryItems(ctx, pool, true)
		require.NoError(t, err)
		require.Len(t, items, 1)
	})

	t.Run("a too-long name is an error on its line", func(t *testing.T) {
		p, _ := run("Name,Accessories\nX," + strings.Repeat("y", MaxInventoryAccessoryNameLen+1) + "\n")
		assert.True(t, hasIssue(p.Errors, 2, "Accessory") || hasIssue(p.Errors, 2, "accessory"), importMessages(p.Errors))
	})

	t.Run("the heading is matched without regard to case", func(t *testing.T) {
		p, _ := run("NAME,accessories\nCamera,Lens\n")
		require.Empty(t, p.Errors)
		assert.Equal(t, []string{"Lens"}, p.Rows[0].Accessories)
	})
}
