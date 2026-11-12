package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

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

func TestUpdatesPopulateUpdatedAt(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	cal := &Calendar{Name: "Stamps", Slug: "stamps", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))

	evt := &CalendarEvent{Title: "Event", StartTime: time.Now().Add(24 * time.Hour)}
	evt.CalendarID = cal.ID
	require.NoError(t, CreateCalendarEvent(ctx, pool, evt))

	// Capture pre-update timestamps, then ensure the update lands in a later transaction.
	before := *GetCalendarByIDOrFatal(t, ctx, pool, cal.ID)
	evtBefore := GetCalendarEventByIDOrFatal(t, ctx, pool, evt.ID)
	time.Sleep(20 * time.Millisecond)

	cal.Name = "Stamps 2"
	require.NoError(t, UpdateCalendar(ctx, pool, cal))
	evt.Title = "Event 2"
	require.NoError(t, UpdateCalendarEvent(ctx, pool, evt))

	got := GetCalendarByIDOrFatal(t, ctx, pool, cal.ID)
	assert.True(t, got.UpdatedAt.After(before.UpdatedAt), "calendar updated_at should advance on update")
	evtGot := GetCalendarEventByIDOrFatal(t, ctx, pool, evt.ID)
	assert.True(t, evtGot.UpdatedAt.After(evtBefore.UpdatedAt), "event updated_at should advance on update")
}

func GetCalendarByIDOrFatal(t testing.TB, ctx context.Context, pool *pgxpool.Pool, id string) *Calendar {
	t.Helper()
	cal, err := GetCalendarByID(ctx, pool, id)
	require.NoError(t, err)
	return cal
}

func GetCalendarEventByIDOrFatal(t testing.TB, ctx context.Context, pool *pgxpool.Pool, id string) *CalendarEvent {
	t.Helper()
	evt, err := GetCalendarEventByID(ctx, pool, id)
	require.NoError(t, err)
	return evt
}
