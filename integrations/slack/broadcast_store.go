package slack

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	BroadcastScheduled = "scheduled"
	BroadcastSending   = "sending"
	BroadcastDone      = "done"
	BroadcastCancelled = "cancelled"

	RecipientPending = "pending"
	RecipientSent    = "sent"
	RecipientFailed  = "failed"
)

// Broadcast is one message an admin sent to every active member. Counts are derived from the recipient rows.
type Broadcast struct {
	ID           string
	AuthorUserID string
	AuthorName   string
	Body         string
	Status       string
	CreatedAt    time.Time
	CompletedAt  *time.Time
	ScheduledFor *time.Time
	Total        int
	Sent         int
	Failed       int
	Pending      int
}

// BroadcastRecipient is one member's delivery result for a broadcast.
type BroadcastRecipient struct {
	ID          string
	UserID      string
	DisplayName string
	SlackID     string
	Status      string
	Error       string
	SentAt      *time.Time
}

const broadcastSelect = `
	SELECT b.id, COALESCE(b.author_user_id::text, ''), b.author_name, b.body, b.status, b.created_at, b.completed_at, b.scheduled_for,
	       COUNT(r.id),
	       COUNT(r.id) FILTER (WHERE r.status = 'sent'),
	       COUNT(r.id) FILTER (WHERE r.status = 'failed'),
	       COUNT(r.id) FILTER (WHERE r.status = 'pending')
	FROM slack.broadcasts b
	LEFT JOIN slack.broadcast_recipients r ON r.broadcast_id = b.id
`

func scanBroadcast(row pgx.Row) (*Broadcast, error) {
	var b Broadcast
	if err := row.Scan(&b.ID, &b.AuthorUserID, &b.AuthorName, &b.Body, &b.Status, &b.CreatedAt, &b.CompletedAt, &b.ScheduledFor,
		&b.Total, &b.Sent, &b.Failed, &b.Pending); err != nil {
		return nil, err
	}
	return &b, nil
}

// CountBroadcastRecipients is how many members a broadcast sent now would reach.
func CountBroadcastRecipients(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM (`+broadcastRecipientQuery+`) q`).Scan(&n)
	return n, err
}

// broadcastRecipientQuery selects the members a broadcast reaches: non-bot users whose Slack identity is
// active. Deactivated and invited accounts are left out.
const broadcastRecipientQuery = `
	SELECT u.id, u.display_name, ui.external_id
	FROM users u
	JOIN user_identities ui ON ui.user_id = u.id AND ui.provider = 'slack'
	WHERE NOT u.bot AND ui.platform_status = 'active' AND ui.external_id <> ''
`

// snapshotBroadcastRecipients fixes who a broadcast reaches, as pending rows.
func snapshotBroadcastRecipients(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO slack.broadcast_recipients (broadcast_id, user_id, display_name, slack_id)
		SELECT $1, q.id, q.display_name, q.external_id FROM (`+broadcastRecipientQuery+`) q
	`, id)
	return err
}

// CreateBroadcast records the message. With a nil scheduledFor it starts sending, and its recipients are
// snapshotted as pending in the same transaction. Otherwise it waits as 'scheduled' with no recipients until
// ActivateDueBroadcasts finds it due.
func CreateBroadcast(ctx context.Context, pool *pgxpool.Pool, authorUserID, authorName, body string, scheduledFor *time.Time) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	status := BroadcastSending
	if scheduledFor != nil {
		status = BroadcastScheduled
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO slack.broadcasts (author_user_id, author_name, body, status, scheduled_for)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5)
		RETURNING id
	`, authorUserID, authorName, body, status, scheduledFor).Scan(&id); err != nil {
		return "", err
	}
	if scheduledFor == nil {
		if err := snapshotBroadcastRecipients(ctx, tx, id); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

// ActivateDueBroadcasts moves scheduled broadcasts whose time has come to 'sending' and snapshots their
// recipients, in one transaction, and returns their IDs. SKIP LOCKED keeps two callers from taking the same one.
func ActivateDueBroadcasts(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		UPDATE slack.broadcasts SET status = 'sending'
		WHERE id IN (
			SELECT id FROM slack.broadcasts
			WHERE status = 'scheduled' AND scheduled_for <= NOW()
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id
	`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := snapshotBroadcastRecipients(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit(ctx)
}

// CancelScheduledBroadcast cancels a broadcast that has not started sending. It returns false if it already
// started, finished or was cancelled.
func CancelScheduledBroadcast(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	tag, err := pool.Exec(ctx, `UPDATE slack.broadcasts SET status = 'cancelled' WHERE id = $1 AND status = 'scheduled'`, id)
	return tag.RowsAffected() == 1, err
}

// ClaimBroadcast atomically marks a sending broadcast as being worked, so only one goroutine runs it. It
// returns false if the broadcast is not sending or another goroutine already holds it.
func ClaimBroadcast(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE slack.broadcasts SET claimed_at = NOW()
		WHERE id = $1 AND status = 'sending' AND claimed_at IS NULL
	`, id)
	return tag.RowsAffected() == 1, err
}

// ListPendingBroadcastRecipients returns the recipients still to be sent to, in a stable order.
func ListPendingBroadcastRecipients(ctx context.Context, pool *pgxpool.Pool, broadcastID string) ([]BroadcastRecipient, error) {
	return listBroadcastRecipients(ctx, pool, broadcastID, `AND status = 'pending'`)
}

// ListBroadcastRecipients returns every recipient of a broadcast, failures first.
func ListBroadcastRecipients(ctx context.Context, pool *pgxpool.Pool, broadcastID string) ([]BroadcastRecipient, error) {
	return listBroadcastRecipients(ctx, pool, broadcastID, ``)
}

func listBroadcastRecipients(ctx context.Context, pool *pgxpool.Pool, broadcastID, extra string) ([]BroadcastRecipient, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, COALESCE(user_id::text, ''), display_name, slack_id, status, COALESCE(error, ''), sent_at
		FROM slack.broadcast_recipients
		WHERE broadcast_id = $1 `+extra+`
		ORDER BY CASE status WHEN 'failed' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END, display_name, id
	`, broadcastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BroadcastRecipient
	for rows.Next() {
		var r BroadcastRecipient
		if err := rows.Scan(&r.ID, &r.UserID, &r.DisplayName, &r.SlackID, &r.Status, &r.Error, &r.SentAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkBroadcastRecipient records one delivery result. errMsg is empty on success.
func MarkBroadcastRecipient(ctx context.Context, pool *pgxpool.Pool, recipientID, status, errMsg string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.broadcast_recipients
		SET status = $2, error = NULLIF($3, ''), sent_at = CASE WHEN $2 = 'sent' THEN NOW() END
		WHERE id = $1
	`, recipientID, status, errMsg)
	return err
}

// CompleteBroadcast marks a broadcast done and releases its claim.
func CompleteBroadcast(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.broadcasts SET status = 'done', completed_at = NOW(), claimed_at = NULL WHERE id = $1
	`, id)
	return err
}

// ResetFailedBroadcastRecipients puts failed recipients back to pending and reopens the broadcast. It returns
// the number reset.
func ResetFailedBroadcastRecipients(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE slack.broadcast_recipients SET status = 'pending', error = NULL
		WHERE broadcast_id = $1 AND status = 'failed'
	`, id)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE slack.broadcasts SET status = 'sending', completed_at = NULL WHERE id = $1 AND claimed_at IS NULL
		`, id); err != nil {
			return 0, err
		}
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

// ListSendingBroadcastIDs returns broadcasts left unfinished, for resuming after a restart. Any claim is
// cleared first because no goroutine survives a restart.
func ListSendingBroadcastIDs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	if _, err := pool.Exec(ctx, `UPDATE slack.broadcasts SET claimed_at = NULL WHERE status = 'sending'`); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT id FROM slack.broadcasts WHERE status = 'sending' ORDER BY created_at`)
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

// GetBroadcast loads one broadcast with its counts, or pgx.ErrNoRows.
func GetBroadcast(ctx context.Context, pool *pgxpool.Pool, id string) (*Broadcast, error) {
	return scanBroadcast(pool.QueryRow(ctx, broadcastSelect+`WHERE b.id = $1 GROUP BY b.id`, id))
}

// ListBroadcasts returns recent broadcasts, newest first.
func ListBroadcasts(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Broadcast, error) {
	rows, err := pool.Query(ctx, broadcastSelect+`GROUP BY b.id ORDER BY b.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Broadcast
	for rows.Next() {
		b, err := scanBroadcast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ReleaseBroadcast clears a claim without finishing, for a goroutine that had to stop early.
func ReleaseBroadcast(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE slack.broadcasts SET claimed_at = NULL WHERE id = $1`, id)
	return err
}
