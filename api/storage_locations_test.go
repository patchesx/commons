package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

func TestListStorageLocationsByIntegrationType(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	// Seed a gdrive integration with two locations and a zoom integration with one.
	var gdriveID, zoomID string
	err := pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('gdrive', 'Drive', true)
		RETURNING id
	`).Scan(&gdriveID)
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('zoom', 'Zoom', true)
		RETURNING id
	`).Scan(&zoomID)
	require.NoError(t, err)

	_, err = store.CreateStorageLocation(ctx, pool, gdriveID, "Recordings", "/rec")
	require.NoError(t, err)
	_, err = store.CreateStorageLocation(ctx, pool, gdriveID, "Archives", "/arc")
	require.NoError(t, err)
	_, err = store.CreateStorageLocation(ctx, pool, zoomID, "Zoom Folder", "/zoom")
	require.NoError(t, err)

	h := ListStorageLocations(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/storage-locations?integration_type=gdrive", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))

	for _, loc := range out {
		assert.Equal(t, "gdrive", loc["IntegrationType"])
	}

	// Count how many belong to our gdriveID (ignore any pre-seeded integrations).
	var ours int
	for _, loc := range out {
		if loc["IntegrationID"] == gdriveID {
			ours++
		}
	}
	assert.Equal(t, 2, ours)
}

func TestListStorageLocationsNoParamReturnsAll(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	var intID string
	err := pool.QueryRow(ctx, `
		INSERT INTO integrations (type, name, enabled) VALUES ('gdrive', 'Drive', true)
		RETURNING id
	`).Scan(&intID)
	require.NoError(t, err)

	_, err = store.CreateStorageLocation(ctx, pool, intID, "Loc A", "/a")
	require.NoError(t, err)
	_, err = store.CreateStorageLocation(ctx, pool, intID, "Loc B", "/b")
	require.NoError(t, err)

	h := ListStorageLocations(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/storage-locations", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))

	var ours int
	for _, loc := range out {
		if loc["IntegrationID"] == intID {
			ours++
		}
	}
	assert.Equal(t, 2, ours)
}

func TestListStorageLocationsUnknownTypeReturnsEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListStorageLocations(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/storage-locations?integration_type=nonexistent_xyz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Empty(t, out)
}
