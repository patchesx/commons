package slack

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// Messages to the member who ends up holding an item they didn't take in Slack themselves: a check-in on the
// public claim page that matched their email, another member handing it off to them, or an admin reassigning
// the item or linking a claim to them. The previous holder is not messaged. A member with no Slack account
// is skipped.

// dmMember DMs a member if they have a Slack account.
func dmMember(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, userID, text string) {
	slackID, err := store.GetUserExternalID(ctx, pool, userID, "slack")
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("slack/inventory: look up Slack ID for %s: %v", userID, err)
		}
		return
	}
	if slackID == "" {
		return
	}
	if _, _, err := SendDM(ctx, client, slackID, text); err != nil {
		log.Printf("slack/inventory: message %s: %v", userID, err)
	}
}

// inventoryNoticeName names an item for a notice. ok is false if the item can't be found.
func inventoryNoticeName(ctx context.Context, pool *pgxpool.Pool, itemID string) (string, bool) {
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil {
		log.Printf("slack/inventory: load item %s for a notice: %v", itemID, err)
		return "", false
	}
	return fmt.Sprintf("*%s* (`%s`)", escapeMrkdwn(item.Name), store.FormatInventoryCode(item.Code)), true
}

// NotifyWebClaimApplied tells a member that a check-in on the public claim page, using their email address,
// gave them the item. If it wasn't them, this is how they find out.
func NotifyWebClaimApplied(ctx context.Context, pool *pgxpool.Pool, itemID, userID string) {
	client := getClient(ctx)
	if client == nil {
		return
	}
	item, ok := inventoryNoticeName(ctx, pool, itemID)
	if !ok {
		return
	}
	dmMember(ctx, pool, client, userID, fmt.Sprintf(
		":package: You now have %s, checked in on the web form with your email address. If this wasn't you, please contact the Inventory Admins.", item))
}

// NotifyClaimLinked tells a member that an inventory admin matched a web-form check-in to them.
func NotifyClaimLinked(ctx context.Context, pool *pgxpool.Pool, itemID, userID string) {
	client := getClient(ctx)
	if client == nil {
		return
	}
	notifyClaimLinked(ctx, pool, client, itemID, userID)
}

func notifyClaimLinked(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, itemID, userID string) {
	item, ok := inventoryNoticeName(ctx, pool, itemID)
	if !ok {
		return
	}
	dmMember(ctx, pool, client, userID, fmt.Sprintf(
		":package: An inventory admin matched a web-form check-in to you, so you now have %s. If that isn't right, please contact the Inventory Admins.", item))
}

// notifyHandedOff tells a member that another member handed the item off to them.
func notifyHandedOff(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, itemID, userID, fromSlackID string) {
	item, ok := inventoryNoticeName(ctx, pool, itemID)
	if !ok {
		return
	}
	dmMember(ctx, pool, client, userID, fmt.Sprintf(":package: <@%s> handed %s off to you. You now have it.", fromSlackID, item))
}

// notifyReassigned tells a member that an admin assigned them the item.
func notifyReassigned(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, itemID, userID, adminSlackID string) {
	item, ok := inventoryNoticeName(ctx, pool, itemID)
	if !ok {
		return
	}
	dmMember(ctx, pool, client, userID, fmt.Sprintf(":package: <@%s> assigned %s to you. You now have it.", adminSlackID, item))
}
