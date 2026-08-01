package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
)

func TestListCategoriesForUserNoPermissionRequired(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	cat, err := CreateResourceCategory(ctx, pool, "Open Category")
	require.NoError(t, err)
	require.NoError(t, CreateResource(ctx, pool, &Resource{Title: "Doc", URL: "https://example.com", Category: cat.Name}))

	// User with no permissions should see categories with no required_permission.
	got, err := ListCategoriesForUser(ctx, pool, []string{})
	require.NoError(t, err)
	assert.Contains(t, got, "Open Category")
}

func TestListCategoriesForUserGatedCategoryHidden(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	perm := permissions.ResourcesManage
	cat, err := CreateResourceCategory(ctx, pool, "Members Only")
	require.NoError(t, err)
	require.NoError(t, UpdateResourceCategory(ctx, pool, cat.ID, cat.Name, &perm))
	require.NoError(t, CreateResource(ctx, pool, &Resource{Title: "Secret Doc", URL: "https://example.com", Category: cat.Name}))

	// User without the required permission should not see the category.
	got, err := ListCategoriesForUser(ctx, pool, []string{})
	require.NoError(t, err)
	assert.NotContains(t, got, "Members Only")
}

func TestListCategoriesForUserGatedCategoryVisibleWithPermission(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	perm := permissions.ResourcesManage
	cat, err := CreateResourceCategory(ctx, pool, "Permissioned Category")
	require.NoError(t, err)
	require.NoError(t, UpdateResourceCategory(ctx, pool, cat.ID, cat.Name, &perm))
	require.NoError(t, CreateResource(ctx, pool, &Resource{Title: "Gated Doc", URL: "https://example.com", Category: cat.Name}))

	// User with the required permission should see the category.
	got, err := ListCategoriesForUser(ctx, pool, []string{perm})
	require.NoError(t, err)
	assert.Contains(t, got, "Permissioned Category")
}

func TestListCategoriesForUserExcludesEmptyCategories(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Create a category but no resources in it.
	_, err := CreateResourceCategory(ctx, pool, "Empty Category")
	require.NoError(t, err)

	got, err := ListCategoriesForUser(ctx, pool, []string{})
	require.NoError(t, err)
	assert.NotContains(t, got, "Empty Category", "category with no resources should not appear")
}
