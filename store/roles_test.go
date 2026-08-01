package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
)

func TestCreateRole(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	r, err := CreateRole(ctx, pool, "test-role", "a description")
	require.NoError(t, err)
	assert.NotEmpty(t, r.ID)
	assert.Equal(t, "test-role", r.Name)
	assert.False(t, r.SystemRole)
}

func TestDeleteRole(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	r, err := CreateRole(ctx, pool, "to-delete", "")
	require.NoError(t, err)

	require.NoError(t, DeleteRole(ctx, pool, r.ID))

	got, err := GetRole(ctx, pool, r.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Nil(t, got)
}

func TestDeleteSystemRole(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// The migrations seed system roles — grab one.
	roles, err := ListRoles(ctx, pool)
	require.NoError(t, err)

	var systemRoleID string
	for _, r := range roles {
		if r.SystemRole {
			systemRoleID = r.ID
			break
		}
	}
	require.NotEmpty(t, systemRoleID, "expected at least one system role from migrations")

	err = DeleteRole(ctx, pool, systemRoleID)
	assert.ErrorIs(t, err, ErrSystemRole)
}

func TestAddRolePermissionIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	r, err := CreateRole(ctx, pool, "perm-role", "")
	require.NoError(t, err)

	require.NoError(t, AddRolePermission(ctx, pool, r.ID, permissions.JobsView))
	require.NoError(t, AddRolePermission(ctx, pool, r.ID, permissions.JobsView)) // idempotent

	roles, err := ListRolesWithPermissions(ctx, pool)
	require.NoError(t, err)
	for _, rp := range roles {
		if rp.ID == r.ID {
			assert.Equal(t, []string{permissions.JobsView}, rp.Permissions)
			return
		}
	}
	t.Fatal("role not found in listing")
}

func TestRemoveRolePermission(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	r, err := CreateRole(ctx, pool, "multi-perm", "")
	require.NoError(t, err)
	require.NoError(t, AddRolePermission(ctx, pool, r.ID, permissions.JobsView))
	require.NoError(t, AddRolePermission(ctx, pool, r.ID, permissions.RecordingsConfigWrite))

	require.NoError(t, RemoveRolePermission(ctx, pool, r.ID, permissions.JobsView))

	roles, err := ListRolesWithPermissions(ctx, pool)
	require.NoError(t, err)
	for _, rp := range roles {
		if rp.ID == r.ID {
			assert.NotContains(t, rp.Permissions, permissions.JobsView)
			assert.Contains(t, rp.Permissions, permissions.RecordingsConfigWrite)
			return
		}
	}
	t.Fatal("role not found in listing")
}

func TestGetUserPermissionsViaRole(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_PERM_TEST", "permtest", "Perm Test")
	require.NoError(t, err)

	r, err := CreateRole(ctx, pool, "test-viewer-perm", "")
	require.NoError(t, err)
	require.NoError(t, AddRolePermission(ctx, pool, r.ID, permissions.JobsView))

	g, err := CreateRoleGroup(ctx, pool, "test-perm-group", "", "")
	require.NoError(t, err)
	require.NoError(t, AddRoleToGroup(ctx, pool, g.ID, r.ID))
	require.NoError(t, AssignGroupToUser(ctx, pool, u.ID, g.ID))

	perms, err := GetUserPermissions(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Contains(t, perms, permissions.JobsView)
}

func TestListUsersWithPermission(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALICE2", "alice", "Alice")
	require.NoError(t, err)
	bob, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_BOB2", "bob", "Bob")
	require.NoError(t, err)

	r, err := CreateRole(ctx, pool, "test-job-viewer", "")
	require.NoError(t, err)
	require.NoError(t, AddRolePermission(ctx, pool, r.ID, permissions.JobsView))
	g, err := CreateRoleGroup(ctx, pool, "test-job-viewer-group", "", "")
	require.NoError(t, err)
	require.NoError(t, AddRoleToGroup(ctx, pool, g.ID, r.ID))
	require.NoError(t, AssignGroupToUser(ctx, pool, alice.ID, g.ID))
	// bob has no group

	users, err := ListUsersWithPermission(ctx, pool, permissions.JobsView)
	require.NoError(t, err)

	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	assert.Contains(t, ids, alice.ID)
	assert.NotContains(t, ids, bob.ID)
}
