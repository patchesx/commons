package slack

import (
	"context"
	"errors"
	"fmt"
	"log"

	"commons/store"
)

func init() {
	registerDMCommand(dmCommand{
		Prefix:  "Checkout",
		Usage:   "Checkout <item code>",
		Help:    "take possession of an inventory item",
		Example: "Checkout K7M-4QX",
		Handle:  handleCheckoutCommand,
	})
}

// handleCheckoutCommand takes possession of the item with the given code for the sender.
func handleCheckoutCommand(ctx context.Context, d dmContext, args string) {
	code := store.NormalizeInventoryCode(args)
	if code == "" {
		d.reply(ctx, "Tell me which item: `Checkout <item code>`, for example `Checkout K7M-4QX`.")
		return
	}
	item, err := store.GetInventoryItemByCode(ctx, d.pool, code)
	if errors.Is(err, store.ErrNotFound) {
		d.reply(ctx, fmt.Sprintf(":warning: No active item has the code `%s`. Check the code on the item's label and try again.", escapeMrkdwn(args)))
		return
	}
	if err != nil {
		log.Printf("slack/dm: checkout lookup %q: %v", code, err)
		d.reply(ctx, ":warning: Something went wrong looking that item up. Try again in a moment.")
		return
	}
	d.reply(ctx, claimInventoryItem(ctx, d.pool, d.client, d.slackUserID, item.ID))
}
