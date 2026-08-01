package discord

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// Notifier wraps the Discord client to satisfy platform.Notifier.
type Notifier struct {
	pool *pgxpool.Pool
}

// NewNotifier returns a Notifier that resolves internal user IDs to Discord user IDs
// and delivers messages as Discord DMs.
func NewNotifier(pool *pgxpool.Pool) *Notifier {
	return &Notifier{pool: pool}
}

// NotifyUser sends a plain-text DM to the user identified by their internal DB ID.
// If the user has no Discord identity, it returns nil (skip silently).
// Implements platform.Notifier.
func (n *Notifier) NotifyUser(ctx context.Context, userID string, msg platform.Message) error {
	discordID, err := store.GetUserExternalID(ctx, n.pool, userID, "discord")
	if errors.Is(err, store.ErrNotFound) {
		return nil // user has no Discord identity — skip silently
	}
	if err != nil {
		return fmt.Errorf("discord notifier: resolve Discord ID for user %s: %w", userID, err)
	}
	return PostDirectMessage(ctx, discordID, msg.Text)
}
