package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestSchedulerSeedDefaults(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	defaults := map[string]string{
		"zoom_meeting_sync_enabled":          "true",
		"zoom_meeting_sync_interval_minutes": "15",
		"legislation_sync_enabled":           "true",
		"legislation_sync_interval_minutes":  "1440",
		"slack_user_sync_enabled":            "true",
		"slack_user_sync_interval_minutes":   "1440",
		"meeting_reminders_enabled":          "true",
		"meeting_reminders_interval_minutes": "5",
	}

	for key, want := range defaults {
		t.Run(key, func(t *testing.T) {
			got, err := GetServiceConfig(ctx, pool, "jobs", key, encKey)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestSchedulerEnabledRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "jobs", "zoom_meeting_sync_enabled", "false", false, nil, encKey))
	got, err := GetServiceConfig(ctx, pool, "jobs", "zoom_meeting_sync_enabled", encKey)
	require.NoError(t, err)
	assert.Equal(t, "false", got)

	require.NoError(t, SetServiceConfig(ctx, pool, "jobs", "zoom_meeting_sync_enabled", "true", false, nil, encKey))
	got, err = GetServiceConfig(ctx, pool, "jobs", "zoom_meeting_sync_enabled", encKey)
	require.NoError(t, err)
	assert.Equal(t, "true", got)
}

func TestSchedulerIntervalRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "jobs", "zoom_meeting_sync_interval_minutes", "30", false, nil, encKey))
	got, err := GetServiceConfig(ctx, pool, "jobs", "zoom_meeting_sync_interval_minutes", encKey)
	require.NoError(t, err)
	assert.Equal(t, "30", got)
}
