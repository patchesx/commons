package slack

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// SendInventoryVerificationReminders asks holders to confirm they still have their items and tells
// inventory admins when a holder hasn't answered.
//
//   - Held items whose verify cadence has elapsed get a DM with "I still have it" and "I don't have it"
//     buttons. Each item is reminded once per cycle; a verification or a new holder starts a new cycle.
//   - Items still unconfirmed once their grace period has passed (the item's own, else the
//     inventory.default_grace_days default) are escalated once to every inventory admin.
//
// There is no due date and nothing is "overdue": possession is simply unconfirmed. The schedule is the
// managed trigger seeded in main.go, edited on the Triggers page. jobs.inventory_verify_enabled can switch
// the job off without touching the trigger.
func SendInventoryVerificationReminders(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	client := getClient(ctx)
	if client == nil {
		return
	}
	if v, err := store.GetServiceConfig(ctx, pool, "jobs", "inventory_verify_enabled", encKey); err == nil &&
		strings.EqualFold(strings.TrimSpace(v), "false") {
		return
	}

	due, err := store.ListInventoryItemsDueForVerification(ctx, pool)
	if err != nil {
		log.Printf("slack/inventory-jobs: list items due for verification: %v", err)
		return
	}
	for _, it := range due {
		remindInventoryHolder(ctx, pool, client, it)
	}

	late, err := store.ListInventoryItemsPastGrace(ctx, pool)
	if err != nil {
		log.Printf("slack/inventory-jobs: list items past grace: %v", err)
		return
	}
	for _, it := range late {
		escalateInventoryItem(ctx, pool, client, it)
	}
}

// inventoryHolderRef names the holder for an admin alert: a Slack mention when they have a Slack account,
// otherwise their display name.
func inventoryHolderRef(ctx context.Context, pool *pgxpool.Pool, userID string) string {
	if slackID, err := store.GetUserExternalID(ctx, pool, userID, "slack"); err == nil && slackID != "" {
		return "<@" + slackID + ">"
	}
	if u, err := store.GetUserByID(ctx, pool, userID); err == nil {
		return "*" + escapeMrkdwn(u.DisplayName) + "*"
	}
	return "the holder"
}

func remindInventoryHolder(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, it store.InventoryItem) {
	if it.CurrentHolderUserID == nil {
		return
	}
	slackID, err := store.GetUserExternalID(ctx, pool, *it.CurrentHolderUserID, "slack")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("slack/inventory-jobs: look up Slack ID for item %s: %v", it.ID, err)
		return
	}

	if slackID == "" {
		// Nobody to ask: tell the admins now instead of waiting out the grace period.
		headline := fmt.Sprintf(":warning: *%s* (`%s`) is held by %s, who has no Slack account linked, so I can't ask them to confirm they still have it.",
			escapeMrkdwn(it.Name), store.FormatInventoryCode(it.Code), inventoryHolderRef(ctx, pool, *it.CurrentHolderUserID))
		if err := store.MarkInventoryItemReminded(ctx, pool, it.ID); err != nil {
			log.Printf("slack/inventory-jobs: mark reminded %s: %v", it.ID, err)
		}
		if alertInventoryAdmins(ctx, pool, client, it.ID, headline) == 0 {
			log.Printf("slack/inventory-jobs: no inventory admin with a Slack account to tell about item %s", it.ID)
			return
		}
		if err := store.MarkInventoryItemEscalated(ctx, pool, it.ID); err != nil {
			log.Printf("slack/inventory-jobs: mark escalated %s: %v", it.ID, err)
		}
		return
	}

	text := fmt.Sprintf("Do you still have %s (%s)?", it.Name, store.FormatInventoryCode(it.Code))
	if _, _, err := SendDM(ctx, client, slackID, text, inventoryReminderBlocks(it)...); err != nil {
		log.Printf("slack/inventory-jobs: remind %s about item %s: %v", slackID, it.ID, err)
	}
	// Stamped even if the send failed (it is queued for retry), so a Slack outage doesn't cause a storm.
	if err := store.MarkInventoryItemReminded(ctx, pool, it.ID); err != nil {
		log.Printf("slack/inventory-jobs: mark reminded %s: %v", it.ID, err)
	}
}

func escalateInventoryItem(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, it store.InventoryItem) {
	if it.CurrentHolderUserID == nil || it.LastRemindedAt == nil {
		return
	}
	days := int(time.Since(*it.LastRemindedAt).Hours() / 24)
	ago := fmt.Sprintf("%d days", days)
	if days == 1 {
		ago = "1 day"
	}
	headline := fmt.Sprintf(":warning: %s hasn't confirmed they still have *%s* (`%s`). I asked %s ago and haven't heard back, so possession is unconfirmed.",
		inventoryHolderRef(ctx, pool, *it.CurrentHolderUserID), escapeMrkdwn(it.Name), store.FormatInventoryCode(it.Code), ago)
	if alertInventoryAdmins(ctx, pool, client, it.ID, headline) == 0 {
		log.Printf("slack/inventory-jobs: no inventory admin with a Slack account to tell about item %s", it.ID)
		return
	}
	if err := store.MarkInventoryItemEscalated(ctx, pool, it.ID); err != nil {
		log.Printf("slack/inventory-jobs: mark escalated %s: %v", it.ID, err)
	}
}
