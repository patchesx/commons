package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// AddRoleToGroup handles POST /api/role-groups/{id}/roles.
// Body: {"roleId": "..."}
func AddRoleToGroup(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		groupID := r.PathValue("id")

		var body struct {
			RoleID string `json:"roleId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RoleID == "" {
			httpx.WriteError(w, http.StatusBadRequest, "roleId is required")
			return
		}

		if err := store.AddRoleToGroup(ctx, pool, groupID, body.RoleID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to add role to group")
			return
		}

		store.LogAction(ctx, pool, nil, "role_group.role_added", groupID, map[string]string{
			"role_id": body.RoleID,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// RemoveRoleFromGroup handles DELETE /api/role-groups/{id}/roles/{roleId}.
func RemoveRoleFromGroup(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		groupID := r.PathValue("id")
		roleID := r.PathValue("roleId")

		if err := store.RemoveRoleFromGroup(ctx, pool, groupID, roleID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to remove role from group")
			return
		}

		store.LogAction(ctx, pool, nil, "role_group.role_removed", groupID, map[string]string{
			"role_id": roleID,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteRoleGroup handles DELETE /api/role-groups/{id}.
func DeleteRoleGroup(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		groupID := r.PathValue("id")

		if err := store.DeleteRoleGroup(ctx, pool, groupID); err != nil {
			if errors.Is(err, store.ErrSystemGroup) {
				httpx.WriteError(w, http.StatusConflict, "cannot delete a system group")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete group")
			return
		}

		store.LogAction(ctx, pool, nil, "role_group.deleted", groupID, nil)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
