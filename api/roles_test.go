package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

func TestCreateRoleHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateRole(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/roles",
		strings.NewReader(`{"name":"testers","description":"QA team"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.RoleWithPermissions
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "testers", out.Name)
	assert.NotEmpty(t, out.ID)
	assert.Empty(t, out.Permissions)
}

func TestCreateRoleHandlerMissingName(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateRole(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/roles",
		strings.NewReader(`{"name":"  "}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteRoleHandlerSuccess(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	r, err := store.CreateRole(ctx, pool, "to-delete-via-api", "")
	require.NoError(t, err)

	h := DeleteRole(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/roles/"+r.ID, nil)
	req.SetPathValue("id", r.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	got, err := store.GetRole(ctx, pool, r.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	assert.Nil(t, got)
}

func TestDeleteRoleHandlerSystemRoleReturns409(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	roles, err := store.ListRoles(ctx, pool)
	require.NoError(t, err)
	var sysID string
	for _, r := range roles {
		if r.SystemRole {
			sysID = r.ID
			break
		}
	}
	require.NotEmpty(t, sysID)

	h := DeleteRole(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/roles/"+sysID, nil)
	req.SetPathValue("id", sysID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestAddRolePermissionHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	r, err := store.CreateRole(ctx, pool, "add-perm-role", "")
	require.NoError(t, err)

	h := AddRolePermission(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/roles/"+r.ID+"/permissions",
		strings.NewReader(`{"key":"`+permissions.JobsView+`"}`))
	req.SetPathValue("id", r.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	perms, err := store.GetUserPermissions(ctx, pool, r.ID)
	// GetUserPermissions is on users — verify via ListRolesWithPermissions instead.
	_ = err
	_ = perms
	roles, err := store.ListRolesWithPermissions(ctx, pool)
	require.NoError(t, err)
	for _, rp := range roles {
		if rp.ID == r.ID {
			assert.Contains(t, rp.Permissions, permissions.JobsView)
			return
		}
	}
	t.Fatal("role not found")
}

func TestAddRolePermissionHandlerMissingKey(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := AddRolePermission(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/roles/some-id/permissions",
		strings.NewReader(`{"key":""}`))
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRemoveRolePermissionHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	r, err := store.CreateRole(ctx, pool, "remove-perm-role", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(ctx, pool, r.ID, permissions.JobsView))

	h := RemoveRolePermission(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/roles/"+r.ID+"/permissions/"+permissions.JobsView, nil)
	req.SetPathValue("id", r.ID)
	req.SetPathValue("key", permissions.JobsView)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	roles, err := store.ListRolesWithPermissions(ctx, pool)
	require.NoError(t, err)
	for _, rp := range roles {
		if rp.ID == r.ID {
			assert.NotContains(t, rp.Permissions, permissions.JobsView)
			return
		}
	}
	t.Fatal("role not found")
}

func TestListRolesWithPermsHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListRolesWithPerms(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/roles", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []store.RoleWithPermissions
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	// Migrations seed system roles, so list should be non-empty.
	assert.NotEmpty(t, out)
}
