package matrix

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// Notifier wraps the Matrix client to satisfy platform.Notifier.
type Notifier struct {
	pool *pgxpool.Pool
}

// NewNotifier returns a Notifier that resolves internal user IDs to Matrix user IDs
// and delivers messages as Matrix DMs.
func NewNotifier(pool *pgxpool.Pool) *Notifier {
	return &Notifier{pool: pool}
}

// NotifyUser sends a plain-text DM to the user identified by their internal DB ID.
// If the user has no Matrix identity, it returns nil (skip silently).
// Implements platform.Notifier.
func (n *Notifier) NotifyUser(ctx context.Context, userID string, msg platform.Message) error {
	matrixID, err := store.GetUserExternalID(ctx, n.pool, userID, "matrix")
	if errors.Is(err, store.ErrNotFound) {
		return nil // user has no Matrix identity — skip silently
	}
	if err != nil {
		return fmt.Errorf("matrix notifier: resolve Matrix ID for user %s: %w", userID, err)
	}
	return PostDirectMessage(ctx, matrixID, msg.Text)
}
