package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/store"
)

func TestFormatCalendarReminder(t *testing.T) {
	chi, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)

	start := time.Date(2027, 1, 5, 15, 4, 0, 0, chi) // Tuesday, CST
	end := start.Add(time.Hour)

	t.Run("timed with end", func(t *testing.T) {
		d := store.ReminderDue{
			LeadMinutes:  60,
			Title:        "General Meeting",
			CalendarName: "Chapter Events",
			StartTime:    start,
			EndTime:      &end,
			Timezone:     "America/Chicago",
		}
		assert.Equal(t,
			"_60 minutes until this event:_\nTue Jan 5, 2027 at 3:04 PM – 4:04 PM CST\nGeneral Meeting (Chapter Events)",
			formatCalendarReminder(d))
	})

	t.Run("where and details lines when present", func(t *testing.T) {
		where := "Main Hall"
		details := "Weekly organizing meeting"
		d := store.ReminderDue{
			LeadMinutes:  60,
			Title:        "General Meeting",
			CalendarName: "Chapter Events",
			StartTime:    start,
			EndTime:      &end,
			Location:     &where,
			Description:  &details,
			Timezone:     "America/Chicago",
		}
		assert.Equal(t,
			"_60 minutes until this event:_\nTue Jan 5, 2027 at 3:04 PM – 4:04 PM CST\nGeneral Meeting (Chapter Events)\n*Where:* Main Hall\n*Details:* Weekly organizing meeting",
			formatCalendarReminder(d))
	})

	t.Run("location only, no description", func(t *testing.T) {
		where := "The Spot"
		d := store.ReminderDue{
			LeadMinutes:  15,
			Title:        "Flyering",
			CalendarName: "Actions",
			StartTime:    start,
			Location:     &where,
			Timezone:     "America/Chicago",
		}
		assert.Equal(t,
			"_15 minutes until this event:_\nTue Jan 5, 2027 at 3:04 PM CST\nFlyering (Actions)\n*Where:* The Spot",
			formatCalendarReminder(d))
	})

	t.Run("timed without end", func(t *testing.T) {
		d := store.ReminderDue{
			LeadMinutes:  30,
			Title:        "Quick Sync",
			CalendarName: "Team",
			StartTime:    start,
			Timezone:     "America/Chicago",
		}
		assert.Equal(t,
			"_30 minutes until this event:_\nTue Jan 5, 2027 at 3:04 PM CST\nQuick Sync (Team)",
			formatCalendarReminder(d))
	})

	t.Run("all day", func(t *testing.T) {
		d := store.ReminderDue{
			LeadMinutes:  1440,
			Title:        "Strike Day",
			CalendarName: "Actions",
			StartTime:    start,
			AllDay:       true,
			Timezone:     "America/Chicago",
		}
		assert.Equal(t,
			"_1440 minutes until this event:_\nTue Jan 5, 2027 (all day)\nStrike Day (Actions)",
			formatCalendarReminder(d))
	})

	t.Run("multi-day end keeps its date", func(t *testing.T) {
		endNextDay := start.Add(20 * time.Hour)
		d := store.ReminderDue{
			LeadMinutes:  60,
			Title:        "Retreat",
			CalendarName: "Org",
			StartTime:    start,
			EndTime:      &endNextDay,
			Timezone:     "America/Chicago",
		}
		assert.Equal(t,
			"_60 minutes until this event:_\nTue Jan 5, 2027 at 3:04 PM – Wed Jan 6, 2027 at 11:04 AM CST\nRetreat (Org)",
			formatCalendarReminder(d))
	})
}
