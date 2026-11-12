package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CalendarReminder is one notification config: a Slack channel notified
// lead_minutes before each of the calendar's occurrences.
type CalendarReminder struct {
	ID          string
	CalendarID  string
	ChannelID   string
	LeadMinutes int
	CreatedAt   time.Time
}

// ReminderDue is one occurrence that has entered its reminder window on some
// calendar config — the unit the job dispatches to the Slack trigger.
type ReminderDue struct {
	ReminderID string
	CalendarID string
	// CalendarName is the config's own calendar (for attribution).
	CalendarName string
	ChannelID    string
	LeadMinutes  int

	EventID         string
	OccurrenceStart time.Time
	Title           string
	StartTime       time.Time
	EndTime         *time.Time
	AllDay          bool
	Description     *string
	Location        *string
	URL             *string
	Timezone        string
}

// ReminderFingerprint uniquely addresses one send: the channel, event, and
// occurrence slot. One message per (channel, occurrence) regardless of how
// many calendar configs cover it.
func ReminderFingerprint(channelID, eventID string, occurrenceStart time.Time) string {
	return fmt.Sprintf("%s|%s|%s", channelID, eventID, occurrenceStart.UTC().Truncate(time.Second).Format(time.RFC3339))
}

// ListCalendarReminders returns all reminder configs for a calendar.
func ListCalendarReminders(ctx context.Context, pool *pgxpool.Pool, calendarID string) ([]CalendarReminder, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, calendar_id, channel_id, lead_minutes, created_at
		FROM calendar_reminders
		WHERE calendar_id = $1
		ORDER BY created_at
	`, calendarID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarReminder
	for rows.Next() {
		var r CalendarReminder
		if err := rows.Scan(&r.ID, &r.CalendarID, &r.ChannelID, &r.LeadMinutes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetCalendarReminders atomically replaces a calendar's reminder configs.
// Personal calendars cannot carry reminders (no channel audience of their own).
func SetCalendarReminders(ctx context.Context, pool *pgxpool.Pool, calendarID string, reminders []CalendarReminder) error {
	cal, err := GetCalendarByID(ctx, pool, calendarID)
	if err != nil {
		return err
	}
	if cal.Kind == CalendarKindPersonal {
		return errors.New("personal calendars cannot have channel reminders")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM calendar_reminders WHERE calendar_id = $1`, calendarID); err != nil {
		return err
	}
	for _, r := range reminders {
		if r.ChannelID == "" {
			return errors.New("channel is required")
		}
		if r.LeadMinutes < 1 {
			return errors.New("lead minutes must be at least 1")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO calendar_reminders (calendar_id, channel_id, lead_minutes)
			VALUES ($1, $2, $3)
		`, calendarID, r.ChannelID, r.LeadMinutes); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReminderAlreadySent reports whether a fingerprint has been recorded.
func ReminderAlreadySent(ctx context.Context, pool *pgxpool.Pool, fingerprint string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calendar_reminder_sent WHERE fingerprint = $1)`, fingerprint).Scan(&exists)
	return exists, err
}

// MarkReminderFingerprintSent records a fingerprint. The reminder_id is only
// bookkeeping — the PK (reminder_id, fingerprint) allows the same fingerprint
// under several configs without erroring.
func MarkReminderFingerprintSent(ctx context.Context, pool *pgxpool.Pool, reminderID, fingerprint string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO calendar_reminder_sent (reminder_id, fingerprint)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, reminderID, fingerprint)
	return err
}

// occurrenceSlot addresses one materialized occurrence (event + series slot).
type occurrenceSlot struct {
	eventID string
	start   string // RFC3339 UTC, second precision
}

// ListDueCalendarReminders returns every occurrence that has entered its
// reminder window as of now, across all reminder configs.
//
// Dedupe rule: per (channel, occurrence), the config with the longest lead time
// wins. When both a composite and its member — or any two configs — cover the
// same occurrence on the same channel, the longest lead fires first and its
// fingerprint suppresses the rest (and any later job re-runs). Attribution
// goes to the winning config's own calendar.
//
// The expansion horizon is 24h: nothing farther out can be due yet, since a
// due occurrence satisfies start - lead <= now, i.e. start <= now + lead.
func ListDueCalendarReminders(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]ReminderDue, error) {
	reminders, err := listAllReminders(ctx, pool)
	if err != nil {
		return nil, err
	}
	if len(reminders) == 0 {
		return nil, nil
	}
	horizon := now.Add(24 * time.Hour)

	// Expand each config's calendar once; carry occurrence data through.
	type cover struct {
		occ      EventOccurrence
		reminder CalendarReminder
		calName  string
		timezone string
	}
	// byChannelOcc maps channel → occurrence slot → covering configs.
	byChannelOcc := map[string]map[occurrenceSlot][]cover{}

	for _, r := range reminders {
		cal, err := GetCalendarByID(ctx, pool, r.CalendarID)
		if err != nil {
			continue // calendar deleted since listing
		}
		var occs []EventOccurrence
		if cal.Kind == CalendarKindComposite {
			occs, err = ListCompositeOccurrences(ctx, pool, cal.ID, now, horizon)
		} else {
			occs, err = ListCalendarOccurrences(ctx, pool, cal.ID, now, horizon)
		}
		if err != nil {
			continue
		}
		for _, occ := range occs {
			key := occurrenceSlot{occ.ID, occ.OccurrenceStart.UTC().Truncate(time.Second).Format(time.RFC3339)}
			m, ok := byChannelOcc[r.ChannelID]
			if !ok {
				m = map[occurrenceSlot][]cover{}
				byChannelOcc[r.ChannelID] = m
			}
			m[key] = append(m[key], cover{occ, r, cal.Name, cal.Timezone})
		}
	}

	var due []ReminderDue
	for channelID, occs := range byChannelOcc {
		for _, covers := range occs {
			// Longest lead wins; stable sort keeps insertion order for ties.
			sort.SliceStable(covers, func(i, j int) bool { return covers[i].reminder.LeadMinutes > covers[j].reminder.LeadMinutes })
			winner := covers[0]

			// Due check: within the lead window (start - lead <= now < start).
			fireAt := winner.occ.StartTime.Add(-time.Duration(winner.reminder.LeadMinutes) * time.Minute)
			if now.Before(fireAt) || !now.Before(winner.occ.StartTime) {
				continue
			}

			due = append(due, ReminderDue{
				ReminderID:      winner.reminder.ID,
				CalendarID:      winner.reminder.CalendarID,
				CalendarName:    winner.calName,
				ChannelID:       channelID,
				LeadMinutes:     winner.reminder.LeadMinutes,
				EventID:         winner.occ.ID,
				OccurrenceStart: winner.occ.OccurrenceStart,
				Title:           winner.occ.Title,
				StartTime:       winner.occ.StartTime,
				EndTime:         winner.occ.EndTime,
				AllDay:          winner.occ.AllDay,
				Description:     winner.occ.Description,
				Location:        winner.occ.Location,
				URL:             winner.occ.URL,
				Timezone:        winner.timezone,
			})
		}
	}

	// Suppress fingerprints already sent.
	var out []ReminderDue
	for _, d := range due {
		sent, err := ReminderAlreadySent(ctx, pool, ReminderFingerprint(d.ChannelID, d.EventID, d.OccurrenceStart))
		if err == nil && sent {
			continue
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out, nil
}

func listAllReminders(ctx context.Context, pool *pgxpool.Pool) ([]CalendarReminder, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, calendar_id, channel_id, lead_minutes, created_at
		FROM calendar_reminders
		ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarReminder
	for rows.Next() {
		var r CalendarReminder
		if err := rows.Scan(&r.ID, &r.CalendarID, &r.ChannelID, &r.LeadMinutes, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
