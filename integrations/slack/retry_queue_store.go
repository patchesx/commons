package slack

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RetryMessage is a queued Slack message awaiting retry after a transient send
// failure (rate limit or 5xx).
type RetryMessage struct {
	ID            string
	Destination   string
	IsDM          bool
	Text          string
	Blocks        []byte // raw JSON Block Kit blocks; nil if none
	AttemptCount  int
	MaxAttempts   int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// EnqueueRetryMessage persists a message for later retry. next_attempt_at
// defaults to NOW() so it is eligible for delivery immediately.
func EnqueueRetryMessage(ctx context.Context, pool *pgxpool.Pool, m *RetryMessage) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO slack.retry_queue (destination, is_dm, text, blocks, max_attempts)
		VALUES ($1, $2, $3, $4, $5)
	`, m.Destination, m.IsDM, m.Text, m.Blocks, m.MaxAttempts)
	return err
}

// ListDueRetryMessages returns up to limit messages whose next attempt has come
// due and that have not exhausted their attempts, oldest first.
func ListDueRetryMessages(ctx context.Context, pool *pgxpool.Pool, limit int) ([]RetryMessage, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, destination, is_dm, text, blocks, attempt_count, max_attempts,
		       next_attempt_at, COALESCE(last_error, ''), created_at
		FROM slack.retry_queue
		WHERE next_attempt_at <= NOW() AND attempt_count < max_attempts
		ORDER BY next_attempt_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RetryMessage
	for rows.Next() {
		var m RetryMessage
		if err := rows.Scan(&m.ID, &m.Destination, &m.IsDM, &m.Text, &m.Blocks,
			&m.AttemptCount, &m.MaxAttempts, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteRetryMessage removes a message after successful delivery.
func DeleteRetryMessage(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM slack.retry_queue WHERE id = $1`, id)
	return err
}

// ListAllRetryMessages returns all queued messages (including exhausted
// dead-lettered ones), newest first. Used by the admin UI.
func ListAllRetryMessages(ctx context.Context, pool *pgxpool.Pool, limit int) ([]RetryMessage, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, destination, is_dm, text, blocks, attempt_count, max_attempts,
		       next_attempt_at, COALESCE(last_error, ''), created_at
		FROM slack.retry_queue
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RetryMessage
	for rows.Next() {
		var m RetryMessage
		if err := rows.Scan(&m.ID, &m.Destination, &m.IsDM, &m.Text, &m.Blocks,
			&m.AttemptCount, &m.MaxAttempts, &m.NextAttemptAt, &m.LastError, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResetRetryMessage resets a message for immediate manual retry: clears the
// attempt count and last error, and makes it due now. The drainer picks it up
// on its next tick (still respecting the per-destination throttle).
func ResetRetryMessage(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.retry_queue
		SET attempt_count = 0,
		    next_attempt_at = NOW(),
		    last_error = NULL
		WHERE id = $1
	`, id)
	return err
}

// MarkRetryFailed records a failed attempt, bumps the attempt count, and
// schedules the next attempt. When attempts are exhausted the row remains in
// the table as a dead-letter for inspection.
func MarkRetryFailed(ctx context.Context, pool *pgxpool.Pool, id string, nextAttemptAt time.Time, lastError string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.retry_queue
		SET attempt_count = attempt_count + 1,
		    next_attempt_at = $1,
		    last_error = $2
		WHERE id = $3
	`, nextAttemptAt, lastError, id)
	return err
}
