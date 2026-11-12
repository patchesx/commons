package slack

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
	"github.com/teambition/rrule-go"

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
// Composites show their merged member events; syndicated calendars fetch their feed live;
// manual calendars read from the calendar_events table.
// Always returns at least a divider + title block; shows a "no events" notice when the list is empty.
// If accessory is non-nil it is placed alongside the "Upcoming Events" title (e.g. a calendar-switcher dropdown).
func calendarBlocksForCalendar(ctx context.Context, pool *pgxpool.Pool, cal store.Calendar, accessory slacklib.BlockElement) []slacklib.Block {
	loc, err := time.LoadLocation(cal.Timezone)
	if err != nil || loc == nil {
		loc = util.FallbackLocation()
	}

	var events []calEvent
	var fetchErr error
	switch {
	case cal.Kind == store.CalendarKindComposite || cal.Kind == store.CalendarKindPersonal:
		events, fetchErr = fetchCompositeEvents(ctx, pool, cal, loc)
		if fetchErr != nil {
			log.Printf("slack/apphome: composite events for calendar %s: %v", cal.ID, fetchErr)
		}
	case cal.IcalURL != nil && *cal.IcalURL != "":
		events, fetchErr = fetchICalEvents(ctx, *cal.IcalURL, loc)
		if fetchErr != nil {
			log.Printf("slack/apphome: ical fetch for calendar %s: %v", cal.ID, fetchErr)
		}
	default:
		events, fetchErr = fetchInternalEvents(ctx, pool, cal.ID)
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

// fetchICalEvents fetches and filters upcoming events from an iCal URL.
// Uses go-ical + rrule-go to correctly expand recurring events including
// positional BYDAY rules (e.g. FREQ=MONTHLY;BYDAY=1WE).
func fetchICalEvents(ctx context.Context, calURL string, loc *time.Location) ([]calEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, calURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calendar fetch returned HTTP %d", resp.StatusCode)
	}

	now := time.Now()
	nowLocal := now.In(loc)
	// Start of today in the display timezone so all-day events starting today are included.
	windowStart := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc)
	// End at 23:59:59 of day 7 in the display timezone.
	windowEnd := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day()+7, 23, 59, 59, 0, loc)

	cal, err := ical.NewDecoder(resp.Body).Decode()
	if err != nil {
		return nil, err
	}

	var events []calEvent
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		summaryProp := comp.Props.Get(ical.PropSummary)
		if summaryProp == nil {
			continue
		}
		title := summaryProp.Value

		dtStartProp := comp.Props.Get(ical.PropDateTimeStart)
		if dtStartProp == nil {
			continue
		}
		dtEndProp := comp.Props.Get(ical.PropDateTimeEnd)

		allDay := !strings.Contains(dtStartProp.Value, "T")

		dtStart, err := dtStartProp.DateTime(loc)
		if err != nil {
			continue
		}
		var dtEnd time.Time
		if dtEndProp != nil {
			dtEnd, _ = dtEndProp.DateTime(loc)
		}
		if dtEnd.IsZero() {
			if allDay {
				dtEnd = dtStart.AddDate(0, 0, 1)
			} else {
				dtEnd = dtStart.Add(time.Hour)
			}
		}

		rruleProp := comp.Props.Get(ical.PropRecurrenceRule)
		if rruleProp != nil {
			opt, err := rrule.StrToROption(rruleProp.Value)
			if err != nil {
				continue
			}
			opt.Dtstart = dtStart
			r, err := rrule.NewRRule(*opt)
			if err != nil {
				continue
			}
			set := &rrule.Set{}
			set.RRule(r)
			for _, exProp := range comp.Props[ical.PropExceptionDates] {
				if exTime, err := exProp.DateTime(loc); err == nil {
					set.ExDate(exTime)
				}
			}
			duration := dtEnd.Sub(dtStart)
			for _, occ := range set.Between(windowStart, windowEnd, true) {
				occEnd := occ.Add(duration)
				if !allDay && occEnd.Before(now) {
					continue
				}
				events = append(events, calEvent{Title: title, Start: occ, End: occEnd, AllDay: allDay})
			}
		} else {
			if dtEnd.Before(now) || dtStart.After(windowEnd) {
				continue
			}
			events = append(events, calEvent{Title: title, Start: dtStart, End: dtEnd, AllDay: allDay})
		}
	}

	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events, nil
}

// fetchCompositeEvents returns the merged, deduplicated events of a composite (or
// personal) calendar within the next 7 days, mirroring the window used by the other
// fetchers. Feed members are fetched via fetchICalEvents; manual members come from
// the calendar_events table.
func fetchCompositeEvents(ctx context.Context, pool *pgxpool.Pool, cal store.Calendar, loc *time.Location) ([]calEvent, error) {
	fetcher := func(ctx context.Context, url string) ([]store.CalendarEvent, error) {
		evts, err := fetchICalEvents(ctx, url, loc)
		if err != nil {
			return nil, err
		}
		out := make([]store.CalendarEvent, 0, len(evts))
		for _, e := range evts {
			evt := store.CalendarEvent{Title: e.Title, StartTime: e.Start, AllDay: e.AllDay}
			if !e.End.IsZero() {
				end := e.End
				evt.EndTime = &end
			}
			out = append(out, evt)
		}
		return out, nil
	}
	merged, err := store.ListCompositeEvents(ctx, pool, cal.ID, fetcher)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	nowLocal := now.In(loc)
	windowEnd := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day()+7, 23, 59, 59, 0, loc)
	var events []calEvent
	for _, e := range merged {
		// Keep events that are upcoming or still ongoing; drop past ones.
		upcoming := !e.StartTime.Before(now)
		ongoing := e.EndTime != nil && e.EndTime.After(now)
		if (!upcoming && !ongoing) || e.StartTime.After(windowEnd) {
			continue
		}
		var end time.Time
		if e.EndTime != nil {
			end = *e.EndTime
		}
		events = append(events, calEvent{Title: e.Title, Start: e.StartTime, End: end, AllDay: e.AllDay})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events, nil
}

// fetchInternalEvents reads upcoming events from the calendar_events table for a given calendar.
func fetchInternalEvents(ctx context.Context, pool *pgxpool.Pool, calendarID string) ([]calEvent, error) {
	now := time.Now()
	rows, err := store.ListUpcomingEventsForCalendar(ctx, pool, calendarID, 7)
	if err != nil {
		return nil, err
	}
	var events []calEvent
	for _, e := range rows {
		var end time.Time
		if e.EndTime != nil {
			end = *e.EndTime
		}
		// Skip multi-day events that have already ended (started before today but end before now).
		if !end.IsZero() && end.Before(now) {
			continue
		}
		events = append(events, calEvent{
			Title:  e.Title,
			Start:  e.StartTime,
			End:    end,
			AllDay: e.AllDay,
		})
	}
	// Sort defensively; ListUpcomingEventsForCalendar orders by start_time but defensive sort is cheap.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Start.Before(events[j].Start)
	})
	return events, nil
}
