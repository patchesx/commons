package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestGetOrCreateUserByIdentityNew(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_NEW", "newuser", "New User")
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.NotEmpty(t, u.ID)
	assert.Equal(t, "New User", u.DisplayName)
}

func TestGetOrCreateUserByIdentityExisting(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	first, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_EXIST", "user", "User")
	require.NoError(t, err)

	second, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_EXIST", "user", "User")
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "second call should return the same user")
}

func TestGetOrCreateUserByIdentityUpdatesDisplayName(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_RENAME", "oldname", "Old Name")
	require.NoError(t, err)

	updated, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_RENAME", "newname", "New Name")
	require.NoError(t, err)
	assert.Equal(t, "New Name", updated.DisplayName)
}

func TestGetOrCreateUserFallsBackToUsername(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Empty displayName — should fall back to username.
	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_NONAME", "justusername", "")
	require.NoError(t, err)
	assert.Equal(t, "justusername", u.DisplayName)
}

func TestGetOrCreateUserFallsBackToExternalID(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Both displayName and username empty — falls back to externalID.
	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_BARE_ID", "", "")
	require.NoError(t, err)
	assert.Equal(t, "U_BARE_ID", u.DisplayName)
}

func TestUpdateIdentityStatus(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_STATUS", "statususer", "Status User")
	require.NoError(t, err)

	require.NoError(t, UpdateIdentityStatus(ctx, pool, "slack", "U_STATUS", "deactivated"))

	users, err := ListUsers(ctx, pool)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "deactivated", users[0].PlatformStatus)
}

func TestListUsersPlatformStatusDefaultUnknown(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Newly created identity gets the column default ('unknown').
	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_DEFAULT_STATUS", "user", "Default Status User")
	require.NoError(t, err)

	users, err := ListUsers(ctx, pool)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "unknown", users[0].PlatformStatus)
}

func TestListUsersPlatformStatusPriority(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Create one user with two identities — one deactivated, one active.
	// ListUsers should return 'active' (higher priority).
	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_PRIO", "user", "Priority User")
	require.NoError(t, err)
	require.NoError(t, UpdateIdentityStatus(ctx, pool, "slack", "U_PRIO", "deactivated"))

	// Insert a second identity linked to the same user.
	_, err = pool.Exec(ctx,
		`INSERT INTO user_identities (user_id, provider, external_id, username, display_name, platform_status)
		 VALUES ($1, 'matrix', 'M_PRIO', 'user', 'Priority User', 'active')`,
		u.ID,
	)
	require.NoError(t, err)

	users, err := ListUsers(ctx, pool)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "active", users[0].PlatformStatus, "active should win over deactivated")
}

func TestUpdateUserSelectedCalendar(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_CAL_TEST", "testuser", "Test User")
	require.NoError(t, err)

	// Initially nil.
	assert.Nil(t, user.SelectedCalendarID)

	// Create a calendar to select.
	cal := &Calendar{Name: "My Cal", Slug: "my-cal", Timezone: "America/Chicago"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))

	// Set selection.
	require.NoError(t, UpdateUserSelectedCalendar(ctx, pool, user.ID, cal.ID))

	updated, err := GetUserByID(ctx, pool, user.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.SelectedCalendarID)
	assert.Equal(t, cal.ID, *updated.SelectedCalendarID)

	// Clear selection.
	require.NoError(t, UpdateUserSelectedCalendar(ctx, pool, user.ID, ""))
	cleared, err := GetUserByID(ctx, pool, user.ID)
	require.NoError(t, err)
	assert.Nil(t, cleared.SelectedCalendarID)
}
