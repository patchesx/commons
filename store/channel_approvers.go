package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SlackChannel struct {
	SlackChannelID string
	Name           string
	IsArchived     bool
	SyncedAt       time.Time
}

// UpsertSlackChannel inserts or updates a channel record. Called once per channel during sync.
func UpsertSlackChannel(ctx context.Context, pool *pgxpool.Pool, id, name string, isArchived bool) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO slack.channels (slack_channel_id, name, is_archived, synced_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (slack_channel_id) DO UPDATE
			SET name        = EXCLUDED.name,
			    is_archived = EXCLUDED.is_archived,
			    synced_at   = NOW()
	`, id, name, isArchived)
	return err
}

// ListSlackChannels returns all non-archived channels ordered by name.
func ListSlackChannels(ctx context.Context, pool *pgxpool.Pool) ([]SlackChannel, error) {
	rows, err := pool.Query(ctx, `
		SELECT slack_channel_id, name, is_archived, synced_at
		FROM slack.channels
		WHERE is_archived = FALSE
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []SlackChannel
	for rows.Next() {
		var ch SlackChannel
		if err := rows.Scan(&ch.SlackChannelID, &ch.Name, &ch.IsArchived, &ch.SyncedAt); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

// GetChannelApprovers returns all users designated as approvers for a given Slack channel.
func GetChannelApprovers(ctx context.Context, pool *pgxpool.Pool, slackChannelID string) ([]User, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM users u
		JOIN channel_approvers ca ON ca.user_id = u.id
		WHERE ca.slack_channel_id = $1
		ORDER BY u.display_name ASC
	`, slackChannelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// GetUserChannelApprovals returns the Slack channel IDs a user is designated to approve requests for.
func GetUserChannelApprovals(ctx context.Context, pool *pgxpool.Pool, userID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT slack_channel_id FROM channel_approvers WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RequestNotification records the DM sent to an approver for a channel request.
type RequestNotification struct {
	ID          string
	RequestID   string
	SlackUserID string
	DMChannelID string
	MessageTS   string
	CreatedAt   time.Time
}

// RecordRequestNotification saves the channel/timestamp of a DM sent to an approver.
func RecordRequestNotification(ctx context.Context, pool *pgxpool.Pool, requestID, slackUserID, dmChannelID, messageTS string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO slack.channel_request_notifications (request_id, slack_user_id, dm_channel_id, message_ts)
		VALUES ($1, $2, $3, $4)
	`, requestID, slackUserID, dmChannelID, messageTS)
	return err
}

// ListRequestNotifications returns all DM notification records for a given request.
func ListRequestNotifications(ctx context.Context, pool *pgxpool.Pool, requestID string) ([]RequestNotification, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, request_id, slack_user_id, dm_channel_id, message_ts, created_at
		FROM slack.channel_request_notifications
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestNotification
	for rows.Next() {
		var n RequestNotification
		if err := rows.Scan(&n.ID, &n.RequestID, &n.SlackUserID, &n.DMChannelID, &n.MessageTS, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetUserChannelApprovals replaces all channel approver assignments for a user.
// Pass an empty slice to remove all assignments.
func SetUserChannelApprovals(ctx context.Context, pool *pgxpool.Pool, userID string, channelIDs []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM channel_approvers WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, chID := range channelIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_approvers (user_id, slack_channel_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, userID, chID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
