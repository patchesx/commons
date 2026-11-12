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

func setupCompositeFixture(t *testing.T) (pool *pgxpool.Pool, comp *Calendar, a *Calendar, b *Calendar) {
	t.Helper()
	ctx := context.Background()
	p := testhelpers.SetupTestDB(t)

	a = &Calendar{Name: "Member A", Slug: "member-a", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, p, a))
	b = &Calendar{Name: "Member B", Slug: "member-b", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, p, b))
	comp = &Calendar{Name: "All Events", Slug: "all-events", Timezone: "UTC", Kind: CalendarKindComposite}
	require.NoError(t, CreateCalendar(ctx, p, comp))
	require.NoError(t, AddCalendarMembers(ctx, p, comp.ID, []string{a.ID, b.ID}))
	return p, comp, a, b
}

func TestAddAndListCalendarMembers(t *testing.T) {
	ctx := context.Background()
	pool, comp, a, b := setupCompositeFixture(t)

	members, err := ListCalendarMembers(ctx, pool, comp.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, a.ID, members[0].ID)
	assert.Equal(t, b.ID, members[1].ID)

	// Idempotent re-add.
	require.NoError(t, AddCalendarMembers(ctx, pool, comp.ID, []string{a.ID}))
	members, err = ListCalendarMembers(ctx, pool, comp.ID)
	require.NoError(t, err)
	assert.Len(t, members, 2, "re-adding an existing member should be a no-op")
}

func TestReplaceCalendarMembers(t *testing.T) {
	ctx := context.Background()
	pool, comp, a, _ := setupCompositeFixture(t)

	c := &Calendar{Name: "Member C", Slug: "member-c", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, c))

	require.NoError(t, ReplaceCalendarMembers(ctx, pool, comp.ID, []string{a.ID, c.ID}))
	members, err := ListCalendarMembers(ctx, pool, comp.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, a.ID, members[0].ID)
	assert.Equal(t, c.ID, members[1].ID, "b should have been removed, c added")

	// Clear membership entirely.
	require.NoError(t, ReplaceCalendarMembers(ctx, pool, comp.ID, nil))
	members, err = ListCalendarMembers(ctx, pool, comp.ID)
	require.NoError(t, err)
	assert.Empty(t, members)
}

func TestAddCalendarMembersValidation(t *testing.T) {
	ctx := context.Background()
	pool, comp, _, _ := setupCompositeFixture(t)

	// Self-membership.
	err := AddCalendarMembers(ctx, pool, comp.ID, []string{comp.ID})
	assert.ErrorIs(t, err, ErrInvalidMember)

	// Personal calendars cannot be members.
	personal := &Calendar{Name: "My Calendar", Timezone: "UTC", Kind: CalendarKindPersonal}
	require.NoError(t, CreateCalendar(ctx, pool, personal))
	err = AddCalendarMembers(ctx, pool, comp.ID, []string{personal.ID})
	assert.ErrorIs(t, err, ErrInvalidMember)

	// Standard calendars cannot hold members.
	std := &Calendar{Name: "Plain", Slug: "plain", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, std))
	err = AddCalendarMembers(ctx, pool, std.ID, []string{comp.ID})
	assert.ErrorIs(t, err, ErrNotComposite)

	// Unknown member ID.
	err = AddCalendarMembers(ctx, pool, comp.ID, []string{"00000000-0000-0000-0000-000000000000"})
	assert.ErrorIs(t, err, ErrInvalidMember)
}

func TestWouldCreateCycle(t *testing.T) {
	ctx := context.Background()
	pool, outer, _, _ := setupCompositeFixture(t)

	// A composite that is itself a member of outer.
	inner := &Calendar{Name: "Inner Comp", Slug: "inner-comp", Timezone: "UTC", Kind: CalendarKindComposite}
	require.NoError(t, CreateCalendar(ctx, pool, inner))
	require.NoError(t, AddCalendarMembers(ctx, pool, outer.ID, []string{inner.ID}))

	// inner is a member of outer; adding outer to inner would close the cycle.
	cycles, err := WouldCreateCycle(ctx, pool, inner.ID, outer.ID)
	require.NoError(t, err)
	assert.True(t, cycles, "adding a composite to its own member should be detected as a cycle")

	// Nested case: outer2 contains outer (which contains inner). inner adding outer2 cycles.
	outer2 := &Calendar{Name: "Outer Two", Slug: "outer-two", Timezone: "UTC", Kind: CalendarKindComposite}
	require.NoError(t, CreateCalendar(ctx, pool, outer2))
	require.NoError(t, AddCalendarMembers(ctx, pool, outer2.ID, []string{outer.ID}))
	cycles, err = WouldCreateCycle(ctx, pool, inner.ID, outer2.ID)
	require.NoError(t, err)
	assert.True(t, cycles, "cycle detection must be transitive")

	// Unrelated calendar: no cycle.
	solo := &Calendar{Name: "Solo", Slug: "solo", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, solo))
	cycles, err = WouldCreateCycle(ctx, pool, inner.ID, solo.ID)
	require.NoError(t, err)
	assert.False(t, cycles)

	// The guarded add surfaces ErrCycleDetected.
	err = AddCalendarMembers(ctx, pool, inner.ID, []string{outer.ID})
	assert.ErrorIs(t, err, ErrCycleDetected)
}

func TestListCompositeEventsMergesAndDedupes(t *testing.T) {
	ctx := context.Background()
	pool, comp, a, b := setupCompositeFixture(t)

	base := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour)
	for _, evt := range []CalendarEvent{
		{Title: "Shared Event", StartTime: base}, // identical in both members
		{Title: "Only In A", StartTime: base.Add(1 * time.Hour)},
		{Title: "Late Event", StartTime: base.Add(3 * time.Hour)},
	} {
		evt.CalendarID = a.ID
		require.NoError(t, CreateCalendarEvent(ctx, pool, &evt))
	}
	for _, evt := range []CalendarEvent{
		{Title: "Shared Event", StartTime: base}, // duplicate of A's
		{Title: "Only In B", StartTime: base.Add(2 * time.Hour)},
	} {
		evt.CalendarID = b.ID
		require.NoError(t, CreateCalendarEvent(ctx, pool, &evt))
	}

	got, err := ListCompositeEvents(ctx, pool, comp.ID)
	require.NoError(t, err)
	require.Len(t, got, 4, "shared event should appear once")
	assert.Equal(t, "Shared Event", got[0].Title)
	assert.Equal(t, "Only In A", got[1].Title)
	assert.Equal(t, "Only In B", got[2].Title)
	assert.Equal(t, "Late Event", got[3].Title, "events should be sorted by start time")
}

func TestListCompositeEventsNested(t *testing.T) {
	ctx := context.Background()
	pool, _, _, b := setupCompositeFixture(t)

	// Reuse fixture's composite as a nested member of a new outer composite.
	outer := &Calendar{Name: "Outer", Slug: "outer-nested", Timezone: "UTC", Kind: CalendarKindComposite}
	require.NoError(t, CreateCalendar(ctx, pool, outer))
	nested, err := GetCalendarBySlug(ctx, pool, "all-events")
	require.NoError(t, err)
	require.NoError(t, AddCalendarMembers(ctx, pool, outer.ID, []string{nested.ID}))

	evt := CalendarEvent{Title: "Deep Event", StartTime: time.Now().Add(48 * time.Hour)}
	evt.CalendarID = b.ID
	require.NoError(t, CreateCalendarEvent(ctx, pool, &evt))

	got, err := ListCompositeEvents(ctx, pool, outer.ID)
	require.NoError(t, err)
	assert.Len(t, got, 1, "events of a nested member's members should surface")
}

func TestListCompositeEventsIncludesSyncedFeedMember(t *testing.T) {
	ctx := context.Background()
	pool, comp, a, _ := setupCompositeFixture(t)

	// Feed members read their synced stored rows — a feed calendar with rows
	// contributes them to the composite.
	feed := &Calendar{Name: "Feed Cal", Slug: "feed-cal", Timezone: "UTC"}
	icalURL := "https://example.com/feed.ics"
	feed.IcalURL = &icalURL
	require.NoError(t, CreateCalendar(ctx, pool, feed))
	require.NoError(t, AddCalendarMembers(ctx, pool, comp.ID, []string{feed.ID}))
	_, err := ReconcileFeedEvents(ctx, pool, feed.ID, []FeedOccurrence{
		{UID: "feed-1", Title: "Synced Feed Event", StartTime: time.Now().Add(48 * time.Hour)},
	})
	require.NoError(t, err)

	evt := CalendarEvent{Title: "Stored Event", StartTime: time.Now().Add(48 * time.Hour)}
	evt.CalendarID = a.ID
	require.NoError(t, CreateCalendarEvent(ctx, pool, &evt))

	got, err := ListCompositeEvents(ctx, pool, comp.ID)
	require.NoError(t, err)
	assert.Len(t, got, 2, "synced feed member rows merge alongside manual member rows")
	titles := []string{got[0].Title, got[1].Title}
	assert.Contains(t, titles, "Stored Event")
	assert.Contains(t, titles, "Synced Feed Event")
}
