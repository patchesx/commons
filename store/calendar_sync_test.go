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

// A sync that can't store its events must not be a sync that fails forever: titles that collide at the same
// start time have to resolve, not roll the whole calendar back.
func TestReconcileTitleCollisions(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 6, 1, 15, 0, 0, 0, time.UTC)
	other := time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)

	titles := func(t *testing.T, pool *pgxpool.Pool, cal *Calendar) map[string]string { // uid -> title
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT uid, title FROM calendar_events WHERE calendar_id = $1`, cal.ID)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var uid, title string
			require.NoError(t, rows.Scan(&uid, &title))
			out[uid] = title
		}
		return out
	}
	seed := func(t *testing.T) (*pgxpool.Pool, *Calendar) {
		pool, cal := feedCalFixture(t)
		_, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "a", Title: "Meeting", StartTime: start},
			{UID: "b", Title: "Meeting (draft)", StartTime: start},
			{UID: "z", Title: "Unrelated", StartTime: other},
		})
		require.NoError(t, err)
		return pool, cal
	}

	t.Run("two events swapping titles at the same start", func(t *testing.T) {
		pool, cal := seed(t)
		res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "a", Title: "Meeting (draft)", StartTime: start},
			{UID: "b", Title: "Meeting", StartTime: start},
			{UID: "z", Title: "Unrelated", StartTime: other},
		})
		require.NoError(t, err)
		assert.Equal(t, 2, res.Updated)
		assert.Equal(t, map[string]string{"a": "Meeting (draft)", "b": "Meeting", "z": "Unrelated"}, titles(t, pool, cal))
	})

	t.Run("a title moving to where another one just left", func(t *testing.T) {
		pool, cal := seed(t)
		_, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "a", Title: "Meeting (final)", StartTime: start},
			{UID: "b", Title: "Meeting", StartTime: start},
			{UID: "z", Title: "Unrelated", StartTime: other},
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"a": "Meeting (final)", "b": "Meeting", "z": "Unrelated"}, titles(t, pool, cal))
	})

	t.Run("a new event can take a title another just vacated", func(t *testing.T) {
		pool, cal := seed(t)
		res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "n", Title: "Meeting", StartTime: start},
			{UID: "a", Title: "Meeting (moved)", StartTime: start},
			{UID: "b", Title: "Meeting (draft)", StartTime: start},
			{UID: "z", Title: "Unrelated", StartTime: other},
		})
		require.NoError(t, err)
		assert.Equal(t, 1, res.Inserted, "the new event isn't dropped for a conflict that's about to clear")
		assert.Equal(t, map[string]string{"n": "Meeting", "a": "Meeting (moved)", "b": "Meeting (draft)", "z": "Unrelated"}, titles(t, pool, cal))
	})

	t.Run("two events that really are the same title and start: the first stays, the sync still succeeds", func(t *testing.T) {
		pool, cal := seed(t)
		res, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "a", Title: "Meeting", StartTime: start},
			{UID: "b", Title: "Meeting", StartTime: start},
			{UID: "z", Title: "Unrelated after", StartTime: other},
		})
		require.NoError(t, err, "one duplicate doesn't block the calendar")
		assert.Equal(t, map[string]string{"a": "Meeting", "z": "Unrelated after"}, titles(t, pool, cal))
		assert.Equal(t, 1, res.Deleted, "b is no longer in the feed as its own event")
		assert.Equal(t, 1, res.Updated, "and the rest of the feed still applied")

		// Syncing the same feed again changes nothing.
		again, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "a", Title: "Meeting", StartTime: start},
			{UID: "b", Title: "Meeting", StartTime: start},
			{UID: "z", Title: "Unrelated after", StartTime: other},
		})
		require.NoError(t, err)
		assert.Equal(t, SyncResult{}, again)
	})

	t.Run("a failed sync still leaves nothing parked", func(t *testing.T) {
		pool, cal := seed(t)
		_, err := ReconcileFeedEvents(ctx, pool, cal.ID, []FeedOccurrence{
			{UID: "a", Title: "Meeting renamed", StartTime: start},
			{UID: "b", Title: "Meeting (draft)", StartTime: start},
			{UID: "z", Title: "Unrelated", StartTime: other},
		})
		require.NoError(t, err)
		for _, title := range titles(t, pool, cal) {
			assert.NotContains(t, title, "sync-parked")
		}
	})
}
