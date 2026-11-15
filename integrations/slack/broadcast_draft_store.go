package slack

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DraftAwaiting = "awaiting"
	DraftReady    = "ready"
)

// BroadcastDraft is a broadcast being written in Slack by one author.
type BroadcastDraft struct {
	ID            string
	AuthorUserID  string
	AuthorSlackID string
	AuthorName    string
	State         string
	Body          string
	ExpiresAt     time.Time
}

const draftSelect = `
	SELECT id, COALESCE(author_user_id::text, ''), author_slack_id, author_name, state, COALESCE(body, ''), expires_at
	FROM slack.broadcast_drafts
`

func scanDraft(row pgx.Row) (*BroadcastDraft, error) {
	var d BroadcastDraft
	if err := row.Scan(&d.ID, &d.AuthorUserID, &d.AuthorSlackID, &d.AuthorName, &d.State, &d.Body, &d.ExpiresAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// StartBroadcastDraft begins a draft awaiting the author's message, replacing any draft they already had.
func StartBroadcastDraft(ctx context.Context, pool *pgxpool.Pool, authorUserID, authorSlackID, authorName string, expiresAt time.Time) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO slack.broadcast_drafts (author_user_id, author_slack_id, author_name, expires_at)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4)
		ON CONFLICT (author_slack_id) DO UPDATE
		SET author_user_id = EXCLUDED.author_user_id, author_name = EXCLUDED.author_name,
		    state = 'awaiting', body = NULL, expires_at = EXCLUDED.expires_at, created_at = NOW()
		RETURNING id
	`, authorUserID, authorSlackID, authorName, expiresAt).Scan(&id)
	return id, err
}

// GetBroadcastDraftBySlackID returns an author's draft, expired or not, or pgx.ErrNoRows.
func GetBroadcastDraftBySlackID(ctx context.Context, pool *pgxpool.Pool, slackID string) (*BroadcastDraft, error) {
	return scanDraft(pool.QueryRow(ctx, draftSelect+`WHERE author_slack_id = $1`, slackID))
}

// GetBroadcastDraft returns a draft by ID, or pgx.ErrNoRows.
func GetBroadcastDraft(ctx context.Context, pool *pgxpool.Pool, id string) (*BroadcastDraft, error) {
	return scanDraft(pool.QueryRow(ctx, draftSelect+`WHERE id = $1`, id))
}

// SetBroadcastDraftBody records the author's message, makes the draft ready, and restarts its expiry.
func SetBroadcastDraftBody(ctx context.Context, pool *pgxpool.Pool, id, body string, expiresAt time.Time) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.broadcast_drafts SET state = 'ready', body = $2, expires_at = $3 WHERE id = $1
	`, id, body, expiresAt)
	return err
}

// DeleteBroadcastDraft removes a draft.
func DeleteBroadcastDraft(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM slack.broadcast_drafts WHERE id = $1`, id)
	return err
}
