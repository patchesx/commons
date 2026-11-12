package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestPersonalCalendarLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u1, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_P1", "Personal One", "p1")
	require.NoError(t, err)
	u2, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_P2", "Personal Two", "p2")
	require.NoError(t, err)

	member := &Calendar{Name: "Member", Slug: "personal-member", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, member))

	// Create.
	p1, err := CreatePersonalCalendar(ctx, pool, u1.ID, []string{member.ID}, "America/Chicago")
	require.NoError(t, err)
	assert.Equal(t, CalendarKindPersonal, p1.Kind)
	assert.Equal(t, PersonalCalendarName, p1.Name)
	assert.Empty(t, p1.Slug, "personal calendars have no slug")
	require.NotNil(t, p1.CreatedBy)
	assert.Equal(t, u1.ID, *p1.CreatedBy)

	// Second personal for the same user fails (one per user).
	_, err = CreatePersonalCalendar(ctx, pool, u1.ID, []string{member.ID}, "UTC")
	assert.Error(t, err, "unique partial index should reject a second personal calendar")

	// A different user can create one — NULL slugs must not collide.
	p2, err := CreatePersonalCalendar(ctx, pool, u2.ID, []string{member.ID}, "UTC")
	require.NoError(t, err)

	// Lookups.
	got, err := GetPersonalCalendar(ctx, pool, u1.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, p1.ID, got.ID)

	// Never reachable by slug (slug is NULL).
	_, err = GetCalendarBySlug(ctx, pool, "")
	assert.ErrorIs(t, err, ErrNotFound)

	// Excluded from ListCalendars.
	cals, err := ListCalendars(ctx, pool)
	require.NoError(t, err)
	for _, c := range cals {
		assert.NotEqual(t, p1.ID, c.ID, "personal calendars must not appear in the shared list")
	}

	// Members merge into the personal view.
	evt := &CalendarEvent{CalendarID: member.ID, Title: "Personal Event", StartTime: time.Now().Add(24 * time.Hour)}
	require.NoError(t, CreateCalendarEvent(ctx, pool, evt))
	merged, err := ListCompositeEvents(ctx, pool, p1.ID)
	require.NoError(t, err)
	assert.Len(t, merged, 1)

	// Same event surfaces for the other user's personal too.
	merged2, err := ListCompositeEvents(ctx, pool, p2.ID)
	require.NoError(t, err)
	assert.Len(t, merged2, 1)

	// Direct events on personal calendars are rejected.
	direct := &CalendarEvent{CalendarID: p1.ID, Title: "Nope", StartTime: time.Now().Add(24 * time.Hour)}
	err = CreateCalendarEvent(ctx, pool, direct)
	assert.ErrorIs(t, err, ErrNotEditable)
}

func TestCreatePersonalCalendarInvalidMember(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	u, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_P3", "Personal Three", "p3")
	require.NoError(t, err)

	// u2's personal calendar cannot be a member of u1's personal.
	other, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_P4", "Personal Four", "p4")
	require.NoError(t, err)
	otherPersonal, err := CreatePersonalCalendar(ctx, pool, other.ID, nil, "UTC")
	require.NoError(t, err)

	_, err = CreatePersonalCalendar(ctx, pool, u.ID, []string{otherPersonal.ID}, "UTC")
	assert.ErrorIs(t, err, ErrInvalidMember)

	// The failed creation must not leave a personal calendar behind.
	got, err := GetPersonalCalendar(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "failed creation should be rolled back")
}
