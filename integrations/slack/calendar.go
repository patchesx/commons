package slack

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
	"commons/util"
)

type calEvent struct {
	Title  string
	Start  time.Time
	End    time.Time
	AllDay bool
}

// calendarBlocksForCalendar returns Slack blocks for the upcoming events section of a calendar.
// Composites show their merged member occurrences; standard calendars — manual or
// syndicated — read their stored events (feed calendars' rows are kept fresh by
// the sync job).
// Always returns at least a divider + title block; shows a "no events" notice when the list is empty.
// If accessory is non-nil it is placed alongside the "Upcoming Events" title (e.g. a calendar-switcher dropdown).
func calendarBlocksForCalendar(ctx context.Context, pool *pgxpool.Pool, cal store.Calendar, accessory slacklib.BlockElement) []slacklib.Block {
	loc, err := time.LoadLocation(cal.Timezone)
	if err != nil || loc == nil {
		loc = util.FallbackLocation()
	}

	var events []calEvent
	var fetchErr error
	if cal.Kind == store.CalendarKindComposite || cal.Kind == store.CalendarKindPersonal {
		events, fetchErr = fetchCompositeEvents(ctx, pool, cal, loc)
		if fetchErr != nil {
			log.Printf("slack/apphome: composite events for calendar %s: %v", cal.ID, fetchErr)
		}
	} else {
		events, fetchErr = fetchInternalEvents(ctx, pool, cal.ID, loc)
		if fetchErr != nil {
			log.Printf("slack/apphome: internal events for calendar %s: %v", cal.ID, fetchErr)
		}
	}

	var blks []slacklib.Block
	blks = append(blks, slacklib.NewDividerBlock())
	if accessory != nil {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "*Upcoming Events*", false, false),
			nil,
			slacklib.NewAccessory(accessory),
		))
	} else {
		blks = append(blks, slacklib.NewHeaderBlock(
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Upcoming Events", false, false),
		))
	}

	if len(events) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No events in the next 7 days._", false, false),
			nil, nil,
		))
		return blks
	}

	const maxFields = 2
	for i := 0; i < len(events); i += maxFields {
		end := i + maxFields
		if end > len(events) {
			end = len(events)
		}
		var fields []*slacklib.TextBlockObject
		for _, e := range events[i:end] {
			start := e.Start.In(loc)
			var when string
			if e.AllDay {
				when = start.Format("Mon, Jan 2")
			} else {
				when = start.Format("Mon, Jan 2 · 3:04 PM MST")
			}
			fields = append(fields, slacklib.NewTextBlockObject(
				slacklib.MarkdownType,
				fmt.Sprintf("*%s*\n%s", e.Title, when),
				false, false,
			))
		}
		blks = append(blks, slacklib.NewSectionBlock(nil, fields, nil))
	}
	return blks
}

// fetchCompositeEvents returns the merged, deduplicated occurrences of a
// composite (or personal) calendar within the next 7 days, mirroring the window
// used by fetchInternalEvents. All members — manual or syndicated — read their
// stored events (feed members' rows are kept fresh by the sync job).
func fetchCompositeEvents(ctx context.Context, pool *pgxpool.Pool, cal store.Calendar, loc *time.Location) ([]calEvent, error) {
	now := time.Now()
	nowLocal := now.In(loc)
	windowStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	windowEnd := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day()+7, 23, 59, 59, 0, loc)

	merged, err := store.ListCompositeOccurrences(ctx, pool, cal.ID, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}
	var events []calEvent
	for _, o := range merged {
		// ListCompositeOccurrences passes standalone stored events through
		// unclipped (composite ICS export needs them all) — this view must
		// apply its own 7-day upper bound or App Home balloons past Slack's
		// block limit on feed-heavy members.
		if o.StartTime.After(windowEnd) {
			continue
		}
		if !o.StartTime.Before(now) || (o.EndTime != nil && o.EndTime.After(now)) {
			var end time.Time
			if o.EndTime != nil {
				end = *o.EndTime
			}
			events = append(events, calEvent{Title: o.Title, Start: o.StartTime, End: end, AllDay: o.AllDay})
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events, nil
}

// fetchInternalEvents reads upcoming events from the calendar_events table for a
// given calendar, expanded over the 7-day display window in loc. All-day events
// starting today are included (the window starts at midnight local).
func fetchInternalEvents(ctx context.Context, pool *pgxpool.Pool, calendarID string, loc *time.Location) ([]calEvent, error) {
	now := time.Now()
	nowLocal := now.In(loc)
	windowStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	windowEnd := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day()+7, 23, 59, 59, 0, loc)

	occs, err := store.ListCalendarOccurrences(ctx, pool, calendarID, windowStart, windowEnd)
	if err != nil {
		return nil, err
	}
	var events []calEvent
	for _, o := range occs {
		// Skip occurrences that already ended (ongoing multi-day events stay).
		if o.EndTime != nil && o.EndTime.Before(now) {
			continue
		}
		if o.StartTime.Before(windowStart) {
			continue
		}
		var end time.Time
		if o.EndTime != nil {
			end = *o.EndTime
		}
		events = append(events, calEvent{Title: o.Title, Start: o.StartTime, End: end, AllDay: o.AllDay})
	}
	// Sort defensively; the store sorts, but defensive sort is cheap.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Start.Before(events[j].Start)
	})
	return events, nil
}
