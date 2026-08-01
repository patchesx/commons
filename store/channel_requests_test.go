package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestListPendingRequestsForChannelsEmptySlice(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Empty channel list should short-circuit and return empty, not hit the DB.
	got, err := ListPendingRequestsForChannels(ctx, pool, []string{})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListPendingRequestsForChannelsFiltersCorrectly(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REQ", "requser", "Req User")
	require.NoError(t, err)

	// Two requests for different channels.
	r1 := &ChannelRequest{RequesterID: &u.ID, SlackChannelID: "C_ALPHA", SlackChannelName: "alpha"}
	r2 := &ChannelRequest{RequesterID: &u.ID, SlackChannelID: "C_BETA", SlackChannelName: "beta"}
	require.NoError(t, CreateChannelRequest(ctx, pool, r1))
	require.NoError(t, CreateChannelRequest(ctx, pool, r2))

	// Query for only C_ALPHA — should not return C_BETA.
	got, err := ListPendingRequestsForChannels(ctx, pool, []string{"C_ALPHA"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "C_ALPHA", got[0].SlackChannelID)
}

func TestListPendingRequestsForChannelsExcludesNonPending(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REQ2", "requser2", "Req User 2")
	require.NoError(t, err)

	r := &ChannelRequest{RequesterID: &u.ID, SlackChannelID: "C_GAMMA", SlackChannelName: "gamma"}
	require.NoError(t, CreateChannelRequest(ctx, pool, r))

	// Approve the request — it should no longer appear in pending results.
	reviewer, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REV", "reviewer", "Reviewer")
	require.NoError(t, err)
	require.NoError(t, UpdateRequestStatus(ctx, pool, r.ID, ChannelRequestApproved, reviewer.ID))

	got, err := ListPendingRequestsForChannels(ctx, pool, []string{"C_GAMMA"})
	require.NoError(t, err)
	assert.Empty(t, got, "approved request should not appear in pending results")
}
