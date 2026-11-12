package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLoc(t *testing.T, tz string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	require.NoError(t, err)
	return loc
}

const sampleICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:event-1@test
DTSTAMP:20260101T000000Z
DTSTART:20260315T140000Z
DTEND:20260315T150000Z
SUMMARY:Simple Event
DESCRIPTION:With details
LOCATION:Main Hall
URL:https://example.com/event
END:VEVENT
BEGIN:VEVENT
UID:allday@test
DTSTAMP:20260101T000000Z
DTSTART;VALUE=DATE:20260401
DTEND;VALUE=DATE:20260402
SUMMARY:All Day Thing
END:VEVENT
BEGIN:VEVENT
UID:weekly@test
DTSTAMP:20260101T000000Z
DTSTART:20260305T090000Z
DTEND:20260305T093000Z
SUMMARY:Weekly Standup
RRULE:FREQ=WEEKLY;BYDAY=TH;COUNT=4
EXDATE:20260319T090000Z
END:VEVENT
BEGIN:VEVENT
DTSTAMP:20260101T000000Z
DTSTART:20260310T120000Z
SUMMARY:No UID No End
END:VEVENT
BEGIN:VEVENT
UID:nostart@test
DTSTAMP:20260101T000000Z
SUMMARY:Missing DTSTART — skipped
END:VEVENT
END:VCALENDAR
`

func TestParseExtractsFields(t *testing.T) {
	events, err := Parse(strings.NewReader(sampleICS), time.UTC)
	require.NoError(t, err)
	require.Len(t, events, 4, "VEVENT without DTSTART is skipped")

	simple := events[0]
	assert.Equal(t, "event-1@test", simple.UID)
	assert.Equal(t, "Simple Event", simple.Title)
	require.NotNil(t, simple.Description)
	assert.Equal(t, "With details", *simple.Description)
	require.NotNil(t, simple.Location)
	assert.Equal(t, "Main Hall", *simple.Location)
	require.NotNil(t, simple.URL)
	assert.Equal(t, "https://example.com/event", *simple.URL)
	assert.False(t, simple.AllDay)
	assert.Empty(t, simple.Rule)
	assert.Equal(t, "2026-03-15T14:00:00Z", simple.StartTime.UTC().Format(time.RFC3339))

	allDay := events[1]
	assert.True(t, allDay.AllDay)
	require.NotNil(t, allDay.EndTime)

	noUID := events[3]
	assert.Empty(t, noUID.UID)
	assert.Nil(t, noUID.EndTime)
}

func TestParseAllDayUsesReferenceLocation(t *testing.T) {
	chi := mustLoc(t, "America/Chicago")

	events, err := Parse(strings.NewReader(sampleICS), chi)
	require.NoError(t, err)
	allDay := events[1]

	// DATE values resolve to midnight in the reference location: April 1 in
	// Chicago, not April 1 UTC (which would render as March 31 evening local).
	start := allDay.StartTime.In(chi)
	assert.Equal(t, "2026-04-01", start.Format("2006-01-02"))
	assert.Equal(t, 0, start.Hour())
}

func TestExpandRecurringWithExdate(t *testing.T) {
	events, err := Parse(strings.NewReader(sampleICS), time.UTC)
	require.NoError(t, err)
	weekly := events[2]
	assert.Equal(t, "FREQ=WEEKLY;BYDAY=TH;COUNT=4", weekly.Rule)

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	occurrences := weekly.Expand(from, to, time.UTC)
	// COUNT=4 (Mar 5, 12, 19, 26) minus the Mar 19 EXDATE.
	require.Len(t, occurrences, 3)
	assert.Equal(t, "2026-03-05", occurrences[0].UTC().Format("2006-01-02"))
	assert.Equal(t, "2026-03-12", occurrences[1].UTC().Format("2006-01-02"))
	assert.Equal(t, "2026-03-26", occurrences[2].UTC().Format("2006-01-02"))
}

func TestExpandStandaloneWindow(t *testing.T) {
	events, err := Parse(strings.NewReader(sampleICS), time.UTC)
	require.NoError(t, err)
	simple := events[0]

	in := simple.Expand(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	assert.Len(t, in, 1)
	out := simple.Expand(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	assert.Empty(t, out)
}

func TestExpandDSTHoldsLocalTime(t *testing.T) {
	chi := mustLoc(t, "America/Chicago")
	// Tuesdays 9:00 Chicago, starting Oct 27 2026 (CDT) — spans the Nov 1 DST end.
	ev := Event{
		UID:       "dst@test",
		Title:     "Coffee",
		StartTime: time.Date(2026, 10, 27, 9, 0, 0, 0, chi),
		Rule:      "FREQ=WEEKLY;BYDAY=TU",
	}
	occurrences := ev.Expand(time.Date(2026, 10, 1, 0, 0, 0, 0, chi), time.Date(2026, 11, 10, 0, 0, 0, 0, chi), chi)
	// Window ends Nov 10 00:00 local — the Nov 10 09:00 occurrence is outside.
	require.Len(t, occurrences, 2, "Oct 27 + Nov 3")
	for _, occ := range occurrences {
		local := occ.In(chi)
		assert.Equal(t, 9, local.Hour(), "local hour must stay 9:00 across the DST change")
	}
}

func TestDefaultEnd(t *testing.T) {
	allDay := Event{AllDay: true, StartTime: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}
	assert.Equal(t, time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC), allDay.DefaultEnd())

	timed := Event{StartTime: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)}
	assert.Equal(t, time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC), timed.DefaultEnd())
}

func TestExpandAllCarriesDurationAndSorts(t *testing.T) {
	events, err := Parse(strings.NewReader(sampleICS), time.UTC)
	require.NoError(t, err)

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	occs := ExpandAll(events, from, to, time.UTC)

	// simple (1) + all-day (1, no end from feed → no EndTime on occurrences without DTEND...
	// all-day HAS DTEND here) + weekly (3) + no-uid (1) = 6.
	require.Len(t, occs, 6)

	// Sorted ascending: Mar 5 weekly first.
	assert.Equal(t, "Weekly Standup", occs[0].Title)
	// Duration carried from the template: standup is 30 minutes.
	require.NotNil(t, occs[0].EndTime)
	assert.Equal(t, occs[0].StartTime.Add(30*time.Minute), occs[0].EndTime.UTC())
	// UID preserved on occurrences.
	assert.Equal(t, "weekly@test", occs[0].UID)
}
