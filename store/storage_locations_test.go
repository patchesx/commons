package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestListStorageLocationsByTypeFiltersCorrectly(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	var gdriveIntID, zoomIntID string
	err := pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('gdrive', 'Test Drive', true)
		RETURNING id
	`).Scan(&gdriveIntID)
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('zoom', 'Test Zoom', true)
		RETURNING id
	`).Scan(&zoomIntID)
	require.NoError(t, err)

	_, err = CreateStorageLocation(ctx, pool, gdriveIntID, "Drive Folder", "/recordings")
	require.NoError(t, err)
	_, err = CreateStorageLocation(ctx, pool, zoomIntID, "Zoom Cloud", "/zoom")
	require.NoError(t, err)

	locs, err := ListStorageLocationsByType(ctx, pool, "gdrive")
	require.NoError(t, err)
	// Only the one gdrive location we created should appear (pre-seeded gdrive
	// integration has no storage_locations in a fresh test DB).
	var found []StorageLocation
	for _, l := range locs {
		if l.IntegrationID == gdriveIntID {
			found = append(found, l)
		}
	}
	require.Len(t, found, 1)
	assert.Equal(t, "gdrive", found[0].IntegrationType)
	assert.Equal(t, "Drive Folder", found[0].Name)

	// Zoom location must not appear in gdrive results.
	for _, l := range locs {
		assert.NotEqual(t, zoomIntID, l.IntegrationID)
	}
}

func TestListStorageLocationsByTypeEmptyWhenNoMatch(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	locs, err := ListStorageLocationsByType(ctx, pool, "nonexistent_type_xyz")
	require.NoError(t, err)
	assert.Empty(t, locs)
}

func TestListStorageLocationsByTypeMultipleIntegrationsSameType(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	var driveID1, driveID2 string
	err := pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('gdrive', 'Drive Alpha', true)
		RETURNING id
	`).Scan(&driveID1)
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('gdrive', 'Drive Beta', true)
		RETURNING id
	`).Scan(&driveID2)
	require.NoError(t, err)

	_, err = CreateStorageLocation(ctx, pool, driveID1, "Alpha Folder", "/alpha")
	require.NoError(t, err)
	_, err = CreateStorageLocation(ctx, pool, driveID2, "Beta Folder", "/beta")
	require.NoError(t, err)

	locs, err := ListStorageLocationsByType(ctx, pool, "gdrive")
	require.NoError(t, err)

	// Collect only the ones we created (ignore any pre-seeded integrations).
	ourIDs := map[string]bool{driveID1: true, driveID2: true}
	var ours []StorageLocation
	for _, l := range locs {
		if ourIDs[l.IntegrationID] {
			ours = append(ours, l)
		}
	}
	require.Len(t, ours, 2)
	for _, l := range ours {
		assert.Equal(t, "gdrive", l.IntegrationType)
	}
}
