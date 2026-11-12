// Package ical parses iCalendar (ICS) feeds into event templates and expands
// recurring occurrences. It unifies the parsing logic previously duplicated in
// api/ics.go and integrations/slack/calendar.go.
//
// Datetime values without a TZID parameter (floating times) and DATE values
// (all-day events) are interpreted in the caller-supplied reference location —
// the owning calendar's timezone — so all-day events land on the right local
// day.
package ical

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Event is a parsed VEVENT template: fields exactly as the feed declared them,
// recurrence rules unexpanded. Rule is the raw RRULE value; empty for
// non-recurring events.
type Event struct {
	UID         string
	Title       string
	Description *string
	Location    *string
	URL         *string
	StartTime   time.Time
	EndTime     *time.Time
	AllDay      bool
	Rule        string

	exDates []time.Time
}

// Fetch downloads and parses an ICS feed. Honors ctx for cancellation and
// timeout.
func Fetch(ctx context.Context, url string, refLoc *time.Location) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ICS fetch returned HTTP %d", resp.StatusCode)
	}
	return Parse(resp.Body, refLoc)
}

// Parse decodes ICS content into event templates. VEVENTs without SUMMARY or
// DTSTART are skipped (they cannot be displayed). refLoc interprets floating
// times and DATE values.
func Parse(r io.Reader, refLoc *time.Location) ([]Event, error) {
	if refLoc == nil {
		refLoc = time.UTC
	}
	cal, err := ical.NewDecoder(r).Decode()
	if err != nil {
		return nil, err
	}

	var events []Event
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		ev, ok := parseEvent(comp, refLoc)
		if !ok {
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}

func parseEvent(comp *ical.Component, refLoc *time.Location) (Event, bool) {
	summaryProp := comp.Props.Get(ical.PropSummary)
	if summaryProp == nil {
		return Event{}, false
	}
	dtStartProp := comp.Props.Get(ical.PropDateTimeStart)
	if dtStartProp == nil {
		return Event{}, false
	}

	allDay := !strings.Contains(dtStartProp.Value, "T")
	dtStart, err := dtStartProp.DateTime(refLoc)
	if err != nil {
		return Event{}, false
	}

	ev := Event{
		UID:       propValue(comp, ical.PropUID),
		Title:     summaryProp.Value,
		StartTime: dtStart,
		AllDay:    allDay,
		Rule:      propValue(comp, ical.PropRecurrenceRule),
	}
	if p := comp.Props.Get(ical.PropDescription); p != nil && p.Value != "" {
		s := p.Value
		ev.Description = &s
	}
	if p := comp.Props.Get(ical.PropLocation); p != nil && p.Value != "" {
		s := p.Value
		ev.Location = &s
	}
	if p := comp.Props.Get(ical.PropURL); p != nil && p.Value != "" {
		s := p.Value
		ev.URL = &s
	}
	if dtEndProp := comp.Props.Get(ical.PropDateTimeEnd); dtEndProp != nil {
		if dtEnd, err := dtEndProp.DateTime(refLoc); err == nil && !dtEnd.IsZero() {
			// Copy to avoid aliasing the loop variable.
			end := dtEnd
			ev.EndTime = &end
		}
	}
	for _, exProp := range comp.Props[ical.PropExceptionDates] {
		if exTime, err := exProp.DateTime(refLoc); err == nil {
			ev.exDates = append(ev.exDates, exTime)
		}
	}
	return ev, true
}

func propValue(comp *ical.Component, prop string) string {
	if p := comp.Props.Get(prop); p != nil {
		return p.Value
	}
	return ""
}

// DefaultEnd supplies the implicit end time for events without DTEND:
// end of the start day for all-day events (i.e. the next midnight), one hour
// after start otherwise.
func (ev Event) DefaultEnd() time.Time {
	if ev.AllDay {
		return ev.StartTime.AddDate(0, 0, 1)
	}
	return ev.StartTime.Add(time.Hour)
}

// EffectiveEnd returns the event's end: DTEND when present, otherwise the
// DefaultEnd.
func (ev Event) EffectiveEnd() time.Time {
	if ev.EndTime != nil {
		return *ev.EndTime
	}
	return ev.DefaultEnd()
}

// Expand returns the start instants of ev's occurrences within [from, to).
// Non-recurring events yield their single start when it falls in the window.
// Recurring events expand in loc — so local start times hold across DST
// changes — with EXDATE occurrences dropped. An unparseable rule degrades to
// the single start rather than hiding the event.
func (ev Event) Expand(from, to time.Time, loc *time.Location) []time.Time {
	if loc == nil {
		loc = time.UTC
	}
	if ev.Rule == "" {
		if !ev.StartTime.Before(from) && ev.StartTime.Before(to) {
			return []time.Time{ev.StartTime}
		}
		return nil
	}
	opt, err := rrule.StrToROption(ev.Rule)
	if err != nil {
		if !ev.StartTime.Before(from) && ev.StartTime.Before(to) {
			return []time.Time{ev.StartTime}
		}
		return nil
	}
	opt.Dtstart = ev.StartTime.In(loc)
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil
	}
	set := &rrule.Set{}
	set.RRule(r)
	for _, ex := range ev.exDates {
		set.ExDate(ex)
	}
	occurrences := set.Between(from.In(loc), to.In(loc), true)
	out := make([]time.Time, 0, len(occurrences))
	for _, occ := range occurrences {
		out = append(out, occ.UTC())
	}
	return out
}

// Occurrence is one materialized occurrence of a feed event: the event's
// template fields applied to a concrete start instant (duration carried from
// the template).
type Occurrence struct {
	UID         string
	Title       string
	Description *string
	Location    *string
	URL         *string
	StartTime   time.Time
	EndTime     *time.Time
	AllDay      bool
}

// ExpandAll expands every event within [from, to) and returns the materialized
// occurrences, sorted by start time.
func ExpandAll(events []Event, from, to time.Time, loc *time.Location) []Occurrence {
	var out []Occurrence
	for _, ev := range events {
		duration := ev.EffectiveEnd().Sub(ev.StartTime)
		for _, start := range ev.Expand(from, to, loc) {
			occ := Occurrence{
				UID:         ev.UID,
				Title:       ev.Title,
				Description: ev.Description,
				Location:    ev.Location,
				URL:         ev.URL,
				StartTime:   start,
				AllDay:      ev.AllDay,
			}
			if ev.EndTime != nil {
				end := start.Add(duration)
				occ.EndTime = &end
			}
			out = append(out, occ)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out
}
