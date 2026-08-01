package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestBulkInsertCalendarEventsInsertsAll(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	c := &Calendar{Name: "Events Cal", Slug: "events-cal", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, c))

	now := time.Now().UTC().Truncate(time.Second)
	events := []CalendarEvent{
		{Title: "Meeting A", StartTime: now.Add(1 * time.Hour)},
		{Title: "Meeting B", StartTime: now.Add(2 * time.Hour)},
		{Title: "Meeting C", StartTime: now.Add(3 * time.Hour)},
	}

	inserted, err := BulkInsertCalendarEvents(ctx, pool, c.ID, events)
	require.NoError(t, err)
	assert.Equal(t, 3, inserted)

	got, err := ListCalendarEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestBulkInsertCalendarEventsSkipsDuplicates(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	c := &Calendar{Name: "Dedup Cal", Slug: "dedup-cal", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, c))

	start := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	events := []CalendarEvent{
		{Title: "Council Meeting", StartTime: start},
	}

	first, err := BulkInsertCalendarEvents(ctx, pool, c.ID, events)
	require.NoError(t, err)
	assert.Equal(t, 1, first)

	// Same event again — should be skipped.
	second, err := BulkInsertCalendarEvents(ctx, pool, c.ID, events)
	require.NoError(t, err)
	assert.Equal(t, 0, second, "duplicate should be skipped")

	got, err := ListCalendarEvents(ctx, pool, c.ID)
	require.NoError(t, err)
	assert.Len(t, got, 1, "only one row should exist after dedup")
}

func TestCountUpcomingEventsWindowFilter(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	c := &Calendar{Name: "Window Cal", Slug: "window-cal", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, c))

	now := time.Now().UTC()
	events := []CalendarEvent{
		{Title: "Soon", StartTime: now.Add(12 * time.Hour)},      // within 1 day
		{Title: "Later", StartTime: now.Add(5 * 24 * time.Hour)}, // within 7 days, not 1
		{Title: "Far", StartTime: now.Add(30 * 24 * time.Hour)},  // outside 7 days
		{Title: "Past", StartTime: now.Add(-1 * time.Hour)},      // in the past
	}
	_, err := BulkInsertCalendarEvents(ctx, pool, c.ID, events)
	require.NoError(t, err)

	within1, err := CountUpcomingEvents(ctx, pool, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, within1, "only the 12h event is within 1 day")

	within7, err := CountUpcomingEvents(ctx, pool, 7)
	require.NoError(t, err)
	assert.Equal(t, 2, within7, "12h and 5-day events are within 7 days")
}

func TestGetCalendarByID(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	cal := &Calendar{
		Name:     "Test Cal",
		Slug:     "test-cal",
		Timezone: "America/Chicago",
	}
	require.NoError(t, CreateCalendar(ctx, pool, cal))
	require.NotEmpty(t, cal.ID)

	got, err := GetCalendarByID(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Equal(t, cal.ID, got.ID)
	assert.Equal(t, "Test Cal", got.Name)
	assert.Nil(t, got.IcalURL)

	_, err = GetCalendarByID(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.Error(t, err)
}

func TestCreateCalendar_WithIcalURL(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	url := "https://example.com/feed.ics"
	cal := &Calendar{
		Name:     "External Cal",
		Slug:     "external-cal",
		Timezone: "America/New_York",
		IcalURL:  &url,
	}
	require.NoError(t, CreateCalendar(ctx, pool, cal))

	got, err := GetCalendarByID(ctx, pool, cal.ID)
	require.NoError(t, err)
	require.NotNil(t, got.IcalURL)
	assert.Equal(t, url, *got.IcalURL)
}

func TestCreateCalendarStoresDisplayOrder(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	cal := &Calendar{Name: "Ordered", Slug: "ordered", Timezone: "UTC", DisplayOrder: 5}
	require.NoError(t, CreateCalendar(ctx, pool, cal))

	got, err := GetCalendarByID(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Equal(t, 5, got.DisplayOrder)
}

func TestUpdateCalendarUpdatesDisplayOrder(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	cal := &Calendar{Name: "Cal", Slug: "cal", Timezone: "UTC", DisplayOrder: 1}
	require.NoError(t, CreateCalendar(ctx, pool, cal))

	cal.DisplayOrder = 10
	require.NoError(t, UpdateCalendar(ctx, pool, cal))

	got, err := GetCalendarByID(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Equal(t, 10, got.DisplayOrder)
}

func TestListCalendarsOrdersByDisplayOrderThenName(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	ctx := context.Background()

	// Insert in an order that would be wrong if sorted only by name.
	cals := []Calendar{
		{Name: "Zebra", Slug: "zebra", Timezone: "UTC", DisplayOrder: 2},
		{Name: "Alpha", Slug: "alpha", Timezone: "UTC", DisplayOrder: 2},
		{Name: "Middle", Slug: "middle", Timezone: "UTC", DisplayOrder: 1},
		{Name: "First", Slug: "first", Timezone: "UTC", DisplayOrder: 0},
	}
	for i := range cals {
		require.NoError(t, CreateCalendar(ctx, pool, &cals[i]))
	}

	got, err := ListCalendars(ctx, pool)
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, "First", got[0].Name)  // display_order 0
	assert.Equal(t, "Middle", got[1].Name) // display_order 1
	assert.Equal(t, "Alpha", got[2].Name)  // display_order 2, A < Z
	assert.Equal(t, "Zebra", got[3].Name)  // display_order 2, Z > A
}
