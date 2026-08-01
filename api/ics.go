package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teambition/rrule-go"

	"commons/store"
)

// ServeICS handles GET /cal/{slug}.ics — public, no auth required.
// Generates and returns a valid iCalendar feed for the named calendar.
func ServeICS(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		slug := strings.TrimSuffix(r.PathValue("slug"), ".ics")

		cal, err := store.GetCalendarBySlug(ctx, pool, slug)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		events, err := store.ListCalendarEvents(ctx, pool, cal.ID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.ics"`, slug))

		now := time.Now().UTC()
		var sb strings.Builder
		sb.WriteString("BEGIN:VCALENDAR\r\n")
		sb.WriteString("VERSION:2.0\r\n")
		sb.WriteString("PRODID:-//Commons//EN\r\n")
		sb.WriteString("CALSCALE:GREGORIAN\r\n")
		sb.WriteString("METHOD:PUBLISH\r\n")
		sb.WriteString(icsLine("X-WR-CALNAME", cal.Name))
		sb.WriteString(icsLine("X-WR-TIMEZONE", cal.Timezone))

		for _, e := range events {
			sb.WriteString("BEGIN:VEVENT\r\n")
			sb.WriteString(icsLine("UID", e.ID+"@commons"))
			sb.WriteString(icsLine("DTSTAMP", now.Format("20060102T150405Z")))
			if e.AllDay {
				sb.WriteString(icsLine("DTSTART;VALUE=DATE", e.StartTime.UTC().Format("20060102")))
				if e.EndTime != nil {
					sb.WriteString(icsLine("DTEND;VALUE=DATE", e.EndTime.UTC().Format("20060102")))
				}
			} else {
				sb.WriteString(icsLine("DTSTART", e.StartTime.UTC().Format("20060102T150405Z")))
				if e.EndTime != nil {
					sb.WriteString(icsLine("DTEND", e.EndTime.UTC().Format("20060102T150405Z")))
				}
			}
			sb.WriteString(icsLine("SUMMARY", icsEscape(e.Title)))
			if e.Description != nil && *e.Description != "" {
				sb.WriteString(icsLine("DESCRIPTION", icsEscape(*e.Description)))
			}
			if e.Location != nil && *e.Location != "" {
				sb.WriteString(icsLine("LOCATION", icsEscape(*e.Location)))
			}
			if e.URL != nil && *e.URL != "" {
				sb.WriteString(icsLine("URL", *e.URL))
			}
			sb.WriteString("END:VEVENT\r\n")
		}

		sb.WriteString("END:VCALENDAR\r\n")
		fmt.Fprint(w, sb.String())
	}
}

// FetchICSEvents fetches an ICS URL and returns all events as CalendarEvent values.
// Recurring events are expanded over a ±1/+2 year window using rrule-go, which
// correctly handles positional BYDAY rules (e.g. FREQ=MONTHLY;BYDAY=1WE).
func FetchICSEvents(url string) ([]store.CalendarEvent, error) {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ICS fetch returned HTTP %d", resp.StatusCode)
	}

	cal, err := ical.NewDecoder(resp.Body).Decode()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	windowStart := now.AddDate(-1, 0, 0)
	windowEnd := now.AddDate(2, 0, 0)

	var events []store.CalendarEvent
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

		dtStart, err := dtStartProp.DateTime(time.UTC)
		if err != nil {
			continue
		}
		var dtEnd time.Time
		if dtEndProp != nil {
			dtEnd, _ = dtEndProp.DateTime(time.UTC)
		}

		buildEvt := func(start, end time.Time) store.CalendarEvent {
			evt := store.CalendarEvent{Title: title, AllDay: allDay, StartTime: start}
			if !end.IsZero() {
				evt.EndTime = &end
			}
			if p := comp.Props.Get(ical.PropDescription); p != nil && p.Value != "" {
				s := p.Value
				evt.Description = &s
			}
			if p := comp.Props.Get(ical.PropLocation); p != nil && p.Value != "" {
				s := p.Value
				evt.Location = &s
			}
			if p := comp.Props.Get(ical.PropURL); p != nil && p.Value != "" {
				s := p.Value
				evt.URL = &s
			}
			return evt
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
				if exTime, err := exProp.DateTime(time.UTC); err == nil {
					set.ExDate(exTime)
				}
			}
			duration := dtEnd.Sub(dtStart)
			for _, occ := range set.Between(windowStart, windowEnd, true) {
				occEnd := occ.Add(duration)
				events = append(events, buildEvt(occ, occEnd))
			}
		} else {
			events = append(events, buildEvt(dtStart, dtEnd))
		}
	}
	return events, nil
}

// icsLine formats a single iCalendar property line with CRLF termination.
func icsLine(key, value string) string {
	return key + ":" + value + "\r\n"
}

// icsEscape escapes special characters in iCalendar text values.
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
