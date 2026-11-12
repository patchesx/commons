package slack

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// SendCalendarReminders queries for calendar occurrences inside their reminder
// window and posts each to its configured Slack channel.
//
// Reminder configs live in calendar_reminders (per calendar: channel + lead
// minutes, default 60). Due computation dedupes across composite/member
// overlaps: per (channel, occurrence) the longest lead wins, and fingerprints
// recorded in calendar_reminder_sent suppress re-sends on subsequent runs —
// the same anti-storm policy as meeting reminders (mark regardless of send
// success).
//
// Wired via the jobs.calendar_reminders callback action, seeded every 1m.
func SendCalendarReminders(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	_ = encKey // reserved for future encrypted config; channels are plain IDs
	if getClient(ctx) == nil {
		return
	}

	due, err := store.ListDueCalendarReminders(ctx, pool, time.Now())
	if err != nil {
		log.Printf("slack/calendarreminders: list due: %v", err)
		return
	}

	for _, d := range due {
		if err := PostChannelMessage(ctx, d.ChannelID, formatCalendarReminder(d)); err != nil {
			log.Printf("slack/calendarreminders: post to %s for %q: %v", d.ChannelID, d.Title, err)
		}
		fingerprint := store.ReminderFingerprint(d.ChannelID, d.EventID, d.OccurrenceStart)
		if err := store.MarkReminderFingerprintSent(ctx, pool, d.ReminderID, fingerprint); err != nil {
			log.Printf("slack/calendarreminders: mark sent: %v", err)
		}
	}
}

// formatCalendarReminder renders one reminder message:
//
//	_60 minutes until this event:_
//	Tue Jan 5, 2027 at 3:04 PM – 4:04 PM CST
//	Event Name (Calendar Name)
//	*Where:* Main Hall
//	*Details:* Weekly organizing meeting
//
// Where/Details lines appear only when the event has them.
func formatCalendarReminder(d store.ReminderDue) string {
	loc := mustRemLoc(d.Timezone)
	start := d.StartTime.In(loc)

	var whenText string
	if d.AllDay {
		whenText = start.Format("Mon Jan 2, 2006 (all day)")
	} else if d.EndTime != nil {
		end := d.EndTime.In(loc)
		// Zone abbreviation shown once, on the final time.
		if sameRemDay(start, end) {
			whenText = start.Format("Mon Jan 2, 2006 at 3:04 PM") + " – " + end.Format("3:04 PM MST")
		} else {
			whenText = start.Format("Mon Jan 2, 2006 at 3:04 PM") + " – " + end.Format("Mon Jan 2, 2006 at 3:04 PM MST")
		}
	} else {
		whenText = start.Format("Mon Jan 2, 2006 at 3:04 PM MST")
	}

	msg := fmt.Sprintf("_%d minutes until this event:_\n%s\n%s (%s)",
		d.LeadMinutes, whenText, d.Title, d.CalendarName)
	if d.Location != nil && *d.Location != "" {
		msg += "\n*Where:* " + *d.Location
	}
	if d.Description != nil && *d.Description != "" {
		msg += "\n*Details:* " + *d.Description
	}
	return msg
}

// sameRemDay reports whether two instants fall on the same local calendar day.
func sameRemDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func mustRemLoc(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}
