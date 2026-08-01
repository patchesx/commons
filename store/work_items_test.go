package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestCreateWorkItem(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_CREATE_WI", "creator", "Creator")
	require.NoError(t, err)

	desc := "something is broken"
	item, err := CreateWorkItem(ctx, pool, u.ID, "issue", "App crashes on login", &desc)
	require.NoError(t, err)
	require.NotNil(t, item)

	assert.NotEmpty(t, item.ID)
	assert.Equal(t, u.ID, item.RequesterID)
	assert.Equal(t, "issue", item.Type)
	assert.Equal(t, "App crashes on login", item.Title)
	require.NotNil(t, item.Description)
	assert.Equal(t, desc, *item.Description)
	assert.Equal(t, "open", item.Status)
	assert.Nil(t, item.AdminNotes)
	assert.Nil(t, item.ResolvedBy)
}

func TestCreateWorkItemNilDescription(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_NODESC", "nodesc", "No Desc")
	require.NoError(t, err)

	item, err := CreateWorkItem(ctx, pool, u.ID, "feature_request", "Add dark mode", nil)
	require.NoError(t, err)
	assert.Nil(t, item.Description)
}

func TestListWorkItemsEmpty(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	items, err := ListWorkItems(ctx, pool, "", "")
	require.NoError(t, err)
	assert.NotNil(t, items)
	assert.Empty(t, items)
}

func TestListWorkItemsStatusFilter(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_STATUS", "statususer", "Status User")
	require.NoError(t, err)

	open, err := CreateWorkItem(ctx, pool, u.ID, "issue", "Open issue", nil)
	require.NoError(t, err)

	acked, err := CreateWorkItem(ctx, pool, u.ID, "issue", "Acked issue", nil)
	require.NoError(t, err)

	require.NoError(t, UpdateWorkItemStatus(ctx, pool, acked.ID, "acknowledged", nil, nil))

	openItems, err := ListWorkItems(ctx, pool, "open", "")
	require.NoError(t, err)
	require.Len(t, openItems, 1)
	assert.Equal(t, open.ID, openItems[0].ID)

	ackedItems, err := ListWorkItems(ctx, pool, "acknowledged", "")
	require.NoError(t, err)
	require.Len(t, ackedItems, 1)
	assert.Equal(t, acked.ID, ackedItems[0].ID)

	all, err := ListWorkItems(ctx, pool, "", "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestListWorkItemsTypeFilter(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_TYPE", "typeuser", "Type User")
	require.NoError(t, err)

	_, err = CreateWorkItem(ctx, pool, u.ID, "issue", "Bug report", nil)
	require.NoError(t, err)

	_, err = CreateWorkItem(ctx, pool, u.ID, "feature_request", "Feature request", nil)
	require.NoError(t, err)

	issues, err := ListWorkItems(ctx, pool, "", "issue")
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "issue", issues[0].Type)

	features, err := ListWorkItems(ctx, pool, "", "feature_request")
	require.NoError(t, err)
	require.Len(t, features, 1)
	assert.Equal(t, "feature_request", features[0].Type)
}

func TestUpdateWorkItemStatus(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_UPDATE", "updateuser", "Update User")
	require.NoError(t, err)

	item, err := CreateWorkItem(ctx, pool, u.ID, "issue", "Update me", nil)
	require.NoError(t, err)

	notes := "looking into it"
	require.NoError(t, UpdateWorkItemStatus(ctx, pool, item.ID, "acknowledged", &notes, nil))

	fetched, err := GetWorkItem(ctx, pool, item.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "acknowledged", fetched.Status)
	require.NotNil(t, fetched.AdminNotes)
	assert.Equal(t, notes, *fetched.AdminNotes)
}

func TestGetWorkItemNotFound(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	item, err := GetWorkItem(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Nil(t, item)
}
