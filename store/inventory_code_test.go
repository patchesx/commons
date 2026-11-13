package store

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

var inventoryCodePattern = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{6}$`)

func TestNewInventoryCode(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		code := NewInventoryCode()
		require.Regexp(t, inventoryCodePattern, code)
		seen[code] = true
	}
	assert.Greater(t, len(seen), 490, "codes should be effectively unique")
}

func TestNormalizeAndFormatInventoryCode(t *testing.T) {
	assert.Equal(t, "K7M4QX", NormalizeInventoryCode("k7m-4qx"))
	assert.Equal(t, "K7M4QX", NormalizeInventoryCode("  K7M 4QX "))
	assert.Equal(t, "K7M4QX", NormalizeInventoryCode("K7M4QX"))
	assert.Equal(t, "K7M-4QX", FormatInventoryCode("K7M4QX"))
	assert.Equal(t, "ABC", FormatInventoryCode("ABC"), "non-codes pass through")
}

func TestGetInventoryItemByCodeAcceptsDash(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	it := invItem(t, pool, "K7M4QX")
	got, err := GetInventoryItemByCode(ctx, pool, "k7m-4qx")
	require.NoError(t, err)
	assert.Equal(t, it.ID, got.ID)
}

func TestRotateInventoryItemCodeRandom(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	it := invItem(t, pool, "OLD234")
	newCode, err := RotateInventoryItemCodeRandom(ctx, pool, it.ID)
	require.NoError(t, err)
	assert.Regexp(t, inventoryCodePattern, newCode)
	assert.NotEqual(t, "OLD234", newCode)

	got, err := GetInventoryItem(ctx, pool, it.ID)
	require.NoError(t, err)
	assert.Equal(t, newCode, got.Code)
	_, err = GetInventoryItemByCode(ctx, pool, "OLD234")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = RotateInventoryItemCodeRandom(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestCreateInventoryItemWithNewCode(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	a, err := CreateInventoryItemWithNewCode(ctx, pool, InventoryItemInput{Name: "A"})
	require.NoError(t, err)
	b, err := CreateInventoryItemWithNewCode(ctx, pool, InventoryItemInput{Name: "B"})
	require.NoError(t, err)

	assert.Regexp(t, inventoryCodePattern, a.Code)
	assert.Regexp(t, inventoryCodePattern, b.Code)
	assert.NotEqual(t, a.Code, b.Code)
}
