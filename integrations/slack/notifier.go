package slack

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// Notifier wraps the Slack client to satisfy platform.Notifier.
// Construct one via NewNotifier and pass it to features that need to send notifications.
type Notifier struct {
	pool *pgxpool.Pool
}

// NewNotifier returns a Notifier that resolves internal user IDs to Slack IDs
// and delivers messages as Slack DMs.
func NewNotifier(pool *pgxpool.Pool) *Notifier {
	return &Notifier{pool: pool}
}

// NotifyUser sends a plain-text DM to the user identified by their internal DB ID.
// Implements platform.Notifier.
func (n *Notifier) NotifyUser(ctx context.Context, userID string, msg platform.Message) error {
	client := getClient(ctx)
	if client == nil {
		return fmt.Errorf("slack notifier: bot_token not configured")
	}
	slackID, err := store.GetUserExternalID(ctx, n.pool, userID, "slack")
	if err != nil {
		return fmt.Errorf("slack notifier: resolve Slack ID for user %s: %w", userID, err)
	}
	_, _, err = SendDM(ctx, client, slackID, msg.Text)
	return err
}
