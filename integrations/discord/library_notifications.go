package discord

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/permissions"
	"commons/platform"
	"commons/store"
)

// notifyLibraryManagers DMs every user with library.manage permission who has a
// linked Discord identity.
func notifyLibraryManagers(ctx context.Context, pool *pgxpool.Pool, text string) {
	managers, err := store.ListUsersWithPermission(ctx, pool, permissions.LibraryManage)
	if err != nil {
		log.Printf("discord: notifyLibraryManagers: list users: %v", err)
		return
	}
	n := &Notifier{pool: pool}
	for _, u := range managers {
		if err := n.NotifyUser(ctx, u.ID, platform.Message{Text: text}); err != nil {
			log.Printf("discord: notifyLibraryManagers: notify %s: %v", u.ID, err)
		}
	}
}

// NotifyNewCheckoutRequest DMs all library managers when a member requests a checkout.
func NotifyNewCheckoutRequest(ctx context.Context, pool *pgxpool.Pool, memberName, bookTitle string) {
	text := fmt.Sprintf(
		"New checkout request: %s wants to borrow %s. Open Manage Library to approve or deny.",
		memberName, bookTitle,
	)
	notifyLibraryManagers(ctx, pool, text)
}

// NotifyBookReturned DMs all library managers when a book is returned.
func NotifyBookReturned(ctx context.Context, pool *pgxpool.Pool, bookTitle string, holdsCount int) {
	text := fmt.Sprintf("%s has been returned.", bookTitle)
	if holdsCount > 0 {
		if holdsCount == 1 {
			text += " There is 1 member waiting — open Manage Library to notify them."
		} else {
			text += fmt.Sprintf(" There are %d members waiting — open Manage Library to notify the next in queue.", holdsCount)
		}
	}
	notifyLibraryManagers(ctx, pool, text)
}

// NotifyCheckoutApproved notifies the member that their checkout request was approved.
func NotifyCheckoutApproved(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle string, dueDate time.Time) {
	duePart := ""
	if !dueDate.IsZero() {
		duePart = " Due date: " + dueDate.Format("January 2, 2006") + "."
	}
	text := fmt.Sprintf(
		"Your checkout request for %s has been approved.%s Coordinate pickup with your library admin.",
		bookTitle, duePart,
	)
	n := &Notifier{pool: pool}
	if err := n.NotifyUser(ctx, userID, platform.Message{Text: text}); err != nil {
		log.Printf("discord: NotifyCheckoutApproved for user %s: %v", userID, err)
	}
}

// NotifyCheckoutDenied notifies the member that their checkout request was not approved.
func NotifyCheckoutDenied(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle, notes string) {
	text := fmt.Sprintf("Your checkout request for %s was not approved.", bookTitle)
	if notes != "" {
		text += " Reason: " + notes
	}
	n := &Notifier{pool: pool}
	if err := n.NotifyUser(ctx, userID, platform.Message{Text: text}); err != nil {
		log.Printf("discord: NotifyCheckoutDenied for user %s: %v", userID, err)
	}
}

// NotifyHoldAvailable notifies the member that a copy of the book they are waiting for is available.
func NotifyHoldAvailable(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle string) {
	text := fmt.Sprintf(
		"A copy of %s is now available for you. Contact your library admin to arrange pickup.",
		bookTitle,
	)
	n := &Notifier{pool: pool}
	if err := n.NotifyUser(ctx, userID, platform.Message{Text: text}); err != nil {
		log.Printf("discord: NotifyHoldAvailable for user %s: %v", userID, err)
	}
}

// NotifyOverdue notifies the member that their book is overdue.
func NotifyOverdue(ctx context.Context, pool *pgxpool.Pool, userID, bookTitle string, dueDate time.Time) {
	text := fmt.Sprintf(
		"Reminder: %s was due on %s. Please return it at your earliest convenience.",
		bookTitle, dueDate.Format("January 2, 2006"),
	)
	n := &Notifier{pool: pool}
	if err := n.NotifyUser(ctx, userID, platform.Message{Text: text}); err != nil {
		log.Printf("discord: NotifyOverdue for user %s: %v", userID, err)
	}
}
