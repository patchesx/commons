package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestListUpcomingMeetingSummariesEmpty(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	var intID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO integrations (type, name) VALUES ('zoom', 'Test') RETURNING id`,
	).Scan(&intID))

	summaries, err := ListUpcomingMeetingSummaries(ctx, pool, intID)
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestListUpcomingMeetingSummariesOneOff(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	var intID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO integrations (type, name) VALUES ('zoom', 'Test') RETURNING id`,
	).Scan(&intID))

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_MTG1", "mtg1", "Mtg User")
	require.NoError(t, err)

	m := &ScheduledMeeting{
		IntegrationID:   intID,
		ZoomMeetingID:   11111,
		Topic:           "One-off Meeting",
		DurationMinutes: 60,
		Timezone:        "America/Chicago",
		IsRecurring:     false,
		CreatedBy:       u.ID,
	}
	occ := MeetingOccurrence{StartTime: time.Now().Add(2 * time.Hour), DurationMinutes: 60, Status: "available"}
	require.NoError(t, CreateScheduledMeeting(ctx, pool, encKey, m, []MeetingOccurrence{occ}))

	summaries, err := ListUpcomingMeetingSummaries(ctx, pool, intID)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "One-off Meeting", summaries[0].Topic)
	assert.False(t, summaries[0].NextStart.IsZero(), "one-off meeting should have NextStart set")
	assert.Empty(t, summaries[0].Occurrences, "one-off meeting should not populate Occurrences slice")
}

func TestListUpcomingMeetingSummariesRecurringOneOccurrencePerSeries(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	var intID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO integrations (type, name) VALUES ('zoom', 'Test') RETURNING id`,
	).Scan(&intID))

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_MTG2", "mtg2", "Mtg User2")
	require.NoError(t, err)

	m := &ScheduledMeeting{
		IntegrationID:   intID,
		ZoomMeetingID:   22222,
		Topic:           "Weekly Standup",
		DurationMinutes: 30,
		Timezone:        "America/Chicago",
		IsRecurring:     true,
		CreatedBy:       u.ID,
	}
	// Three future occurrences — CTE should only return 1 per series.
	now := time.Now()
	occs := []MeetingOccurrence{
		{StartTime: now.Add(1 * 24 * time.Hour), DurationMinutes: 30, Status: "available"},
		{StartTime: now.Add(8 * 24 * time.Hour), DurationMinutes: 30, Status: "available"},
		{StartTime: now.Add(15 * 24 * time.Hour), DurationMinutes: 30, Status: "available"},
	}
	require.NoError(t, CreateScheduledMeeting(ctx, pool, encKey, m, occs))

	summaries, err := ListUpcomingMeetingSummaries(ctx, pool, intID)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.True(t, summaries[0].IsRecurring)
	assert.Len(t, summaries[0].Occurrences, 1, "recurring series should expose only the next occurrence")
}

func TestListUpcomingMeetingSummariesCapsAtTenSeries(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	var intID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO integrations (type, name) VALUES ('zoom', 'Test') RETURNING id`,
	).Scan(&intID))

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_MTG3", "mtg3", "Mtg User3")
	require.NoError(t, err)

	// Create 12 one-off meetings with future occurrences.
	for i := 0; i < 12; i++ {
		m := &ScheduledMeeting{
			IntegrationID:   intID,
			ZoomMeetingID:   int64(30000 + i),
			Topic:           "Meeting",
			DurationMinutes: 60,
			Timezone:        "America/Chicago",
			IsRecurring:     false,
			CreatedBy:       u.ID,
		}
		occ := MeetingOccurrence{
			StartTime:       time.Now().Add(time.Duration(i+1) * time.Hour),
			DurationMinutes: 60,
			Status:          "available",
		}
		require.NoError(t, CreateScheduledMeeting(ctx, pool, encKey, m, []MeetingOccurrence{occ}))
	}

	summaries, err := ListUpcomingMeetingSummaries(ctx, pool, intID)
	require.NoError(t, err)
	assert.Len(t, summaries, 10, "should cap at 10 series")
}
