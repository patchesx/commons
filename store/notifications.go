package store

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
)

// Notification is a web inbox notification for a user.
type Notification struct {
	ID        string
	UserID    string
	Type      string
	Title     string
	Body      *string
	ActionURL *string
	Read      bool
	CreatedAt time.Time
}

// CreateNotification inserts a new notification row.
func CreateNotification(ctx context.Context, pool *pgxpool.Pool, userID, notifType, title string, body, actionURL *string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO notifications (user_id, type, title, body, action_url)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, notifType, title, body, actionURL)
	return err
}

// ListNotifications returns notifications for a user, newest first.
// When unreadOnly is true, only unread notifications are returned.
func ListNotifications(ctx context.Context, pool *pgxpool.Pool, userID string, unreadOnly bool, limit, offset int) ([]Notification, error) {
	query := `
		SELECT id, user_id, type, title, body, action_url, read, created_at
		FROM notifications
		WHERE user_id = $1`
	if unreadOnly {
		query += ` AND read = FALSE`
	}
	query += ` ORDER BY created_at DESC LIMIT $2 OFFSET $3`

	rows, err := pool.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notifs []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body, &n.ActionURL, &n.Read, &n.CreatedAt); err != nil {
			return nil, err
		}
		notifs = append(notifs, n)
	}
	return notifs, rows.Err()
}

// CountUnread returns the count of unread notifications for a user.
func CountUnread(ctx context.Context, pool *pgxpool.Pool, userID string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND read = FALSE
	`, userID).Scan(&count)
	return count, err
}

// MarkRead marks a single notification as read. The userID check prevents cross-user access.
func MarkRead(ctx context.Context, pool *pgxpool.Pool, notificationID, userID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE notifications SET read = TRUE WHERE id = $1 AND user_id = $2
	`, notificationID, userID)
	return err
}

// MarkAllRead marks all notifications as read for a user.
func MarkAllRead(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE notifications SET read = TRUE WHERE user_id = $1 AND read = FALSE
	`, userID)
	return err
}

// NotifyUser creates a web notification row. If routing is web_and_chat and the user
// has a chat identity, it also triggers a DM via the provided Notifier.
// Callers pass a Notifier (may be nil if chat is not configured).
// If body is empty, title is used as the DM text.
func NotifyUser(ctx context.Context, pool *pgxpool.Pool, encKey []byte,
	userID, notifType, title, body, actionURL string,
	notifier platform.Notifier,
) error {
	var bodyPtr *string
	if body != "" {
		bodyPtr = &body
	}
	var urlPtr *string
	if actionURL != "" {
		urlPtr = &actionURL
	}
	if err := CreateNotification(ctx, pool, userID, notifType, title, bodyPtr, urlPtr); err != nil {
		return fmt.Errorf("create notification: %w", err)
	}

	if notifier == nil {
		return nil
	}
	routing, _ := GetServiceConfig(ctx, pool, "notifications", "routing", encKey)
	if routing == "" {
		routing = "web_and_chat"
	}
	if routing != "web_and_chat" {
		return nil
	}

	dmText := body
	if dmText == "" {
		dmText = title
	}
	if err := notifier.NotifyUser(ctx, userID, platform.Message{Text: dmText}); err != nil {
		log.Printf("store: NotifyUser: chat DM for user %s: %v", userID, err)
		// Don't propagate — web notification already persisted.
	}
	return nil
}
