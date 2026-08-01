package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

// configEntry is the response shape for both ListConfig and SetConfig.
// It merges config_schema (always present) with config_store (present when configured).
type configEntry struct {
	Service     string  `json:"service"`
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Description *string `json:"description"`
	Sensitive   bool    `json:"sensitive"`
	Required    bool    `json:"required"`
	Configured  bool    `json:"configured"`
	Value       string  `json:"value"` // "***" if sensitive+not revealed; "" if not configured
}

// ListConfig handles GET /api/config?service=zoom[&reveal=true]
// Returns all schema-defined keys for the service, with values filled in where configured.
func ListConfig(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		service := r.URL.Query().Get("service")
		reveal := r.URL.Query().Get("reveal") == "true"

		schema, err := store.ListConfigSchema(ctx, pool, service)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to load config schema")
			return
		}

		configured, err := store.ListServiceConfigs(ctx, pool, service)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list config")
			return
		}

		// Index configured entries by key for O(1) lookup.
		byKey := make(map[string]store.ConfigEntry, len(configured))
		for _, e := range configured {
			byKey[e.Key] = e
		}

		out := make([]configEntry, len(schema))
		for i, s := range schema {
			entry := configEntry{
				Service:     s.Service,
				Key:         s.Key,
				Label:       s.Label,
				Description: s.Description,
				Sensitive:   s.Sensitive,
				Required:    s.Required,
			}
			if c, ok := byKey[s.Key]; ok {
				entry.Configured = true
				if s.Sensitive {
					if reveal {
						plain, err := store.Decrypt(encKey, c.Value)
						if err != nil {
							log.Printf("api: decrypt config %s/%s: %v", s.Service, s.Key, err)
							httpx.WriteError(w, http.StatusInternalServerError, "failed to decrypt config")
							return
						}
						entry.Value = plain
					} else {
						entry.Value = "***"
					}
				} else {
					entry.Value = c.Value
				}
			}
			out[i] = entry
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// ListConfigSchemaAll handles GET /api/config/schema
// Returns all non-sensitive config_schema entries across all services.
// Used to populate config_ref dropdowns in the webhook filter UI.
func ListConfigSchemaAll(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := store.ListAllConfigSchemaEntries(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to load config schema")
			return
		}
		type row struct {
			Service string `json:"service"`
			Key     string `json:"key"`
			Label   string `json:"label"`
		}
		out := make([]row, len(entries))
		for i, e := range entries {
			out[i] = row{Service: e.Service, Key: e.Key, Label: e.Label}
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// SetConfig handles PUT /api/config/{service}/{key}
// Body: {"value": "..."}
// The sensitive flag is always taken from config_schema, not the request body.
// Returns 400 if the key is not defined in config_schema.
func SetConfig(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		service := r.PathValue("service")
		key := r.PathValue("key")

		var body struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		// Validate against schema and use its sensitive flag.
		schemaEntry, err := store.GetConfigSchemaEntry(ctx, pool, service, key)
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusBadRequest, "unknown config key")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to validate config key")
			return
		}

		adminID := web.UserIDFromContext(ctx)
		var adminIDPtr *string
		if adminID != "" {
			adminIDPtr = &adminID
		}

		// Empty value = clear the entry (revert to default). Required keys cannot be cleared.
		if body.Value == "" {
			if schemaEntry.Required {
				httpx.WriteError(w, http.StatusBadRequest, "value is required")
				return
			}
			if err := store.DeleteServiceConfig(ctx, pool, service, key); err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "failed to clear config")
				return
			}
			store.LogAction(ctx, pool, nil, "config.cleared", service+"/"+key, map[string]string{
				"service": service,
				"key":     key,
			})
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}

		if err := store.SetServiceConfig(ctx, pool, service, key, body.Value, schemaEntry.Sensitive, adminIDPtr, encKey); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update config")
			return
		}

		store.LogAction(ctx, pool, nil, "config.updated", service+"/"+key, map[string]string{
			"service": service,
			"key":     key,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
