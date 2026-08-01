package slack

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// SendMeetingReminders queries for occurrences starting in ~1 hour and sends
// a reminder DM (bot-scheduled) or channel post (sync-imported).
// MarkReminderSent is called regardless of send success to prevent retry storms.
func SendMeetingReminders(ctx context.Context, pool *pgxpool.Pool, encKey []byte, sentinelUserID string) {
	client := getClient(ctx)
	if client == nil {
		return
	}

	occs, err := store.ListOccurrencesDueForReminder(ctx, pool, encKey, sentinelUserID)
	if err != nil {
		log.Printf("slack/reminders: list occurrences: %v", err)
		return
	}

	for _, occ := range occs {
		if occ.IsSyncImport {
			sendSyncImportReminder(ctx, pool, encKey, client, occ)
		} else {
			sendBotScheduledReminder(ctx, pool, encKey, client, occ)
		}
		if err := store.MarkReminderSent(ctx, pool, occ.OccurrenceID); err != nil {
			log.Printf("slack/reminders: mark sent for occurrence %s: %v", occ.OccurrenceID, err)
		}
	}
}

func sendBotScheduledReminder(ctx context.Context, pool *pgxpool.Pool, _ []byte, client *slacklib.Client, occ store.OccurrenceReminder) {
	slackID, err := store.GetUserExternalID(ctx, pool, occ.CreatedByID, "slack")
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("slack/reminders: no Slack identity for user %s (occurrence %s)", occ.CreatedByID, occ.OccurrenceID)
		return
	}
	if err != nil {
		log.Printf("slack/reminders: get Slack ID for user %s: %v", occ.CreatedByID, err)
		return
	}

	loc, _ := time.LoadLocation(occ.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	startFormatted := occ.StartTime.In(loc).Format("Mon Jan 2, 2006 at 3:04 PM MST")

	text := fmt.Sprintf(
		"*Reminder:* Your meeting starts in about an hour.\n*%s*\n%s\n*Start link (do not share):* %s\nJoin link: %s",
		occ.Topic, startFormatted, occ.StartURL, occ.JoinURL,
	)

	if _, _, err := SendDM(ctx, client, slackID, text); err != nil {
		log.Printf("slack/reminders: DM to %s for occurrence %s: %v", slackID, occ.OccurrenceID, err)
	}
}

func sendSyncImportReminder(ctx context.Context, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, occ store.OccurrenceReminder) {
	enabled, _ := store.GetServiceConfig(ctx, pool, "meetings", "sync_reminder_enabled", encKey)
	if enabled != "true" {
		return
	}
	channel, _ := store.GetServiceConfig(ctx, pool, "meetings", "sync_reminder_channel", encKey)
	if channel == "" {
		return
	}

	loc, _ := time.LoadLocation(occ.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	startFormatted := occ.StartTime.In(loc).Format("Mon Jan 2, 2006 at 3:04 PM MST")

	text := fmt.Sprintf(
		"*Upcoming meeting in ~1 hour:*\n*%s*\n%s\nJoin: %s",
		occ.Topic, startFormatted, occ.JoinURL,
	)

	if _, _, err := client.PostMessageContext(ctx, channel, slacklib.MsgOptionText(text, false)); err != nil {
		log.Printf("slack/reminders: channel post for occurrence %s: %v", occ.OccurrenceID, err)
	}
}
