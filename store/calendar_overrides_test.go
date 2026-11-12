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

// setupSeriesCal creates a calendar in America/Chicago and a weekly Thursday
// 9:00 series on it starting at start.
func setupSeriesCal(t *testing.T, start time.Time, rrule string) (*pgxpool.Pool, *CalendarEvent) {
	t.Helper()
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	cal := &Calendar{Name: "Recurrence Cal", Slug: "recurrence-cal", Timezone: "America/Chicago"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))
	chi, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	ev := &CalendarEvent{
		CalendarID: cal.ID,
		Title:      "Weekly",
		StartTime:  start.In(chi).UTC(),
		RRule:      rrule,
	}
	require.NoError(t, CreateCalendarEvent(ctx, pool, ev))
	return pool, ev
}

// occAt returns the occurrence at the given series slot, if present.
func occAt(occs []EventOccurrence, slot time.Time) *EventOccurrence {
	for i := range occs {
		if occs[i].OccurrenceStart.Equal(slot) {
			return &occs[i]
		}
	}
	return nil
}

func TestListCalendarOccurrencesWindowed(t *testing.T) {
	ctx := context.Background()
	// Weekly Thursday 9:00 Chicago starting 2026-10-01, 12 occurrences
	// (…, Nov 26, Dec 3, Dec 10).
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, mustLoc(t, "America/Chicago"))
	pool, ev := setupSeriesCal(t, start, "FREQ=WEEKLY;BYDAY=TH;COUNT=12")

	// December window up to Dec 15: Dec 3 and Dec 10.
	chi := mustLoc(t, "America/Chicago")
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, chi)
	to := time.Date(2026, 12, 15, 0, 0, 0, 0, chi)
	occs, err := ListCalendarOccurrences(ctx, pool, ev.CalendarID, from, to)
	require.NoError(t, err)
	require.Len(t, occs, 2)
	assert.Equal(t, "2026-12-03T15:00:00Z", occs[0].StartTime.UTC().Format(time.RFC3339))
	assert.Equal(t, "2026-12-10T15:00:00Z", occs[1].StartTime.UTC().Format(time.RFC3339))
	for _, o := range occs {
		assert.True(t, o.IsRecurring)
		assert.Equal(t, ev.ID, o.ID)
	}
}

func mustLoc(t *testing.T, tz string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	require.NoError(t, err)
	return loc
}

func TestApplyEventEditScopeOne(t *testing.T) {
	ctx := context.Background()
	chi := mustLoc(t, "America/Chicago")
	start := time.Date(2026, 12, 3, 9, 0, 0, 0, chi)
	pool, ev := setupSeriesCal(t, start, "FREQ=WEEKLY;BYDAY=TH;COUNT=6")

	// Edit the 2nd occurrence (Dec 10): new title and a moved time.
	slot := time.Date(2026, 12, 10, 9, 0, 0, 0, chi).UTC()
	moved := time.Date(2026, 12, 10, 10, 30, 0, 0, chi).UTC()
	edited := &CalendarEvent{
		Title:     "Weekly (moved)",
		StartTime: moved,
		AllDay:    false,
	}
	require.NoError(t, ApplyEventEdit(ctx, pool, ev.ID, slot, ScopeOne, edited))

	from := time.Date(2026, 12, 1, 0, 0, 0, 0, chi)
	to := time.Date(2026, 12, 20, 0, 0, 0, 0, chi)
	occs, err := ListCalendarOccurrences(ctx, pool, ev.CalendarID, from, to)
	require.NoError(t, err)
	require.Len(t, occs, 3)

	second := occAt(occs, slot)
	require.NotNil(t, second)
	assert.Equal(t, "Weekly (moved)", second.Title)
	assert.Equal(t, moved.UTC(), second.StartTime.UTC(), "edited start takes effect (instant compare — pgx may return any location)")
	assert.True(t, second.IsOverride)

	// Others untouched.
	assert.Equal(t, "Weekly", occs[0].Title)
	assert.Equal(t, "Weekly", occs[2].Title)

	// Re-editing the same occurrence updates the override in place.
	edited.Title = "Weekly (moved again)"
	require.NoError(t, ApplyEventEdit(ctx, pool, ev.ID, slot, ScopeOne, edited))
	ovs, err := ListEventOverridesByIDs(ctx, pool, []string{ev.ID})
	require.NoError(t, err)
	assert.Len(t, ovs[ev.ID], 1, "upsert, not append")
}

func TestApplyEventEditScopeAllClearsOverrides(t *testing.T) {
	ctx := context.Background()
	chi := mustLoc(t, "America/Chicago")
	start := time.Date(2026, 12, 3, 9, 0, 0, 0, chi)
	pool, ev := setupSeriesCal(t, start, "FREQ=WEEKLY;BYDAY=TH;COUNT=6")

	slot := time.Date(2026, 12, 10, 9, 0, 0, 0, chi).UTC()
	require.NoError(t, ApplyEventEdit(ctx, pool, ev.ID, slot, ScopeOne, &CalendarEvent{Title: "Edited", StartTime: slot}))

	// Edit-all with a new rule.
	require.NoError(t, ApplyEventEdit(ctx, pool, ev.ID, slot, ScopeAll, &CalendarEvent{
		Title:     "Renamed",
		StartTime: ev.StartTime,
		RRule:     "FREQ=WEEKLY;BYDAY=TH;COUNT=3",
	}))

	ovs, err := ListEventOverridesByIDs(ctx, pool, []string{ev.ID})
	require.NoError(t, err)
	assert.Empty(t, ovs[ev.ID], "edit-all clears overrides")

	got, err := GetCalendarEventByID(ctx, pool, ev.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Title)
	assert.Equal(t, "FREQ=WEEKLY;BYDAY=TH;COUNT=3", got.RRule)
}

func TestApplyEventEditScopeFollowingSplits(t *testing.T) {
	ctx := context.Background()
	chi := mustLoc(t, "America/Chicago")
	// 6 weekly Thursdays from Dec 3.
	start := time.Date(2026, 12, 3, 9, 0, 0, 0, chi)
	pool, ev := setupSeriesCal(t, start, "FREQ=WEEKLY;BYDAY=TH;COUNT=6")

	// Pre-existing edit on the 5th occurrence (Dec 31), new title only.
	dec31 := time.Date(2026, 12, 31, 9, 0, 0, 0, chi).UTC()
	require.NoError(t, ApplyEventEdit(ctx, pool, ev.ID, dec31, ScopeOne, &CalendarEvent{Title: "New Year eve special", StartTime: dec31}))

	// Split at the 3rd occurrence (Dec 17), moving it to 10:00.
	splitSlot := time.Date(2026, 12, 17, 9, 0, 0, 0, chi).UTC()
	newStart := time.Date(2026, 12, 17, 10, 0, 0, 0, chi).UTC()
	require.NoError(t, ApplyEventEdit(ctx, pool, ev.ID, splitSlot, ScopeFollowing, &CalendarEvent{
		Title:     "Weekly (new time)",
		StartTime: newStart,
	}))

	// Old series: truncated before Dec 17, still holds Dec 3 + Dec 10.
	old, err := GetCalendarEventByID(ctx, pool, ev.ID)
	require.NoError(t, err)
	require.NotEmpty(t, old.RRule)
	assert.Contains(t, old.RRule, "UNTIL", "old series rule gains an UNTIL bound")
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, chi)
	to := time.Date(2027, 1, 15, 0, 0, 0, 0, chi)
	oldOccs, err := ListCalendarOccurrences(ctx, pool, ev.CalendarID, from, to)
	require.NoError(t, err)

	// Total across both series in the window: Dec 3, 10 (old, truncated);
	// Dec 17, 24, 31, Jan 7 (new series with carried COUNT=4).
	require.Len(t, oldOccs, 6)

	// The split occurrence is the new series' first occurrence — addressed by the
	// new series' dtstart slot, not the old series' slot it split from.
	var dec17 *EventOccurrence
	for i := range oldOccs {
		if oldOccs[i].Title == "Weekly (new time)" && oldOccs[i].OccurrenceStart.Equal(newStart) {
			dec17 = &oldOccs[i]
		}
	}
	require.NotNil(t, dec17, "the split occurrence belongs to the new series")
	assert.Equal(t, newStart.UTC(), dec17.StartTime.UTC())

	// The Dec 31 override moved to the new series and re-addressed to 10:00.
	dec31MovedSlot := time.Date(2026, 12, 31, 10, 0, 0, 0, chi).UTC()
	dec31Occ := occAt(oldOccs, dec31MovedSlot)
	require.NotNil(t, dec31Occ, "moved override must address the new series' slot")
	assert.Equal(t, "New Year eve special", dec31Occ.Title)

	// New series carries remaining count: 6 - 2 elapsed = 4.
	newEv := dec17.ID
	assert.NotEqual(t, ev.ID, newEv, "split created a new series row")
	gotNew, err := GetCalendarEventByID(ctx, pool, newEv)
	require.NoError(t, err)
	assert.Contains(t, gotNew.RRule, "COUNT=4", "remaining occurrences carried over")
}

func TestDeleteEventWithScopes(t *testing.T) {
	ctx := context.Background()
	chi := mustLoc(t, "America/Chicago")
	start := time.Date(2026, 12, 3, 9, 0, 0, 0, chi)
	pool, ev := setupSeriesCal(t, start, "FREQ=WEEKLY;BYDAY=TH;COUNT=6")

	from := time.Date(2026, 12, 1, 0, 0, 0, 0, chi)
	to := time.Date(2027, 1, 10, 0, 0, 0, 0, chi)

	// ScopeOne skips a single occurrence.
	dec10 := time.Date(2026, 12, 10, 9, 0, 0, 0, chi).UTC()
	require.NoError(t, DeleteEventWithScope(ctx, pool, ev.ID, dec10, ScopeOne))
	occs, err := ListCalendarOccurrences(ctx, pool, ev.CalendarID, from, to)
	require.NoError(t, err)
	assert.Len(t, occs, 5)
	assert.Nil(t, occAt(occs, dec10))

	// ScopeFollowing truncates from Dec 24 onward.
	dec24 := time.Date(2026, 12, 24, 9, 0, 0, 0, chi).UTC()
	require.NoError(t, DeleteEventWithScope(ctx, pool, ev.ID, dec24, ScopeFollowing))
	occs, err = ListCalendarOccurrences(ctx, pool, ev.CalendarID, from, to)
	require.NoError(t, err)
	assert.Len(t, occs, 2, "Dec 3 + unskipped Dec 17 remain; Dec 10 skipped, Dec 24+ truncated")

	// ScopeAll removes the template and its overrides.
	require.NoError(t, DeleteEventWithScope(ctx, pool, ev.ID, dec10, ScopeAll))
	_, err = GetCalendarEventByID(ctx, pool, ev.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	ovs, err := ListEventOverridesByIDs(ctx, pool, []string{ev.ID})
	require.NoError(t, err)
	assert.Empty(t, ovs[ev.ID], "overrides cascade with the series")
}

func TestListUpcomingCalendarOccurrencesAcrossCalendars(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	calA := &Calendar{Name: "Cal A", Slug: "upcoming-a", Timezone: "America/Chicago"}
	require.NoError(t, CreateCalendar(ctx, pool, calA))
	calB := &Calendar{Name: "Cal B", Slug: "upcoming-b", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, calB))

	base := time.Now().UTC()
	// Series on A starting in the past — future occurrences must surface.
	series := &CalendarEvent{
		CalendarID: calA.ID, Title: "Series",
		StartTime: base.AddDate(0, 0, -14), RRule: "FREQ=DAILY;COUNT=40",
	}
	require.NoError(t, CreateCalendarEvent(ctx, pool, series))
	// Standalone on B within the window.
	standalone := &CalendarEvent{CalendarID: calB.ID, Title: "Standalone", StartTime: base.Add(48 * time.Hour)}
	require.NoError(t, CreateCalendarEvent(ctx, pool, standalone))
	// Standalone outside the window — excluded.
	far := &CalendarEvent{CalendarID: calB.ID, Title: "Far", StartTime: base.Add(30 * 24 * time.Hour)}
	require.NoError(t, CreateCalendarEvent(ctx, pool, far))

	// Non-boundary-aligned window (±1h padding) so the count is deterministic
	// regardless of rrule boundary-inclusion semantics and DST spacing.
	occs, err := ListUpcomingCalendarOccurrences(ctx, pool, base.Add(-25*time.Hour), base.Add(7*24*time.Hour+time.Hour))
	require.NoError(t, err)
	var seriesCount, standaloneCount int
	for _, o := range occs {
		switch o.Title {
		case "Series":
			seriesCount++
		case "Standalone":
			standaloneCount++
		case "Far":
			t.Fatal("out-of-window event must not appear")
		}
	}
	assert.Equal(t, 9, seriesCount, "daily series expands into the window from a past dtstart: base-24h through base+7d")
	assert.Equal(t, 1, standaloneCount)
}
