package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestNormalizeInventoryFieldOptions(t *testing.T) {
	got, err := NormalizeInventoryFieldOptions([]string{" A/V ", "", "Events", "a/v", "  "})
	require.NoError(t, err)
	assert.Equal(t, []string{"A/V", "Events"}, got, "trimmed, blanks and repeats dropped, first spelling wins")

	for name, in := range map[string][]string{
		"empty":    {},
		"blanks":   {" ", ""},
		"too long": {strings.Repeat("x", MaxInventoryFieldOptionLen+1)},
	} {
		_, err := NormalizeInventoryFieldOptions(in)
		assert.ErrorIs(t, err, ErrInventoryFieldOptionsInvalid, name)
	}
}

func TestInventorySelectFields(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	a, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Mixer"}, "MIX222")
	require.NoError(t, err)
	b, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Banner"}, "BAN222")
	require.NoError(t, err)
	c, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Table"}, "TAB222")
	require.NoError(t, err)

	def, err := CreateInventoryFieldDefinition(ctx, pool, "type", "Type")
	require.NoError(t, err)
	assert.Equal(t, InventoryFieldText, def.Kind, "fields start as text")
	assert.Empty(t, def.Options)

	// Free text first, the way an imported field arrives.
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, a.ID, map[string]string{"type": "av"}))
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, b.ID, map[string]string{"type": "Events"}))
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, c.ID, map[string]string{"type": "Events"}))

	t.Run("the values in use are what a conversion starts from", func(t *testing.T) {
		vals, err := ListInventoryFieldValues(ctx, pool, []string{"type", "other"})
		require.NoError(t, err)
		assert.Equal(t, []string{"av", "Events"}, vals["type"], "distinct and sorted")
		assert.Empty(t, vals["other"])
	})

	t.Run("converting keeps every stored value", func(t *testing.T) {
		require.NoError(t, SetInventoryFieldKind(ctx, pool, def.ID, InventoryFieldSelect, []string{"A/V", "Events", "Furniture"}))
		got, _ := GetInventoryItem(ctx, pool, a.ID)
		assert.Equal(t, "av", got.Metadata["type"], "not in the list, still there")
		defs, _ := ListInventoryFieldDefinitions(ctx, pool, true)
		assert.Equal(t, InventoryFieldSelect, defs[0].Kind)
		assert.Equal(t, []string{"A/V", "Events", "Furniture"}, defs[0].Options)
	})

	t.Run("only listed choices are accepted, spelled as listed", func(t *testing.T) {
		assert.ErrorIs(t, SetInventoryItemMetadata(ctx, pool, c.ID, map[string]string{"type": "Sports"}), ErrInventoryNotAnOption)
		got, _ := GetInventoryItem(ctx, pool, c.ID)
		assert.Equal(t, "Events", got.Metadata["type"], "a refused value changes nothing")

		require.NoError(t, SetInventoryItemMetadata(ctx, pool, c.ID, map[string]string{"type": "furniture"}))
		got, _ = GetInventoryItem(ctx, pool, c.ID)
		assert.Equal(t, "Furniture", got.Metadata["type"])
	})

	t.Run("an item can keep a value that left the list, and clearing is always allowed", func(t *testing.T) {
		assert.NoError(t, SetInventoryItemMetadata(ctx, pool, a.ID, map[string]string{"type": "av"}), "saving what it already has")
		require.NoError(t, SetInventoryItemMetadata(ctx, pool, a.ID, map[string]string{"type": ""}))
		got, _ := GetInventoryItem(ctx, pool, a.ID)
		assert.NotContains(t, got.Metadata, "type")
	})

	t.Run("a dropdown needs choices", func(t *testing.T) {
		assert.ErrorIs(t, SetInventoryFieldKind(ctx, pool, def.ID, InventoryFieldSelect, nil), ErrInventoryFieldOptionsInvalid)
		assert.ErrorIs(t, SetInventoryFieldKind(ctx, pool, def.ID, "number", nil), ErrInventoryFieldOptionsInvalid)
		assert.ErrorIs(t, SetInventoryFieldKind(ctx, pool, "00000000-0000-0000-0000-000000000000", InventoryFieldText, nil), ErrNotFound)
	})

	t.Run("back to text accepts anything and keeps values", func(t *testing.T) {
		require.NoError(t, SetInventoryFieldKind(ctx, pool, def.ID, InventoryFieldText, []string{"ignored"}))
		defs, _ := ListInventoryFieldDefinitions(ctx, pool, true)
		assert.Equal(t, InventoryFieldText, defs[0].Kind)
		assert.Empty(t, defs[0].Options)
		require.NoError(t, SetInventoryItemMetadata(ctx, pool, c.ID, map[string]string{"type": "Sports"}))
	})
}

func TestInventoryImportSelectFields(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	def, err := CreateInventoryFieldDefinition(ctx, pool, "type", "Type")
	require.NoError(t, err)
	existing, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Archive box"}, "ARC222")
	require.NoError(t, err)
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, existing.ID, map[string]string{"type": "legacy"}))
	require.NoError(t, SetInventoryFieldKind(ctx, pool, def.ID, InventoryFieldSelect, []string{"A/V", "Events"}))

	plan := func(csv string) *InventoryImportPlan {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), true)
		require.NoError(t, err)
		return p
	}

	t.Run("values are matched to the list and spelled as listed", func(t *testing.T) {
		p := plan("Name,Type\nMixer,a/v\nBanner,EVENTS\n")
		require.Empty(t, p.Errors)
		assert.Equal(t, "A/V", p.Rows[0].Values["type"])
		assert.Equal(t, "Events", p.Rows[1].Values["type"])
		_, err := ApplyInventoryImport(ctx, pool, p)
		require.NoError(t, err)
	})

	t.Run("a value that isn't a choice is an error naming the choices", func(t *testing.T) {
		p := plan("Name,Type\nMixer,Sports\n")
		require.NotEmpty(t, p.Errors)
		assert.True(t, hasIssue(p.Errors, 2, "A/V, Events"), importMessages(p.Errors))
		_, err := ApplyInventoryImport(ctx, pool, p)
		assert.ErrorIs(t, err, ErrInventoryImportInvalid)
	})

	t.Run("a blank cell is fine", func(t *testing.T) {
		assert.Empty(t, plan("Name,Type\nMixer,\n").Errors)
	})

	t.Run("an item's current value passes, so an export imports back", func(t *testing.T) {
		assert.Empty(t, plan("Name,Code,Type\nArchive box,ARC222,legacy\n").Errors)
		p := plan("Name,Code,Type\nArchive box,ARC222,Sports\n")
		assert.NotEmpty(t, p.Errors, "but a different bad value still fails")
	})
}
