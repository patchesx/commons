package jobs

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// SendMeetingReminders queries for occurrences starting in ~1 hour and fires
// the scheduler.meeting_reminder trigger for each.
// MarkReminderSent is called regardless of fire success to prevent retry storms.
func SendMeetingReminders(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	sentinelID, _ := store.GetZoomSentinelUserID(ctx, pool)

	occs, err := store.ListOccurrencesDueForReminder(ctx, pool, encKey, sentinelID)
	if err != nil {
		log.Printf("jobs/reminders: list occurrences: %v", err)
		return
	}

	for _, occ := range occs {
		if !occ.IsSyncImport {
			sendBotScheduledReminder(ctx, pool, occ)
		}
		if err := store.MarkReminderSent(ctx, pool, occ.OccurrenceID); err != nil {
			log.Printf("jobs/reminders: mark sent for occurrence %s: %v", occ.OccurrenceID, err)
		}
	}
}

func sendBotScheduledReminder(ctx context.Context, pool *pgxpool.Pool, occ store.OccurrenceReminder) {
	loc, _ := time.LoadLocation(occ.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	startFormatted := occ.StartTime.In(loc).Format("Mon Jan 2, 2006 at 3:04 PM MST")

	data := map[string]any{
		"topic":      occ.Topic,
		"start_time": startFormatted,
		"start_url":  occ.StartURL,
		"join_url":   occ.JoinURL,
	}
	slackID, err := store.GetUserExternalID(ctx, pool, occ.CreatedByID, "slack")
	if err != nil {
		log.Printf("jobs/reminders: no Slack identity for user %s: %v", occ.CreatedByID, err)
	}
	data["user_slack_id"] = slackID

	if err := plugin.Fire(ctx, "scheduler.meeting_reminder", occ.CreatedByID, data); err != nil {
		log.Printf("jobs/reminders: fire trigger for occurrence %s: %v", occ.OccurrenceID, err)
	}
}
