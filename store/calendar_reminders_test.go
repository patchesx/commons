package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestReminderConfigCRUD(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	cal := &Calendar{Name: "Rem Cal", Slug: "rem-cal", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))

	// Set two configs.
	require.NoError(t, SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{
		{CalendarID: cal.ID, ChannelID: "C1", LeadMinutes: 60},
		{CalendarID: cal.ID, ChannelID: "C2", LeadMinutes: 30},
	}))
	got, err := ListCalendarReminders(ctx, pool, cal.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "C1", got[0].ChannelID)
	assert.Equal(t, 60, got[0].LeadMinutes)

	// Replace with one.
	require.NoError(t, SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{
		{CalendarID: cal.ID, ChannelID: "C2", LeadMinutes: 1440},
	}))
	got, err = ListCalendarReminders(ctx, pool, cal.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 1440, got[0].LeadMinutes)

	// Clear entirely.
	require.NoError(t, SetCalendarReminders(ctx, pool, cal.ID, nil))
	got, err = ListCalendarReminders(ctx, pool, cal.ID)
	require.NoError(t, err)
	assert.Empty(t, got)

	// Validation.
	err = SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{{ChannelID: "", LeadMinutes: 60}})
	assert.ErrorContains(t, err, "channel")
	err = SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{{ChannelID: "C1", LeadMinutes: 0}})
	assert.ErrorContains(t, err, "lead")

	// Personal calendars rejected.
	pu, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REMCAL", "rem", "Rem User")
	require.NoError(t, err)
	personal, err := CreatePersonalCalendar(ctx, pool, pu.ID, nil, "UTC")
	require.NoError(t, err)
	err = SetCalendarReminders(ctx, pool, personal.ID, []CalendarReminder{{ChannelID: "C1", LeadMinutes: 60}})
	assert.ErrorContains(t, err, "personal")
}

func TestListDueCalendarRemindersDedupeCompositeMember(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	member := &Calendar{Name: "Member", Slug: "rem-member", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, member))
	comp := &Calendar{Name: "Comp", Slug: "rem-comp", Timezone: "UTC", Kind: CalendarKindComposite}
	require.NoError(t, CreateCalendar(ctx, pool, comp))
	require.NoError(t, AddCalendarMembers(ctx, pool, comp.ID, []string{member.ID}))

	// Event starts in 30 minutes — inside both the member's 60m lead and the
	// composite's 15m lead.
	start := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	evt := &CalendarEvent{CalendarID: member.ID, Title: "Soon Event", StartTime: start}
	require.NoError(t, CreateCalendarEvent(ctx, pool, evt))

	// Both configs on the SAME channel: longest lead (member, 60m) wins.
	require.NoError(t, SetCalendarReminders(ctx, pool, member.ID, []CalendarReminder{
		{ChannelID: "C_SHARED", LeadMinutes: 60},
	}))
	require.NoError(t, SetCalendarReminders(ctx, pool, comp.ID, []CalendarReminder{
		{ChannelID: "C_SHARED", LeadMinutes: 15},
	}))

	due, err := ListDueCalendarReminders(ctx, pool, time.Now())
	require.NoError(t, err)
	require.Len(t, due, 1, "composite/member overlap on one channel must dedupe to one reminder")
	assert.Equal(t, 60, due[0].LeadMinutes, "longest lead wins")
	assert.Equal(t, "Member", due[0].CalendarName, "attribution goes to the winning config's calendar")

	// Fingerprint recorded → not due again.
	fp := ReminderFingerprint(due[0].ChannelID, due[0].EventID, due[0].OccurrenceStart)
	require.NoError(t, MarkReminderFingerprintSent(ctx, pool, due[0].ReminderID, fp))
	due2, err := ListDueCalendarReminders(ctx, pool, time.Now())
	require.NoError(t, err)
	assert.Empty(t, due2, "sent fingerprint suppresses re-sends")
}

func TestListDueCalendarRemindersDifferentChannelsBothFire(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	cal := &Calendar{Name: "Dual", Slug: "rem-dual", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))
	start := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	evt := &CalendarEvent{CalendarID: cal.ID, Title: "Dual Event", StartTime: start}
	require.NoError(t, CreateCalendarEvent(ctx, pool, evt))

	require.NoError(t, SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{
		{CalendarID: cal.ID, ChannelID: "C_A", LeadMinutes: 60},
		{CalendarID: cal.ID, ChannelID: "C_B", LeadMinutes: 60},
	}))

	due, err := ListDueCalendarReminders(ctx, pool, time.Now())
	require.NoError(t, err)
	require.Len(t, due, 2, "one message per channel")
	channels := []string{due[0].ChannelID, due[1].ChannelID}
	assert.ElementsMatch(t, []string{"C_A", "C_B"}, channels)
}

func TestListDueCalendarRemindersWindowRespected(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	cal := &Calendar{Name: "Window", Slug: "rem-window", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))
	now := time.Now().UTC()

	events := []struct {
		title  string
		offset time.Duration
	}{
		{"Too Far", 90 * time.Minute}, // beyond 60m lead → not yet due
		{"Just Right", 30 * time.Minute},
		{"Started", -5 * time.Minute}, // already started → not due
	}
	for _, e := range events {
		evt := &CalendarEvent{CalendarID: cal.ID, Title: e.title, StartTime: now.Add(e.offset).UTC().Truncate(time.Second)}
		require.NoError(t, CreateCalendarEvent(ctx, pool, evt))
	}
	require.NoError(t, SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{
		{CalendarID: cal.ID, ChannelID: "C_W", LeadMinutes: 60},
	}))

	due, err := ListDueCalendarReminders(ctx, pool, now)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "Just Right", due[0].Title)
}

func TestListDueCalendarRemindersRecurringAndOverrides(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	cal := &Calendar{Name: "Series", Slug: "rem-series", Timezone: "UTC"}
	require.NoError(t, CreateCalendar(ctx, pool, cal))
	now := time.Now().UTC()

	// Daily series; occurrence today skipped, today+1 edited to a new title.
	today := now.Add(30 * time.Minute).Truncate(time.Second)
	// Align to a clean daily grid from yesterday.
	dtstart := today.AddDate(0, 0, -1)
	series := &CalendarEvent{
		CalendarID: cal.ID, Title: "Daily Sync", StartTime: dtstart, RRule: "FREQ=DAILY",
	}
	require.NoError(t, CreateCalendarEvent(ctx, pool, series))
	require.NoError(t, UpsertEventOverride(ctx, pool, &EventOverride{
		SeriesID: series.ID, OccurrenceStart: today, Deleted: true,
	}))
	newTitle := "Daily Sync (special)"
	require.NoError(t, UpsertEventOverride(ctx, pool, &EventOverride{
		SeriesID: series.ID, OccurrenceStart: today.AddDate(0, 0, 1), Title: &newTitle,
		StartTime: ptrTime(today.AddDate(0, 0, 1)),
	}))

	require.NoError(t, SetCalendarReminders(ctx, pool, cal.ID, []CalendarReminder{
		{CalendarID: cal.ID, ChannelID: "C_S", LeadMinutes: 60},
	}))

	due, err := ListDueCalendarReminders(ctx, pool, now)
	require.NoError(t, err)
	// Today's occurrence is skipped → nothing due in the lead window today…
	// (tomorrow's edited occurrence is 24h out — outside its 60m lead.)
	titles := []string{}
	for _, d := range due {
		titles = append(titles, d.Title)
	}
	assert.NotContains(t, titles, "Daily Sync", "skipped occurrence must not remind")
	assert.NotContains(t, titles, "Daily Sync (special)", "tomorrow's occurrence is not yet due")
}

func ptrTime(t time.Time) *time.Time { return &t }
