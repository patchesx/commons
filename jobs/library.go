package jobs

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// SendOverdueReminders checks for active checkouts past their due date and
// notifies the borrower if they have not been notified in the last 23 hours.
func SendOverdueReminders(ctx context.Context, pool *pgxpool.Pool, notifier platform.Notifier) error {
	if notifier == nil {
		return nil
	}

	checkouts, err := store.ListOverdueUnnotifiedCheckouts(ctx, pool)
	if err != nil {
		return fmt.Errorf("list overdue checkouts: %w", err)
	}
	for _, co := range checkouts {
		if co.DueDate == nil {
			continue
		}
		text := fmt.Sprintf(
			"Reminder: %s was due on %s. Please return it at your earliest convenience.",
			co.BookTitle, co.DueDate.Format("January 2, 2006"),
		)
		if err := notifier.NotifyUser(ctx, co.UserID, platform.Message{Text: text}); err != nil {
			log.Printf("jobs/library: notify overdue user %s: %v", co.UserID, err)
		}
		if err := store.MarkCheckoutOverdueNotified(ctx, pool, co.ID); err != nil {
			log.Printf("jobs/library: mark notified %s: %v", co.ID, err)
		}
	}
	return nil
}
