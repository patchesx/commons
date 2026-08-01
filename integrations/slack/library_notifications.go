package slack

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/permissions"
	"commons/store"
)

// notifyLibraryManagers DMs every user with library.manage permission who has a
// linked Slack identity. Used for admin-directed events (new request, book returned
// with holds waiting).
func notifyLibraryManagers(ctx context.Context, pool *pgxpool.Pool, text string) {
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack: notifyLibraryManagers: bot_token not configured")
		return
	}
	managers, err := store.ListUsersWithPermission(ctx, pool, permissions.LibraryManage)
	if err != nil {
		log.Printf("slack: notifyLibraryManagers: list users: %v", err)
		return
	}
	for _, u := range managers {
		slackID, err := store.GetUserExternalID(ctx, pool, u.ID, "slack")
		if err != nil {
			log.Printf("slack: notifyLibraryManagers: no Slack identity for %s: %v", u.ID, err)
			continue
		}
		if _, _, err := SendDM(ctx, client, slackID, text); err != nil {
			log.Printf("slack: notifyLibraryManagers: DM to %s: %v", slackID, err)
		}
	}
}

// NotifyNewCheckoutRequest DMs all library managers when a member requests a checkout.
func NotifyNewCheckoutRequest(ctx context.Context, pool *pgxpool.Pool, memberName, bookTitle string) {
	text := fmt.Sprintf(
		":books: New checkout request: *%s* wants to borrow *%s*. Open Manage Library to approve or deny.",
		memberName, bookTitle,
	)
	notifyLibraryManagers(ctx, pool, text)
}

// NotifyBookReturned DMs all library managers when a book is returned.
// holdsCount is the number of members currently waiting for this book.
func NotifyBookReturned(ctx context.Context, pool *pgxpool.Pool, bookTitle string, holdsCount int) {
	text := fmt.Sprintf(":books: *%s* has been returned.", bookTitle)
	if holdsCount > 0 {
		text += fmt.Sprintf(
			" There %s %d %s waiting — open Manage Library to notify the next in queue.",
			pluralIs(holdsCount), holdsCount, pluralize(holdsCount, "member", "members"),
		)
	}
	notifyLibraryManagers(ctx, pool, text)
}

// NotifyCheckoutApproved notifies the member that their checkout request was approved.
func NotifyCheckoutApproved(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle string, dueDate time.Time) {
	duePart := ""
	if !dueDate.IsZero() {
		duePart = " Due date: " + dueDate.Format("January 2, 2006") + "."
	}
	body := fmt.Sprintf(
		"Your checkout request for *%s* has been approved.%s Coordinate pickup with your library admin.",
		bookTitle, duePart,
	)
	if err := store.NotifyUser(ctx, pool, pkgEncKey, userID, "checkout_approved", "Checkout Approved", body, "", &Notifier{pool: pool}); err != nil {
		log.Printf("slack: NotifyCheckoutApproved for user %s: %v", userID, err)
	}
}

// NotifyCheckoutDenied notifies the member that their checkout request was not approved.
func NotifyCheckoutDenied(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle, notes string) {
	body := fmt.Sprintf("Your checkout request for *%s* was not approved.", bookTitle)
	if notes != "" {
		body += " Reason: " + notes
	}
	if err := store.NotifyUser(ctx, pool, pkgEncKey, userID, "checkout_denied", "Checkout Denied", body, "", &Notifier{pool: pool}); err != nil {
		log.Printf("slack: NotifyCheckoutDenied for user %s: %v", userID, err)
	}
}

// NotifyHoldAvailable notifies the member that a copy of the book they are waiting for is available.
func NotifyHoldAvailable(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle string) {
	body := fmt.Sprintf(
		"A copy of *%s* is now available for you. Contact your library admin to arrange pickup.",
		bookTitle,
	)
	if err := store.NotifyUser(ctx, pool, pkgEncKey, userID, "hold_available", "Hold Available", body, "", &Notifier{pool: pool}); err != nil {
		log.Printf("slack: NotifyHoldAvailable for user %s: %v", userID, err)
	}
}

// NotifyOverdue notifies the member that their book is overdue.
func NotifyOverdue(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle string, dueDate time.Time) {
	body := fmt.Sprintf(
		"Reminder: *%s* was due on %s. Please return it at your earliest convenience.",
		bookTitle, dueDate.Format("January 2, 2006"),
	)
	if err := store.NotifyUser(ctx, pool, pkgEncKey, userID, "library_overdue", "Overdue Reminder", body, "", &Notifier{pool: pool}); err != nil {
		log.Printf("slack: NotifyOverdue for user %s: %v", userID, err)
	}
}

// SendOverdueReminders checks for active checkouts past their due date and DMs the
// borrower if they have not been notified in the last 23 hours.
func SendOverdueReminders(ctx context.Context, pool *pgxpool.Pool) error {
	checkouts, err := store.ListOverdueUnnotifiedCheckouts(ctx, pool)
	if err != nil {
		return fmt.Errorf("list overdue checkouts: %w", err)
	}
	for _, co := range checkouts {
		if co.DueDate == nil {
			continue
		}
		NotifyOverdue(ctx, pool, co.UserID, co.BookTitle, *co.DueDate)
		if err := store.MarkCheckoutOverdueNotified(ctx, pool, co.ID); err != nil {
			log.Printf("slack: SendOverdueReminders: mark notified %s: %v", co.ID, err)
		}
	}
	return nil
}

func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
