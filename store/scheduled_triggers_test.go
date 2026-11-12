package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestUpsertManagedScheduledTriggerOnePerManagerAndName(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Two jobs managed by the same manager — both must exist independently.
	first, err := UpsertManagedScheduledTrigger(ctx, pool, UpsertManagedScheduledTriggerParams{
		Name: "Meeting Reminders", Schedule: "1m", ManagedBy: "jobs", Enabled: true,
	})
	require.NoError(t, err)
	second, err := UpsertManagedScheduledTrigger(ctx, pool, UpsertManagedScheduledTriggerParams{
		Name: "Calendar Feed Sync", Schedule: "30m", ManagedBy: "jobs", Enabled: true,
	})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID, "distinct names must get distinct triggers")

	// Re-seeding each updates in place — no duplicates, schedules preserved per name.
	firstAgain, err := UpsertManagedScheduledTrigger(ctx, pool, UpsertManagedScheduledTriggerParams{
		Name: "Meeting Reminders", Schedule: "1m", ManagedBy: "jobs", Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, firstAgain.ID)

	var count int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM trigger_sources ts
		JOIN scheduled_trigger_config stc ON stc.trigger_id = ts.id
		WHERE ts.managed_by = 'jobs'
	`).Scan(&count))
	assert.Equal(t, 2, count, "re-seeding must not create duplicate triggers")

	// Schedules stay per-trigger.
	var firstSchedule, secondSchedule string
	require.NoError(t, pool.QueryRow(ctx, `SELECT schedule FROM scheduled_trigger_config WHERE trigger_id=$1`, first.ID).Scan(&firstSchedule))
	require.NoError(t, pool.QueryRow(ctx, `SELECT schedule FROM scheduled_trigger_config WHERE trigger_id=$1`, second.ID).Scan(&secondSchedule))
	assert.Equal(t, "1m", firstSchedule)
	assert.Equal(t, "30m", secondSchedule, "one seed's schedule must never clobber another's")
}
