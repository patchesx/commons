package slack

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

// TestFetchCompositeEventsBoundedToWindow guards the App Home view size:
// ListCompositeOccurrences returns standalone stored events unclipped (the
// composite ICS export needs far-future events), so fetchCompositeEvents must
// apply its own 7-day upper bound. Without it, a member with years of synced
// feed events balloons the view past Slack's block limit and every
// views.publish fails with invalid_arguments.
func TestFetchCompositeEventsBoundedToWindow(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)

	member := &store.Calendar{Name: "Feed Member", Slug: "comp-feed-member", Timezone: "America/Chicago"}
	require.NoError(t, store.CreateCalendar(ctx, pool, member))
	comp := &store.Calendar{Name: "Comp", Slug: "comp-bounded", Timezone: "America/Chicago", Kind: store.CalendarKindComposite}
	require.NoError(t, store.CreateCalendar(ctx, pool, comp))
	require.NoError(t, store.AddCalendarMembers(ctx, pool, comp.ID, []string{member.ID}))

	now := time.Now()
	events := []struct {
		title  string
		offset time.Duration
	}{
		{"Ongoing Multi-Day", -24 * time.Hour}, // started yesterday, ends tomorrow → stays
		{"Tomorrow", 24 * time.Hour},           // in window
		{"Next Week", 8 * 24 * time.Hour},      // just past the 7-day window
		{"Far Future", 400 * 24 * time.Hour},   // years of feed data → must not appear
		{"Past Ended", -48 * time.Hour},        // already over → dropped
	}
	endDelta := 48 * time.Hour
	for _, e := range events {
		start := now.Add(e.offset)
		evt := &store.CalendarEvent{CalendarID: member.ID, Title: e.title, StartTime: start}
		if e.title == "Ongoing Multi-Day" || e.title == "Past Ended" {
			end := now.Add(endDelta)
			if e.title == "Past Ended" {
				end = now.Add(-time.Hour)
			}
			evt.EndTime = &end
		}
		require.NoError(t, store.CreateCalendarEvent(ctx, pool, evt))
	}

	got, err := fetchCompositeEvents(ctx, pool, *comp, loc)
	require.NoError(t, err)

	var titles []string
	for _, e := range got {
		titles = append(titles, e.Title)
	}
	assert.ElementsMatch(t, []string{"Ongoing Multi-Day", "Tomorrow"}, titles,
		"composite App Home must show only in-window upcoming/ongoing events — far-future and ended events must be excluded")
}
