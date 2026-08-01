package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestUpsertAndListSlackChannels(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	err := UpsertSlackChannel(ctx, pool, "C001", "general", false)
	require.NoError(t, err)

	err = UpsertSlackChannel(ctx, pool, "C001", "general-renamed", false)
	require.NoError(t, err)

	channels, err := ListSlackChannels(ctx, pool)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.Equal(t, "general-renamed", channels[0].Name)
}

func TestListSlackChannels_ExcludesArchived(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	UpsertSlackChannel(ctx, pool, "C001", "active", false)
	UpsertSlackChannel(ctx, pool, "C002", "archived", true)

	channels, err := ListSlackChannels(ctx, pool)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.Equal(t, "C001", channels[0].SlackChannelID)
}

func TestSetUserChannelApprovals_ReplaceAll(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	alice, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALICE_CH", "Alice", "Alice")
	require.NoError(t, err)

	UpsertSlackChannel(ctx, pool, "C001", "alpha", false)
	UpsertSlackChannel(ctx, pool, "C002", "beta", false)

	err = SetUserChannelApprovals(ctx, pool, alice.ID, []string{"C001", "C002"})
	require.NoError(t, err)

	ids, err := GetUserChannelApprovals(ctx, pool, alice.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"C001", "C002"}, ids)

	err = SetUserChannelApprovals(ctx, pool, alice.ID, []string{"C002"})
	require.NoError(t, err)

	ids, err = GetUserChannelApprovals(ctx, pool, alice.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"C002"}, ids)
}

func TestGetChannelApprovers(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	alice, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALICE2_CH", "Alice", "Alice")
	require.NoError(t, err)
	bob, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_BOB_CH", "Bob", "Bob")
	require.NoError(t, err)

	UpsertSlackChannel(ctx, pool, "C010", "alpha", false)

	SetUserChannelApprovals(ctx, pool, alice.ID, []string{"C010"})
	SetUserChannelApprovals(ctx, pool, bob.ID, []string{"C010"})

	approvers, err := GetChannelApprovers(ctx, pool, "C010")
	require.NoError(t, err)
	require.Len(t, approvers, 2)
}

func TestGetChannelApprovers_ReturnsEmptyForUnassigned(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	UpsertSlackChannel(ctx, pool, "C020", "unassigned", false)

	approvers, err := GetChannelApprovers(ctx, pool, "C020")
	require.NoError(t, err)
	require.Empty(t, approvers)
}
