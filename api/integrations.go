package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// IntegrationStatus handles GET /api/integrations/status.
// Returns a map of integration type → enabled for the known integration types (slack, zoom, youtube, google).
// Used by the member portal to gate navigation items that require specific integrations.
func IntegrationStatus(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		integrations, err := store.ListIntegrations(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to fetch integrations")
			return
		}
		status := map[string]bool{
			"slack":   false,
			"zoom":    false,
			"youtube": false,
			"google":  false,
		}
		for _, i := range integrations {
			if _, ok := status[i.Type]; ok && i.Enabled {
				status[i.Type] = true
			}
		}
		httpx.WriteJSON(w, http.StatusOK, status)
	}
}

// ListIntegrations handles GET /api/integrations.
// Optional query parameter: type to filter by integration type.
func ListIntegrations(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		intType := r.URL.Query().Get("type")

		var integrations []store.Integration
		var err error
		if intType != "" {
			integrations, err = store.ListIntegrationsByType(ctx, pool, intType)
		} else {
			integrations, err = store.ListIntegrations(ctx, pool)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list integrations")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, integrations)
	}
}

// SetIntegrationEnabled handles PATCH /api/integrations/{id}/enabled.
// Toggles the enabled state of an integration.
func SetIntegrationEnabled(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := store.SetIntegrationEnabled(r.Context(), pool, id, body.Enabled); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update integration")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
