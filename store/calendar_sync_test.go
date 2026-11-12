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

func feedCalFixture(t *testing.T) (pool *pgxpool.Pool, cal *Calendar) {
	t.Helper()
	ctx := context.Background()
	p := testhelpers.SetupTestDB(t)
	url := "https://example.com/feed.ics"
	c := &Calendar{Name: "Feed Cal", Slug: "feed-sync-cal", Timezone: "UTC", IcalURL: &url}
	require.NoError(t, CreateCalendar(ctx, p, c))
	return p, c
}

func TestReconcileInsertsUpdatesDeletes(t *testing.T) {
	ctx := context.Background()
	pool, cal := feedCalFixture(t)
	day := func(d int) time.Time { return time.Date(2026, 6, d, 15, 0, 0, 0, time.UTC) }

	// First sync: two occurrences insert.
	res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
		{UID: "a-1", Title: "Event A", StartTime: day(1)},
		{UID: "b-1", Title: "Event B", StartTime: day(2)},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Inserted)
	assert.Equal(t, 0, res.Updated)
	assert.Equal(t, 0, res.Deleted)

	// Second sync: A renamed, B vanished, C new.
	res, err = ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
		{UID: "a-1", Title: "Event A (renamed)", StartTime: day(1)},
		{UID: "c-1", Title: "Event C", StartTime: day(3)},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Inserted, "c-1")
	assert.Equal(t, 1, res.Updated, "a-1 renamed")
	assert.Equal(t, 1, res.Deleted, "b-1 vanished upstream")

	evts, err := ListCalendarEvents(ctx, pool, cal.ID)
	require.NoError(t, err)
	require.Len(t, evts, 2)
	titles := []string{evts[0].Title, evts[1].Title}
	assert.Contains(t, titles, "Event A (renamed)")
	assert.Contains(t, titles, "Event C")
	assert.NotContains(t, titles, "Event B")

	// last_synced_at round-trips.
	require.NoError(t, MarkCalendarSynced(ctx, pool, cal.ID))
	ts, ok := GetCalendarLastSynced(ctx, pool, cal.ID)
	require.True(t, ok)
	assert.WithinDuration(t, time.Now(), ts, time.Minute)
}

func TestReconcileSameUIDMultipleOccurrences(t *testing.T) {
	ctx := context.Background()
	pool, cal := feedCalFixture(t)

	// One recurring series, three occurrences (same UID, different starts).
	res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
		{UID: "series-1", Title: "Weekly", StartTime: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)},
		{UID: "series-1", Title: "Weekly", StartTime: time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)},
		{UID: "series-1", Title: "Weekly", StartTime: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Inserted)

	// Next sync: one occurrence cancelled upstream (EXDATE) — only that slot goes.
	res, err = ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
		{UID: "series-1", Title: "Weekly", StartTime: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)},
		{UID: "series-1", Title: "Weekly", StartTime: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deleted)
	assert.Equal(t, 0, res.Updated, "identical remaining occurrences must not be touched")

	evts, err := ListCalendarEvents(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Len(t, evts, 2)
}

func TestReconcileReplacesLegacyUidlessRows(t *testing.T) {
	ctx := context.Background()
	pool, cal := feedCalFixture(t)

	// Legacy import: rows without uid (pre-sync-era BulkInsert behavior).
	legacy := &CalendarEvent{CalendarID: cal.ID, Title: "Legacy Event", StartTime: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)}
	require.NoError(t, CreateCalendarEvent(ctx, pool, legacy))

	// Sync delivers the same event with a uid: the stale row is replaced, not duplicated.
	res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
		{UID: "legacy-1", Title: "Legacy Event", StartTime: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Inserted)
	assert.Equal(t, 1, res.Deleted, "stale uid-less row must be cleaned up")

	evts, err := ListCalendarEvents(ctx, pool, cal.ID)
	require.NoError(t, err)
	require.Len(t, evts, 1)
	assert.Equal(t, "Legacy Event", evts[0].Title)
}

func TestReconcileUidlessFeedOccurrencesDedupeAcrossSyncs(t *testing.T) {
	ctx := context.Background()
	pool, cal := feedCalFixture(t)

	occ := FeedOccurrence{UID: "", Title: "No UID", StartTime: time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)}
	_, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{occ})
	require.NoError(t, err)

	// Re-syncing the same uid-less occurrence must not duplicate the row —
	// the (calendar_id, title, start_time) unique constraint dedupes.
	res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{occ})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Inserted, "duplicate uid-less occurrence is skipped")
	assert.Equal(t, 0, res.Deleted, "a uid-less occurrence still in the feed keeps its row")

	evts, err := ListCalendarEvents(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Len(t, evts, 1, "exactly one row after two syncs")
}

func TestReconcileRejectsNonFeedCalendar(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	manual := &Calendar{Name: "Manual", Slug: "manual-cal", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, manual))
	evt := &CalendarEvent{CalendarID: manual.ID, Title: "Precious Manual Event", StartTime: time.Now().Add(24 * time.Hour)}
	require.NoError(t, CreateCalendarEvent(ctx, pool, evt))

	// Reconcile on a manual calendar must refuse — it would delete manual rows.
	_, err := ReconcileFeedEvents(ctx, pool, manual.ID, nil)
	assert.ErrorIs(t, err, ErrNotFeedCalendar)

	evts, err := ListCalendarEvents(ctx, pool, manual.ID)
	require.NoError(t, err)
	assert.Len(t, evts, 1, "manual events must be untouched")
}

func TestReconcileEmptyFeedDeletesAll(t *testing.T) {
	ctx := context.Background()
	pool, cal := feedCalFixture(t)

	_, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
		{UID: "x-1", Title: "Doomed", StartTime: time.Date(2026, 6, 9, 9, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)

	// An empty feed (all events deleted upstream) legitimately clears the calendar.
	res, err := ReconcileFeedEvents(ctx, pool, cal.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deleted)

	evts, err := ListCalendarEvents(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Empty(t, evts)
}

func TestListFeedCalendars(t *testing.T) {
	ctx := context.Background()
	pool, feedCal := feedCalFixture(t)

	manual := &Calendar{Name: "Manual", Slug: "feed-list-manual", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, manual))

	cals, err := ListFeedCalendars(ctx, pool)
	require.NoError(t, err)
	require.Len(t, cals, 1)
	assert.Equal(t, feedCal.ID, cals[0].ID)
}
