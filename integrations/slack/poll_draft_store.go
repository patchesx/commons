package slack

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	PollStepQuestion = "question"
	PollStepOptions  = "options"
	PollStepAudience = "audience"
	PollStepDuration = "duration"
	PollStepConfirm  = "confirm"
)

// PollDraft is a poll being written in Slack by one author, one answer per step.
type PollDraft struct {
	ID            string
	AuthorUserID  string
	AuthorSlackID string
	AuthorName    string
	Step          string
	Question      string
	Options       []PollOptionInput
	Audience      string
	ChannelID     string
	DurationSecs  int        // how long to stay open once started, or 0
	ClosesAt      *time.Time // an exact closing time, or nil
	ExpiresAt     time.Time
}

const pollDraftSelect = `
	SELECT id, COALESCE(author_user_id::text, ''), author_slack_id, author_name, step, question, options,
	       audience, channel_id, duration_secs, closes_at, expires_at
	FROM slack.poll_drafts
`

func scanPollDraft(row pgx.Row) (*PollDraft, error) {
	var d PollDraft
	var options []byte
	if err := row.Scan(&d.ID, &d.AuthorUserID, &d.AuthorSlackID, &d.AuthorName, &d.Step, &d.Question, &options,
		&d.Audience, &d.ChannelID, &d.DurationSecs, &d.ClosesAt, &d.ExpiresAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(options, &d.Options); err != nil {
		return nil, err
	}
	return &d, nil
}

// StartPollDraft begins a draft at the question step, replacing any draft the author already had.
func StartPollDraft(ctx context.Context, pool *pgxpool.Pool, authorUserID, authorSlackID, authorName string, expiresAt time.Time) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO slack.poll_drafts (author_user_id, author_slack_id, author_name, expires_at)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4)
		ON CONFLICT (author_slack_id) DO UPDATE
		SET author_user_id = EXCLUDED.author_user_id, author_name = EXCLUDED.author_name, step = 'question',
		    question = '', options = '[]', audience = '', channel_id = '', duration_secs = 0, closes_at = NULL,
		    expires_at = EXCLUDED.expires_at, created_at = NOW()
		RETURNING id
	`, authorUserID, authorSlackID, authorName, expiresAt).Scan(&id)
	return id, err
}

// GetPollDraftBySlackID returns an author's draft, expired or not, or pgx.ErrNoRows.
func GetPollDraftBySlackID(ctx context.Context, pool *pgxpool.Pool, slackID string) (*PollDraft, error) {
	return scanPollDraft(pool.QueryRow(ctx, pollDraftSelect+`WHERE author_slack_id = $1`, slackID))
}

// GetPollDraft returns a draft by ID, or pgx.ErrNoRows.
func GetPollDraft(ctx context.Context, pool *pgxpool.Pool, id string) (*PollDraft, error) {
	return scanPollDraft(pool.QueryRow(ctx, pollDraftSelect+`WHERE id = $1`, id))
}

// UpdatePollDraft changes a draft under a row lock, so two messages sent at once (several option messages in a
// row, say) both land instead of the last one overwriting the other. fn edits the draft; if it returns an error
// nothing is saved and the error is returned. The saved draft is returned.
func UpdatePollDraft(ctx context.Context, pool *pgxpool.Pool, id string, fn func(*PollDraft) error) (*PollDraft, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	d, err := scanPollDraft(tx.QueryRow(ctx, pollDraftSelect+`WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if err := fn(d); err != nil {
		return nil, err
	}
	options, err := json.Marshal(d.Options)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE slack.poll_drafts
		SET step = $2, question = $3, options = $4, audience = $5, channel_id = $6, duration_secs = $7,
		    closes_at = $8, expires_at = $9
		WHERE id = $1
	`, id, d.Step, d.Question, options, d.Audience, d.ChannelID, d.DurationSecs, d.ClosesAt, d.ExpiresAt); err != nil {
		return nil, err
	}
	return d, tx.Commit(ctx)
}

// DeletePollDraft removes a draft.
func DeletePollDraft(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM slack.poll_drafts WHERE id = $1`, id)
	return err
}

// DeletePollDraftBySlackID removes whatever poll draft an author has.
func DeletePollDraftBySlackID(ctx context.Context, pool *pgxpool.Pool, slackID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM slack.poll_drafts WHERE author_slack_id = $1`, slackID)
	return err
}

// DeleteBroadcastDraftBySlackID removes whatever broadcast draft an author has.
func DeleteBroadcastDraftBySlackID(ctx context.Context, pool *pgxpool.Pool, slackID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM slack.broadcast_drafts WHERE author_slack_id = $1`, slackID)
	return err
}
