package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// ListRolesWithPerms handles GET /api/roles — returns all roles with their permission keys.
func ListRolesWithPerms(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roles, err := store.ListRolesWithPermissions(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list roles")
			return
		}
		if roles == nil {
			roles = []store.RoleWithPermissions{}
		}
		httpx.WriteJSON(w, http.StatusOK, roles)
	}
}

// CreateRole handles POST /api/roles.
func CreateRole(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name is required")
			return
		}

		role, err := store.CreateRole(ctx, pool, body.Name, body.Description)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create role")
			return
		}

		store.LogAction(ctx, pool, nil, "role.created", role.ID, map[string]string{
			"name": role.Name,
		})

		out := store.RoleWithPermissions{
			Role:        *role,
			Permissions: []string{},
		}
		httpx.WriteJSON(w, http.StatusCreated, out)
	}
}

// UpdateRole handles PATCH /api/roles/{id}.
func UpdateRole(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")

		var body struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name is required")
			return
		}

		if err := store.UpdateRole(ctx, pool, roleID, body.Name, body.DisplayName, body.Description); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update role")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteRole handles DELETE /api/roles/{id}.
func DeleteRole(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")

		if err := store.DeleteRole(ctx, pool, roleID); err != nil {
			if errors.Is(err, store.ErrSystemRole) {
				httpx.WriteError(w, http.StatusConflict, "cannot delete a system role")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete role")
			return
		}

		store.LogAction(ctx, pool, nil, "role.deleted", roleID, nil)

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// AddRolePermission handles POST /api/roles/{id}/permissions.
func AddRolePermission(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")

		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Key == "" {
			httpx.WriteError(w, http.StatusBadRequest, "key is required")
			return
		}

		if err := store.AddRolePermission(ctx, pool, roleID, body.Key); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to add permission")
			return
		}

		store.LogAction(ctx, pool, nil, "role.permission_added", roleID, map[string]string{
			"key": body.Key,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// RemoveRolePermission handles DELETE /api/roles/{id}/permissions/{key}.
func RemoveRolePermission(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")
		key := r.PathValue("key")

		if key == "" {
			httpx.WriteError(w, http.StatusBadRequest, "permission key is required")
			return
		}

		if err := store.RemoveRolePermission(ctx, pool, roleID, key); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to remove permission")
			return
		}

		store.LogAction(ctx, pool, nil, "role.permission_removed", roleID, map[string]string{
			"key": key,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
