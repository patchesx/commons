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

func TestListUsersPageAlphabeticalOrder(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Create users out of alphabetical order.
	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ZOE", "zoe", "Zoe Zhang")
	require.NoError(t, err)
	_, err = GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALICE", "alice", "Alice Adams")
	require.NoError(t, err)
	_, err = GetOrCreateUserByIdentity(ctx, pool, "slack", "U_BOB", "bob", "Bob Brown")
	require.NoError(t, err)

	users, err := ListUsersPage(ctx, pool, "", "", 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 3)
	assert.Equal(t, "Alice Adams", users[0].DisplayName)
	assert.Equal(t, "Bob Brown", users[1].DisplayName)
	assert.Equal(t, "Zoe Zhang", users[2].DisplayName)

	total, err := CountUsersPage(ctx, pool, "", "")
	require.NoError(t, err)
	assert.Equal(t, 3, total)
}

func TestListUsersPageSearchByName(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_1", "u1", "Alice Adams")
	require.NoError(t, err)
	_, err = GetOrCreateUserByIdentity(ctx, pool, "slack", "U_2", "u2", "Bob Brown")
	require.NoError(t, err)
	_, err = GetOrCreateUserByIdentity(ctx, pool, "slack", "U_3", "u3", "Alicia Keys")
	require.NoError(t, err)

	// Case-insensitive partial match on display_name.
	users, err := ListUsersPage(ctx, pool, "", "ali", 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, "Alice Adams", users[0].DisplayName)
	assert.Equal(t, "Alicia Keys", users[1].DisplayName)

	total, err := CountUsersPage(ctx, pool, "", "ali")
	require.NoError(t, err)
	assert.Equal(t, 2, total)
}

func TestListUsersPageSearchByEmail(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u1, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_E1", "u1", "Alice Adams")
	require.NoError(t, err)
	require.NoError(t, UpdateUserEmail(ctx, pool, u1.ID, "alice@example.com"))

	u2, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_E2", "u2", "Bob Brown")
	require.NoError(t, err)
	require.NoError(t, UpdateUserEmail(ctx, pool, u2.ID, "bob@example.com"))

	// Search by email fragment — should only match Alice.
	users, err := ListUsersPage(ctx, pool, "", "alice@example", 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "Alice Adams", users[0].DisplayName)

	// Search fragment matching both emails.
	users, err = ListUsersPage(ctx, pool, "", "example.com", 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 2)
}

func TestListUsersPageSearchNoMatch(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_NM", "u", "Alice Adams")
	require.NoError(t, err)

	users, err := ListUsersPage(ctx, pool, "", "nonexistent", 10, 0)
	require.NoError(t, err)
	assert.Empty(t, users)

	total, err := CountUsersPage(ctx, pool, "", "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, 0, total)
}

func TestListUsersPageStatusFilter(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_S1", "u1", "Alice Adams")
	require.NoError(t, err)
	require.NoError(t, UpdateIdentityStatus(ctx, pool, "slack", "U_S1", "active"))

	_, err = GetOrCreateUserByIdentity(ctx, pool, "slack", "U_S2", "u2", "Bob Brown")
	require.NoError(t, err)
	require.NoError(t, UpdateIdentityStatus(ctx, pool, "slack", "U_S2", "deactivated"))

	users, err := ListUsersPage(ctx, pool, "active", "", 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "Alice Adams", users[0].DisplayName)
	assert.Equal(t, "active", users[0].PlatformStatus)

	total, err := CountUsersPage(ctx, pool, "active", "")
	require.NoError(t, err)
	assert.Equal(t, 1, total)
}

func TestListUsersPageStatusAndSearchCombined(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_C1", "u1", "Alice Adams")
	require.NoError(t, err)
	require.NoError(t, UpdateIdentityStatus(ctx, pool, "slack", "U_C1", "active"))

	_, err = GetOrCreateUserByIdentity(ctx, pool, "slack", "U_C2", "u2", "Alice Brown")
	require.NoError(t, err)
	require.NoError(t, UpdateIdentityStatus(ctx, pool, "slack", "U_C2", "deactivated"))

	// "Alice" matches both names, but only one is active.
	users, err := ListUsersPage(ctx, pool, "active", "alice", 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "Alice Adams", users[0].DisplayName)

	total, err := CountUsersPage(ctx, pool, "active", "alice")
	require.NoError(t, err)
	assert.Equal(t, 1, total)
}

func TestListUsersPagePagination(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Create 5 users alphabetically: A, B, C, D, E.
	for _, name := range []string{"A", "B", "C", "D", "E"} {
		_, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_"+name, name, name)
		require.NoError(t, err)
	}

	// Page 1: limit=2, offset=0 → A, B.
	users, err := ListUsersPage(ctx, pool, "", "", 2, 0)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, "A", users[0].DisplayName)
	assert.Equal(t, "B", users[1].DisplayName)

	// Page 2: limit=2, offset=2 → C, D.
	users, err = ListUsersPage(ctx, pool, "", "", 2, 2)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, "C", users[0].DisplayName)
	assert.Equal(t, "D", users[1].DisplayName)

	// Page 3: limit=2, offset=4 → E (last page, partial).
	users, err = ListUsersPage(ctx, pool, "", "", 2, 4)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "E", users[0].DisplayName)

	total, err := CountUsersPage(ctx, pool, "", "")
	require.NoError(t, err)
	assert.Equal(t, 5, total)
}
