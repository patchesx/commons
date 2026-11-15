package slack

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	PollPosting   = "posting"
	PollOpen      = "open"
	PollClosed    = "closed"
	PollCancelled = "cancelled"

	PollAudienceChannel  = "channel"
	PollAudienceEveryone = "everyone"

	PollMessagePending = "pending"
	PollMessagePosted  = "posted"
	PollMessageFailed  = "failed"
)

// Poll is an emoji straw poll. Options carry their vote counts once the poll has been tallied.
type Poll struct {
	ID             string
	CreatorUserID  string
	CreatorName    string
	CreatorSlackID string
	Question       string
	Audience       string
	ChannelID      string
	ClosesAt       time.Time
	Status         string
	CreatedAt      time.Time
	ClosedAt       *time.Time
	Options        []PollOption
	Voters         int // distinct members who voted
	Messages       int
	Posted         int
	Failed         int
}

// PollOption is one answer: an emoji and what it stands for.
type PollOption struct {
	ID       string
	Position int
	Emoji    string // shortcode, no colons
	Label    string
	Votes    int
}

// PollMessage is one posted copy of a poll: the channel message, or one member's DM.
type PollMessage struct {
	ID               string
	PollID           string
	RecipientUserID  string
	RecipientSlackID string
	ChannelID        string
	TS               string
	Status           string
	Error            string
}

// PollVote is one member's vote for one option.
type PollVote struct {
	OptionID    string
	SlackUserID string
	DisplayName string
}

// PollOptionInput is an option as entered by the creator.
type PollOptionInput struct {
	Emoji string
	Label string
}

// CreatePollInput is what the creator submits.
type CreatePollInput struct {
	CreatorUserID  string
	CreatorName    string
	CreatorSlackID string
	Question       string
	Audience       string
	ChannelID      string // required for the channel audience
	ClosesAt       time.Time
	Options        []PollOptionInput
}

// CreatePoll records the poll, its options, and the messages to send, in one transaction. A channel poll gets
// one pending message. An everyone poll gets one per member from broadcastRecipientQuery, snapshotted now.
func CreatePoll(ctx context.Context, pool *pgxpool.Pool, in CreatePollInput) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO slack.polls (creator_user_id, creator_name, creator_slack_id, question, audience, channel_id, closes_at)
		VALUES (NULLIF($1, '')::uuid, $2, $3, $4, $5, NULLIF($6, ''), $7)
		RETURNING id
	`, in.CreatorUserID, in.CreatorName, in.CreatorSlackID, in.Question, in.Audience, in.ChannelID, in.ClosesAt).Scan(&id); err != nil {
		return "", err
	}
	for i, o := range in.Options {
		if _, err := tx.Exec(ctx, `
			INSERT INTO slack.poll_options (poll_id, position, emoji, label) VALUES ($1, $2, $3, $4)
		`, id, i, o.Emoji, o.Label); err != nil {
			return "", err
		}
	}
	if in.Audience == PollAudienceChannel {
		_, err = tx.Exec(ctx, `INSERT INTO slack.poll_messages (poll_id, channel_id) VALUES ($1, $2)`, id, in.ChannelID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO slack.poll_messages (poll_id, recipient_user_id, recipient_slack_id)
			SELECT $1, q.id, q.external_id FROM (`+broadcastRecipientQuery+`) q
		`, id)
	}
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// ClaimPoll atomically marks a poll as being worked, so only one goroutine posts or closes it. status is the
// state the poll must be in: PollPosting to post it, PollOpen to close it. It returns false if the poll is in
// another state or already claimed.
func ClaimPoll(ctx context.Context, pool *pgxpool.Pool, id, status string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE slack.polls SET claimed_at = NOW()
		WHERE id = $1 AND status = $2 AND claimed_at IS NULL
	`, id, status)
	return tag.RowsAffected() == 1, err
}

// ReleasePoll clears a claim without changing state, for a goroutine that had to stop early.
func ReleasePoll(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE slack.polls SET claimed_at = NULL WHERE id = $1`, id)
	return err
}

// MarkPollOpen records that posting is finished and releases the claim.
func MarkPollOpen(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.polls SET status = 'open', claimed_at = NULL WHERE id = $1 AND status = 'posting'
	`, id)
	return err
}

// ClosePoll marks a tallied poll closed and releases the claim.
func ClosePoll(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.polls SET status = 'closed', closed_at = NOW(), claimed_at = NULL
		WHERE id = $1 AND status IN ('posting', 'open')
	`, id)
	return err
}

// CancelPoll stops a poll that has not closed. It returns false if it already closed or was cancelled.
func CancelPoll(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE slack.polls SET status = 'cancelled', closed_at = NOW(), claimed_at = NULL
		WHERE id = $1 AND status IN ('posting', 'open')
	`, id)
	return tag.RowsAffected() == 1, err
}

// ListDuePollIDs returns open polls whose closing time has passed, oldest deadline first.
func ListDuePollIDs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	return pollIDs(ctx, pool, `
		SELECT id FROM slack.polls WHERE status = 'open' AND closes_at <= NOW() AND claimed_at IS NULL ORDER BY closes_at
	`)
}

// ListUnfinishedPollIDs returns polls left posting or open by a restart, after clearing their claims because no
// goroutine survives a restart. Posting resumes for the first kind. The second is picked up by the scheduler.
func ListUnfinishedPollIDs(ctx context.Context, pool *pgxpool.Pool) (posting []string, err error) {
	if _, err := pool.Exec(ctx, `UPDATE slack.polls SET claimed_at = NULL WHERE status IN ('posting', 'open')`); err != nil {
		return nil, err
	}
	return pollIDs(ctx, pool, `SELECT id FROM slack.polls WHERE status = 'posting' ORDER BY created_at`)
}

func pollIDs(ctx context.Context, pool *pgxpool.Pool, query string) ([]string, error) {
	rows, err := pool.Query(ctx, query)
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

// ListPendingPollMessages returns the messages still to be posted, in a stable order.
func ListPendingPollMessages(ctx context.Context, pool *pgxpool.Pool, pollID string) ([]PollMessage, error) {
	return listPollMessages(ctx, pool, pollID, `AND status = 'pending'`)
}

// ListPostedPollMessages returns the messages that went out, for tallying and for updating at close.
func ListPostedPollMessages(ctx context.Context, pool *pgxpool.Pool, pollID string) ([]PollMessage, error) {
	return listPollMessages(ctx, pool, pollID, `AND status = 'posted'`)
}

func listPollMessages(ctx context.Context, pool *pgxpool.Pool, pollID, extra string) ([]PollMessage, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, poll_id, COALESCE(recipient_user_id::text, ''), COALESCE(recipient_slack_id, ''),
		       COALESCE(channel_id, ''), COALESCE(ts, ''), status, COALESCE(error, '')
		FROM slack.poll_messages
		WHERE poll_id = $1 `+extra+`
		ORDER BY id
	`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PollMessage
	for rows.Next() {
		var m PollMessage
		if err := rows.Scan(&m.ID, &m.PollID, &m.RecipientUserID, &m.RecipientSlackID, &m.ChannelID, &m.TS, &m.Status, &m.Error); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecordPollMessagePosted stores where a message landed.
func RecordPollMessagePosted(ctx context.Context, pool *pgxpool.Pool, messageID, channelID, ts string) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.poll_messages SET status = 'posted', channel_id = $2, ts = $3, error = NULL WHERE id = $1
	`, messageID, channelID, ts)
	return err
}

// MarkPollMessageFailed records that a message could not be posted.
func MarkPollMessageFailed(ctx context.Context, pool *pgxpool.Pool, messageID, errMsg string) error {
	_, err := pool.Exec(ctx, `UPDATE slack.poll_messages SET status = 'failed', error = $2 WHERE id = $1`, messageID, errMsg)
	return err
}

// SavePollVotes replaces a poll's votes, so tallying twice gives the same result.
func SavePollVotes(ctx context.Context, pool *pgxpool.Pool, pollID string, votes []PollVote) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM slack.poll_votes WHERE poll_id = $1`, pollID); err != nil {
		return err
	}
	for _, v := range votes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO slack.poll_votes (poll_id, option_id, slack_user_id, display_name)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING
		`, pollID, v.OptionID, v.SlackUserID, v.DisplayName); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListPollVotes returns a poll's votes, grouped by option order then voter name.
func ListPollVotes(ctx context.Context, pool *pgxpool.Pool, pollID string) ([]PollVote, error) {
	rows, err := pool.Query(ctx, `
		SELECT v.option_id, v.slack_user_id, v.display_name
		FROM slack.poll_votes v
		JOIN slack.poll_options o ON o.id = v.option_id
		WHERE v.poll_id = $1
		ORDER BY o.position, v.display_name, v.slack_user_id
	`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PollVote
	for rows.Next() {
		var v PollVote
		if err := rows.Scan(&v.OptionID, &v.SlackUserID, &v.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const pollSelect = `
	SELECT p.id, COALESCE(p.creator_user_id::text, ''), p.creator_name, p.creator_slack_id, p.question, p.audience,
	       COALESCE(p.channel_id, ''), p.closes_at, p.status, p.created_at, p.closed_at,
	       (SELECT COUNT(DISTINCT slack_user_id) FROM slack.poll_votes WHERE poll_id = p.id),
	       (SELECT COUNT(*) FROM slack.poll_messages WHERE poll_id = p.id),
	       (SELECT COUNT(*) FROM slack.poll_messages WHERE poll_id = p.id AND status = 'posted'),
	       (SELECT COUNT(*) FROM slack.poll_messages WHERE poll_id = p.id AND status = 'failed')
	FROM slack.polls p
`

func scanPoll(row pgx.Row) (*Poll, error) {
	var p Poll
	if err := row.Scan(&p.ID, &p.CreatorUserID, &p.CreatorName, &p.CreatorSlackID, &p.Question, &p.Audience,
		&p.ChannelID, &p.ClosesAt, &p.Status, &p.CreatedAt, &p.ClosedAt,
		&p.Voters, &p.Messages, &p.Posted, &p.Failed); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetPoll loads one poll with its options and their vote counts, or pgx.ErrNoRows.
func GetPoll(ctx context.Context, pool *pgxpool.Pool, id string) (*Poll, error) {
	p, err := scanPoll(pool.QueryRow(ctx, pollSelect+`WHERE p.id = $1`, id))
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT o.id, o.position, o.emoji, o.label,
		       (SELECT COUNT(*) FROM slack.poll_votes v WHERE v.option_id = o.id)
		FROM slack.poll_options o
		WHERE o.poll_id = $1
		ORDER BY o.position
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o PollOption
		if err := rows.Scan(&o.ID, &o.Position, &o.Emoji, &o.Label, &o.Votes); err != nil {
			return nil, err
		}
		p.Options = append(p.Options, o)
	}
	return p, rows.Err()
}

// ListPolls returns recent polls, newest first. Options are not loaded.
func ListPolls(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Poll, error) {
	rows, err := pool.Query(ctx, pollSelect+`ORDER BY p.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Poll
	for rows.Next() {
		p, err := scanPoll(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// PollStatus returns a poll's current status, or pgx.ErrNoRows.
func PollStatus(ctx context.Context, pool *pgxpool.Pool, id string) (string, error) {
	var status string
	err := pool.QueryRow(ctx, `SELECT status FROM slack.polls WHERE id = $1`, id).Scan(&status)
	return status, err
}

// SlackDisplayNames maps Slack user IDs to the names of the members they belong to. IDs the app has no member
// for are left out.
func SlackDisplayNames(ctx context.Context, pool *pgxpool.Pool, slackIDs []string) (map[string]string, error) {
	names := map[string]string{}
	if len(slackIDs) == 0 {
		return names, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT ui.external_id, u.display_name
		FROM user_identities ui
		JOIN users u ON u.id = ui.user_id
		WHERE ui.provider = 'slack' AND ui.external_id = ANY($1)
	`, slackIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}
