package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// ListStorageLocations handles GET /api/storage-locations.
// Optional query parameter: integration_id to filter by integration instance.
func ListStorageLocations(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		integrationID := r.URL.Query().Get("integration_id")
		integrationType := r.URL.Query().Get("integration_type")

		var locations []store.StorageLocation
		var err error
		if integrationType != "" {
			locations, err = store.ListStorageLocationsByType(ctx, pool, integrationType)
		} else if integrationID != "" {
			locations, err = store.ListStorageLocationsByIntegration(ctx, pool, integrationID)
		} else {
			locations, err = store.ListStorageLocations(ctx, pool)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list storage locations")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, locations)
	}
}

// CreateStorageLocation handles POST /api/storage-locations.
func CreateStorageLocation(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var body struct {
			IntegrationID string `json:"integrationId"`
			Name          string `json:"name"`
			Path          string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.IntegrationID == "" || body.Name == "" || body.Path == "" {
			httpx.WriteError(w, http.StatusBadRequest, "integration_id, name, and path are required")
			return
		}

		loc, err := store.CreateStorageLocation(ctx, pool, body.IntegrationID, body.Name, body.Path)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create storage location")
			return
		}

		store.LogAction(ctx, pool, nil, "storage_location.created", loc.ID, map[string]string{
			"integration_id": loc.IntegrationID,
			"name":           loc.Name,
			"path":           loc.Path,
		})

		httpx.WriteJSON(w, http.StatusCreated, loc)
	}
}

// DeleteStorageLocation handles DELETE /api/storage-locations/{id}.
func DeleteStorageLocation(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.DeleteStorageLocation(ctx, pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete storage location")
			return
		}

		store.LogAction(ctx, pool, nil, "storage_location.deleted", id, nil)

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
