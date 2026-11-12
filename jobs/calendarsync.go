package jobs

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/ical"
	"commons/store"
)

// feedSyncWindow is how far back and forward feed occurrences are expanded and
// stored: one year of history, two years of future. The window rolls forward
// with each sync — occurrences aging out are deleted, new ones inserted.
func feedSyncWindow(now time.Time) (time.Time, time.Time) {
	return now.AddDate(-1, 0, 0), now.AddDate(2, 0, 0)
}

// SyncCalendarFeeds syncs every syndicated calendar's feed. Used by the
// scheduled job (jobs.calendar_feed_sync, every 30m). Each calendar syncs
// independently — one failing feed doesn't block the others.
func SyncCalendarFeeds(ctx context.Context, pool *pgxpool.Pool) {
	cals, err := store.ListFeedCalendars(ctx, pool)
	if err != nil {
		log.Printf("jobs/calendarsync: list feed calendars: %v", err)
		return
	}
	for _, cal := range cals {
		if _, err := SyncFeedCalendar(ctx, pool, &cal); err != nil {
			log.Printf("jobs/calendarsync: sync %s (%s): %v", cal.Name, cal.ID, err)
		}
	}
}

// SyncFeedCalendar fetches one calendar's feed, expands occurrences over the
// sync window in the calendar's timezone, and reconciles them into stored
// events. On success the calendar's last_synced_at is stamped. Exported for
// the admin "Sync now" action and the initial import at calendar creation.
func SyncFeedCalendar(ctx context.Context, pool *pgxpool.Pool, cal *store.Calendar) (store.SyncResult, error) {
	loc, err := time.LoadLocation(cal.Timezone)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	now := time.Now()
	from, to := feedSyncWindow(now)

	events, err := ical.Fetch(ctx, *cal.IcalURL, loc)
	if err != nil {
		return store.SyncResult{}, err
	}
	occurrences := ical.ExpandAll(events, from, to, loc)

	feedOccs := make([]store.FeedOccurrence, 0, len(occurrences))
	for _, occ := range occurrences {
		feedOccs = append(feedOccs, store.FeedOccurrence{
			UID:         occ.UID,
			Title:       occ.Title,
			Description: occ.Description,
			Location:    occ.Location,
			URL:         occ.URL,
			StartTime:   occ.StartTime,
			EndTime:     occ.EndTime,
			AllDay:      occ.AllDay,
		})
	}

	res, err := store.ReconcileFeedEvents(ctx, pool, cal.ID, feedOccs)
	if err != nil {
		return res, err
	}
	if err := store.MarkCalendarSynced(ctx, pool, cal.ID); err != nil {
		return res, err
	}
	return res, nil
}
