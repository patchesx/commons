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
	"commons/store"
)

func TestListUsersEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListUsers(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Empty(t, out)
}

func TestListUsersWithUser(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	_, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIST", "listuser", "List User")
	require.NoError(t, err)

	h := ListUsers(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "List User", out[0]["displayName"])
}

func TestSearchUsersShortQueryReturnsEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	for _, q := range []string{"", "a"} {
		h := SearchUsers(pool)
		req := httptest.NewRequest(http.MethodGet, "/api/users/search?q="+q, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var out []any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
		assert.Empty(t, out, "query %q should return empty", q)
	}
}

func TestSearchUsersReturnsMatch(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	_, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_SEARCH", "searchuser", "Alice Finder")
	require.NoError(t, err)

	h := SearchUsers(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/users/search?q=Alice", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "Alice Finder", out[0]["displayName"])
}

func TestAssignGroupHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ASSIGN", "assignuser", "Assign User")
	require.NoError(t, err)

	g, err := store.CreateRoleGroup(ctx, pool, "assign-test-group", "", "")
	require.NoError(t, err)

	h := AssignGroup(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/users/"+u.ID+"/group",
		strings.NewReader(`{"groupId":"`+g.ID+`"}`))
	req.SetPathValue("id", u.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	got, err := store.GetUserGroup(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, g.ID, got.ID)
}

func TestAssignGroupHandlerMissingGroupID(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := AssignGroup(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/users/some-id/group",
		strings.NewReader(`{"groupId":""}`))
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListUsersIncludesIsWebAdmin(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WADMIN", "wadmin", "Web Admin User")
	require.NoError(t, err)

	// Promote to web admin.
	require.NoError(t, store.PromoteUserToWebAdmin(ctx, pool, u.ID, "wadmin@example.com", "hash"))

	// Also create a non-admin user.
	_, err = store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REGULAR", "regular", "Regular User")
	require.NoError(t, err)

	h := ListUsers(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out, 2)

	byName := map[string]map[string]any{}
	for _, o := range out {
		byName[o["displayName"].(string)] = o
	}
	assert.True(t, byName["Web Admin User"]["isWebAdmin"].(bool))
	assert.False(t, byName["Regular User"]["isWebAdmin"].(bool))
}

func TestListUsersIncludesPlatformStatus(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	_, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_PSTATUS", "ps", "Platform Status User")
	require.NoError(t, err)
	require.NoError(t, store.UpdateIdentityStatus(ctx, pool, "slack", "U_PSTATUS", "invited"))

	h := ListUsers(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "invited", out[0]["platformStatus"])
}

func TestListUsersStatusFilter(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	_, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ACTIVE", "active", "Active User")
	require.NoError(t, err)
	require.NoError(t, store.UpdateIdentityStatus(ctx, pool, "slack", "U_ACTIVE", "active"))

	_, err = store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_DEACT", "deact", "Deactivated User")
	require.NoError(t, err)
	require.NoError(t, store.UpdateIdentityStatus(ctx, pool, "slack", "U_DEACT", "deactivated"))

	h := ListUsers(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/users?status=active", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, "Active User", out[0]["displayName"])
}

func TestPromoteUserHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_PROMOTE", "promote", "Promote Me")
	require.NoError(t, err)
	// Set an email on the user record so promote can resolve it.
	_, err = pool.Exec(ctx, `UPDATE users SET email = 'promote@example.com' WHERE id = $1`, u.ID)
	require.NoError(t, err)

	h := PromoteUser(pool, testhelpers.EncKey(), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+u.ID+"/promote", nil)
	req.SetPathValue("id", u.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "promote@example.com", out["email"])

	isAdmin, err := store.IsWebAdmin(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.True(t, isAdmin)
}

func TestPromoteUserAlreadyAdmin(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALREADY", "already", "Already Admin")
	require.NoError(t, err)
	require.NoError(t, store.PromoteUserToWebAdmin(ctx, pool, u.ID, "already@example.com", "hash"))

	h := PromoteUser(pool, testhelpers.EncKey(), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+u.ID+"/promote", nil)
	req.SetPathValue("id", u.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestPromoteUserNoEmail(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	// User with no email on record, no body email.
	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_NOEMAIL", "noemail", "No Email User")
	require.NoError(t, err)

	h := PromoteUser(pool, testhelpers.EncKey(), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/users/"+u.ID+"/promote", nil)
	req.SetPathValue("id", u.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRemoveGroupHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REMOVE", "removeuser", "Remove User")
	require.NoError(t, err)

	g, err := store.CreateRoleGroup(ctx, pool, "remove-test-group", "", "")
	require.NoError(t, err)
	require.NoError(t, store.AssignGroupToUser(ctx, pool, u.ID, g.ID))

	h := RemoveGroup(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/users/"+u.ID+"/group", nil)
	req.SetPathValue("id", u.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	_, err = store.GetUserGroup(ctx, pool, u.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}
