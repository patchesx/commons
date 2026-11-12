package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dayUTC(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

func TestBuildAndParseRRULERoundTrip(t *testing.T) {
	dtstart := dayUTC(2026, 11, 12, 15, 0) // a Thursday

	cases := []RecurrenceState{
		{Freq: "daily", Interval: 1, End: "never"},
		{Freq: "daily", Interval: 3, End: "count", Count: 10},
		{Freq: "weekly", Interval: 1, ByDay: []string{"MO", "TH"}, End: "never"},
		{Freq: "weekly", Interval: 2, ByDay: []string{"TU"}, End: "until", Until: "2027-03-01"},
		{Freq: "monthly", Interval: 1, MonthlyMode: "day", ByMonthDay: 15, End: "never"},
		{Freq: "monthly", Interval: 1, MonthlyMode: "nth", Nth: 2, NthWeekday: "TU", End: "never"},
		{Freq: "yearly", Interval: 1, End: "never"},
	}
	for _, st := range cases {
		rule, err := BuildRRULE(st, dtstart)
		require.NoError(t, err, "build %+v", st)
		require.NotEmpty(t, rule)

		got := ParseRRULE(rule, dtstart)
		assert.Equal(t, st.Freq, got.Freq, "freq round-trip for %s", rule)
		assert.Equal(t, st.Interval, got.Interval, "interval round-trip for %s", rule)
		assert.Equal(t, st.End, got.End, "end round-trip for %s", rule)
		switch st.Freq {
		case "weekly":
			assert.ElementsMatch(t, st.ByDay, got.ByDay, "byday round-trip for %s", rule)
		case "monthly":
			assert.Equal(t, st.MonthlyMode, got.MonthlyMode, "monthly mode round-trip for %s", rule)
			if st.MonthlyMode == "nth" {
				assert.Equal(t, st.Nth, got.Nth, "nth round-trip for %s", rule)
				assert.Equal(t, st.NthWeekday, got.NthWeekday, "nth weekday round-trip for %s", rule)
			} else {
				assert.Equal(t, st.ByMonthDay, got.ByMonthDay, "month day round-trip for %s", rule)
			}
		}
		if st.End == "count" {
			assert.Equal(t, st.Count, got.Count, "count round-trip for %s", rule)
		}
	}
}

func TestBuildRRULEValidation(t *testing.T) {
	dtstart := dayUTC(2026, 11, 12, 15, 0)
	_, err := BuildRRULE(RecurrenceState{Freq: "hourly"}, dtstart)
	assert.Error(t, err)
	_, err = BuildRRULE(RecurrenceState{Freq: "weekly", ByDay: []string{"XX"}}, dtstart)
	assert.Error(t, err)
	_, err = BuildRRULE(RecurrenceState{Freq: "daily", End: "until"}, dtstart)
	assert.Error(t, err, "until without a date must fail")
	_, err = BuildRRULE(RecurrenceState{Freq: "daily", End: "until", Until: "not-a-date"}, dtstart)
	assert.Error(t, err)
	_, err = BuildRRULE(RecurrenceState{Freq: "daily", End: "count", Count: 0}, dtstart)
	assert.Error(t, err)
	// none → empty rule, no error.
	rule, err := BuildRRULE(RecurrenceState{Freq: "none"}, dtstart)
	require.NoError(t, err)
	assert.Empty(t, rule)
}

func TestExpandEventsStandaloneWindow(t *testing.T) {
	in := CalendarEvent{ID: "e1", Title: "In", StartTime: dayUTC(2026, 12, 15, 10, 0)}
	before := CalendarEvent{ID: "e2", Title: "Before", StartTime: dayUTC(2026, 11, 1, 10, 0)}
	after := CalendarEvent{ID: "e3", Title: "After", StartTime: dayUTC(2027, 2, 1, 10, 0)}

	from, to := dayUTC(2026, 12, 1, 0, 0), dayUTC(2027, 1, 1, 0, 0)
	occs := ExpandEvents([]CalendarEvent{in, before, after}, nil, from, to, time.UTC)
	require.Len(t, occs, 1)
	assert.Equal(t, "In", occs[0].Title)
	assert.False(t, occs[0].IsRecurring)
	assert.Equal(t, occs[0].StartTime, occs[0].OccurrenceStart)
}

func TestExpandEventsWeeklySeries(t *testing.T) {
	// Thursdays at 15:00 UTC, starting Dec 3 2026 (a Thursday).
	series := CalendarEvent{ID: "s1", Title: "Standup", StartTime: dayUTC(2026, 12, 3, 15, 0), RRule: "FREQ=WEEKLY;BYDAY=TH"}
	from, to := dayUTC(2026, 12, 1, 0, 0), dayUTC(2027, 1, 1, 0, 0)

	occs := ExpandEvents([]CalendarEvent{series}, nil, from, to, time.UTC)
	require.Len(t, occs, 5, "Dec 3, 10, 17, 24, 31")
	for i, occ := range occs {
		assert.True(t, occ.IsRecurring)
		assert.Equal(t, time.Thursday, occ.StartTime.Weekday())
		expected := dayUTC(2026, 12, 3+i*7, 15, 0)
		assert.Equal(t, expected, occ.StartTime.UTC(), "occurrence %d", i)
		assert.Equal(t, expected, occ.OccurrenceStart.UTC(), "occurrence address %d", i)
	}
}

func TestExpandEventsCountAndUntil(t *testing.T) {
	from, to := dayUTC(2026, 12, 1, 0, 0), dayUTC(2027, 3, 1, 0, 0)

	counted := CalendarEvent{ID: "s1", StartTime: dayUTC(2026, 12, 1, 9, 0), RRule: "FREQ=DAILY;COUNT=3"}
	occs := ExpandEvents([]CalendarEvent{counted}, nil, from, to, time.UTC)
	assert.Len(t, occs, 3, "COUNT bounds the series regardless of window")

	until := CalendarEvent{ID: "s2", StartTime: dayUTC(2026, 12, 1, 9, 0), RRule: "FREQ=DAILY;UNTIL=20261205T000000Z"}
	occs = ExpandEvents([]CalendarEvent{until}, nil, from, to, time.UTC)
	assert.Len(t, occs, 4, "UNTIL is inclusive: Dec 1-4")
}

func TestExpandEventsDSTHoldsLocalTime(t *testing.T) {
	chi, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)

	// Weekly Tuesday 9:00 Chicago, first occurrence Nov 3 2026 (CST, UTC-6).
	// Nov 3 2026 is after the Nov 1 DST end, so let's start before it: Oct 27 (CDT, UTC-5).
	// The series spans the transition: Oct 27 CDT → Nov 3 CST.
	start := time.Date(2026, 10, 27, 9, 0, 0, 0, chi) // 14:00 UTC
	series := CalendarEvent{ID: "s1", Title: "Coffee", StartTime: start.UTC(), RRule: "FREQ=WEEKLY;BYDAY=TU"}

	from, to := dayUTC(2026, 10, 1, 0, 0), dayUTC(2026, 12, 1, 0, 0)
	occs := ExpandEvents([]CalendarEvent{series}, nil, from, to, chi)
	require.Len(t, occs, 5, "Oct 27, Nov 3, 10, 17, 24 — Dec 1 09:00 CST is 15:00 UTC, past the window end")

	for _, occ := range occs {
		local := occ.StartTime.In(chi)
		assert.Equal(t, 9, local.Hour(), "local hour must stay 9:00 across the DST change")
		assert.Equal(t, time.Tuesday, local.Weekday())
	}
	// Spot check the UTC instants across the transition.
	assert.Equal(t, "2026-10-27T14:00:00Z", occs[0].StartTime.UTC().Format(time.RFC3339), "CDT is UTC-5")
	assert.Equal(t, "2026-11-03T15:00:00Z", occs[1].StartTime.UTC().Format(time.RFC3339), "CST is UTC-6")
}

func TestExpandEventsOverrides(t *testing.T) {
	series := CalendarEvent{ID: "s1", Title: "Standup", StartTime: dayUTC(2026, 12, 3, 15, 0), RRule: "FREQ=WEEKLY;BYDAY=TH"}
	from, to := dayUTC(2026, 12, 1, 0, 0), dayUTC(2027, 1, 1, 0, 0)

	dec17 := dayUTC(2026, 12, 17, 15, 0)
	dec24 := dayUTC(2026, 12, 24, 15, 0)
	newTitle := "Standup — special"
	newStart := dayUTC(2026, 12, 17, 16, 0)
	overrides := map[string][]EventOverride{
		"s1": {
			{SeriesID: "s1", OccurrenceStart: dec17, Deleted: true},                                                  // skip
			{SeriesID: "s1", OccurrenceStart: dec24, Title: &newTitle, StartTime: &newStart, AllDay: ptrBool(false)}, // edit
		},
	}

	occs := ExpandEvents([]CalendarEvent{series}, overrides, from, to, time.UTC)
	require.Len(t, occs, 4, "5 occurrences minus 1 skipped")

	var edited *EventOccurrence
	for i := range occs {
		assert.NotEqual(t, dec17, occs[i].OccurrenceStart, "skipped occurrence must not appear")
		if occs[i].OccurrenceStart.Equal(dec24) {
			edited = &occs[i]
		}
	}
	require.NotNil(t, edited, "edited occurrence must be present")
	assert.Equal(t, "Standup — special", edited.Title)
	assert.Equal(t, newStart, edited.StartTime, "edited start takes effect")
	assert.True(t, edited.IsOverride)
	assert.True(t, edited.IsRecurring)
	assert.Equal(t, dec24, edited.OccurrenceStart, "addressing key stays the original slot")
}

func TestExpandEventsUnparseableRuleDegradesToStandalone(t *testing.T) {
	broken := CalendarEvent{ID: "s1", Title: "Broken", StartTime: dayUTC(2026, 12, 10, 9, 0), RRule: "FREQ=NOTAFREQ"}
	from, to := dayUTC(2026, 12, 1, 0, 0), dayUTC(2027, 1, 1, 0, 0)

	occs := ExpandEvents([]CalendarEvent{broken}, nil, from, to, time.UTC)
	require.Len(t, occs, 1, "unparseable rule degrades to a single occurrence instead of hiding the event")
	assert.Equal(t, "Broken", occs[0].Title)
}

func TestExpandEventsSeriesStartingBeforeWindow(t *testing.T) {
	// Template started long before the window; its future occurrences must appear.
	series := CalendarEvent{ID: "s1", Title: "Old series", StartTime: dayUTC(2020, 1, 2, 9, 0), RRule: "FREQ=WEEKLY;BYDAY=TH"}
	from, to := dayUTC(2026, 12, 1, 0, 0), dayUTC(2026, 12, 8, 0, 0)

	occs := ExpandEvents([]CalendarEvent{series}, nil, from, to, time.UTC)
	require.Len(t, occs, 1, "Dec 3 2026 is the only Thursday in the window")
	assert.Equal(t, dayUTC(2026, 12, 3, 9, 0), occs[0].StartTime.UTC())
}

func ptrBool(b bool) *bool { return &b }
