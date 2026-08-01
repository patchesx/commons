package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// ListResources handles GET /api/resources?page=1&per_page=25.
func ListResources(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		page, perPage := 1, 25
		if p := r.URL.Query().Get("page"); p != "" {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				page = v
			}
		}
		if pp := r.URL.Query().Get("per_page"); pp != "" {
			if v, err := strconv.Atoi(pp); err == nil && v > 0 {
				perPage = v
			}
		}

		total, err := store.CountResources(ctx, pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to count resources")
			return
		}

		resources, err := store.ListResources(ctx, pool, page, perPage)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list resources")
			return
		}
		if resources == nil {
			resources = []store.Resource{}
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"resources": resources,
			"total":     total,
			"page":      page,
			"perPage":   perPage,
		})
	}
}

// CreateResource handles POST /api/resources.
// Body: {title, url, category, description, position}
func CreateResource(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var res store.Resource
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if res.Title == "" || res.URL == "" || res.Category == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title, url, and category are required")
			return
		}

		if err := store.CreateResource(ctx, pool, &res); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create resource")
			return
		}

		store.LogAction(ctx, pool, nil, "resource.created", res.ID, map[string]string{
			"title": res.Title,
		})

		httpx.WriteJSON(w, http.StatusCreated, res)
	}
}

// UpdateResource handles PUT /api/resources/{id}.
func UpdateResource(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		var res store.Resource
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		res.ID = id

		if err := store.UpdateResource(ctx, pool, &res); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update resource")
			return
		}

		store.LogAction(ctx, pool, nil, "resource.updated", id, map[string]string{
			"title": res.Title,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteResource handles DELETE /api/resources/{id}.
func DeleteResource(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.DeleteResource(ctx, pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete resource")
			return
		}

		store.LogAction(ctx, pool, nil, "resource.deleted", id, nil)

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ListResourceCategories handles GET /api/resource-categories.
func ListResourceCategories(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cats, err := store.ListResourceCategories(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list categories")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, cats)
	}
}

// CreateResourceCategory handles POST /api/resource-categories.
// Body: {"name": "..."}
func CreateResourceCategory(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name is required")
			return
		}

		cat, err := store.CreateResourceCategory(ctx, pool, strings.TrimSpace(body.Name))
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create category")
			return
		}

		httpx.WriteJSON(w, http.StatusCreated, cat)
	}
}

// UpdateResourceCategory handles PUT /api/resource-categories/{id}.
// Body: {"name": "...", "requiredPermission": "members.view"} — requiredPermission is nullable.
func UpdateResourceCategory(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		var body struct {
			Name               string  `json:"name"`
			RequiredPermission *string `json:"requiredPermission"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name is required")
			return
		}

		name := strings.TrimSpace(body.Name)
		if err := store.UpdateResourceCategory(ctx, pool, id, name, body.RequiredPermission); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update category")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ListPermissions handles GET /api/permissions.
// Returns all permissions defined in the system with keys and descriptions.
func ListPermissions(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		perms, err := store.ListAllPermissions(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list permissions")
			return
		}
		if perms == nil {
			perms = []store.Permission{}
		}
		httpx.WriteJSON(w, http.StatusOK, perms)
	}
}

// DeleteResourceCategory handles DELETE /api/resource-categories/{id}.
// Returns 409 if resources are still assigned to the category.
func DeleteResourceCategory(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.DeleteResourceCategory(ctx, pool, id); err != nil {
			// pgx surfaces FK violation as a *pgconn.PgError with code 23503.
			if strings.Contains(err.Error(), "23503") {
				httpx.WriteError(w, http.StatusConflict, "category still has resources assigned to it")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete category")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
