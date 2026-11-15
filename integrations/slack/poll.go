package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/permissions"
	"commons/store"
	"commons/util"
)

const (
	ActionPollNew    = "poll_new"
	ActionPollClose  = "poll_close"
	ActionPollCancel = "poll_cancel"

	// pollPostAttempts is how many times a poll DM is tried when Slack answers with a transient error.
	pollPostAttempts = 3
)

// canCreatePoll reports whether a member holds poll.create.
func canCreatePoll(ctx context.Context, pool *pgxpool.Pool, userID string) bool {
	perms, err := store.GetUserPermissions(ctx, pool, userID)
	if err != nil {
		log.Printf("slack/poll: load permissions for %s: %v", userID, err)
		return false
	}
	return hasPermission(perms, permissions.PollCreate)
}

// pollLocation is the organization's timezone, which poll times fall back to when Slack can't localize them.
func pollLocation(ctx context.Context) *time.Location {
	if pkgPool == nil {
		return util.FallbackLocation()
	}
	loc, err := time.LoadLocation(util.DefaultTimezone(ctx, pkgPool, pkgEncKey))
	if err != nil {
		return util.FallbackLocation()
	}
	return loc
}

// modalErrors answers a view submission with validation errors, one per block ID.
func modalErrors(w http.ResponseWriter, errs map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{"response_action": "errors", "errors": errs})
}

// StartPollPosting posts a poll in the background. Safe to call more than once for the same poll.
func StartPollPosting(pool *pgxpool.Pool, id string) {
	go runPollPosting(context.Background(), pool, id)
}

// ResumeInterruptedPolls restarts polls whose posting was cut off by a restart. Each continues over its pending
// messages only, so nobody receives the poll twice. Called once at startup.
func ResumeInterruptedPolls(ctx context.Context, pool *pgxpool.Pool) {
	ids, err := ListUnfinishedPollIDs(ctx, pool)
	if err != nil {
		log.Printf("slack/poll: list unfinished polls: %v", err)
		return
	}
	for _, id := range ids {
		log.Printf("slack/poll: resuming posting of %s", id)
		StartPollPosting(pool, id)
	}
}

func runPollPosting(ctx context.Context, pool *pgxpool.Pool, id string) {
	claimed, err := ClaimPoll(ctx, pool, id, PollPosting)
	if err != nil {
		log.Printf("slack/poll: claim %s: %v", id, err)
		return
	}
	if !claimed {
		return
	}
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/poll: %s: bot_token not configured", id)
		ReleasePoll(ctx, pool, id)
		return
	}
	p, err := GetPoll(ctx, pool, id)
	if err != nil {
		log.Printf("slack/poll: load %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return
	}
	pending, err := ListPendingPollMessages(ctx, pool, id)
	if err != nil {
		log.Printf("slack/poll: list messages for %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return
	}
	text, blocks := pollMessage(p, pollLocation(ctx))
	for i, m := range pending {
		if i > 0 {
			time.Sleep(broadcastInterval)
		}
		// The creator may cancel while members are still being sent the poll.
		if status, err := PollStatus(ctx, pool, id); err == nil && status != PollPosting {
			refreshPollMessages(ctx, pool, client, id)
			return
		}
		postPollMessage(ctx, pool, client, p, m, text, blocks)
	}
	finishPollPosting(ctx, pool, client, id)
}

// postPollMessage sends one copy of the poll and records where it landed, or why it didn't.
func postPollMessage(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, p *Poll, m PollMessage, text string, blocks []slacklib.Block) {
	var channelID, ts string
	var err error
	if p.Audience == PollAudienceChannel {
		joinChannelBestEffort(ctx, client, m.ChannelID)
		channelID, ts, err = postChannelBlocks(ctx, client, m.ChannelID, text, blocks)
	} else {
		for attempt := 1; attempt <= pollPostAttempts; attempt++ {
			channelID, ts, err = sendDM(ctx, client, m.RecipientSlackID, text, blocks...)
			if err == nil || !isRetryableSendError(err) {
				break
			}
		}
	}
	if err != nil {
		log.Printf("slack/poll: post %s to %s%s: %v", p.ID, m.ChannelID, m.RecipientSlackID, err)
		if merr := MarkPollMessageFailed(ctx, pool, m.ID, slackErrDetail(err)); merr != nil {
			log.Printf("slack/poll: record failure: %v", merr)
		}
		return
	}
	if err := RecordPollMessagePosted(ctx, pool, m.ID, channelID, ts); err != nil {
		log.Printf("slack/poll: record post: %v", err)
	}
}

// postChannelBlocks posts blocks to a channel and returns where they landed. Like sendDM it throttles and
// records rate limits but leaves retrying to the caller.
func postChannelBlocks(ctx context.Context, client *slacklib.Client, channel, text string, blocks []slacklib.Block) (string, string, error) {
	if err := throttleSlackSend(ctx, channel); err != nil {
		return "", "", fmt.Errorf("throttle slack send: %w", err)
	}
	ch, ts, err := client.PostMessageContext(ctx, channel, slacklib.MsgOptionText(text, false), slacklib.MsgOptionBlocks(blocks...))
	return ch, ts, recordRateLimitIfAny(channel, err)
}

// joinChannelBestEffort joins a public channel so the bot can post in it. A private channel can't be joined, so
// the post is what fails, with an error the creator is told about.
func joinChannelBestEffort(ctx context.Context, client *slacklib.Client, channel string) {
	if _, _, _, err := client.JoinConversationContext(ctx, channel); err != nil {
		log.Printf("slack/poll: join %s: %v", channel, err)
	}
}

// finishPollPosting opens the poll once its messages are out, or cancels it if none could be posted, and tells
// the creator which.
func finishPollPosting(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, id string) {
	p, err := GetPoll(ctx, pool, id)
	if err != nil {
		log.Printf("slack/poll: reload %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return
	}
	if p.Posted == 0 {
		if ok, err := CancelPoll(ctx, pool, id); err != nil {
			log.Printf("slack/poll: cancel %s: %v", id, err)
		} else if ok {
			notifyPollCreator(ctx, client, p, fmt.Sprintf(":warning: Your poll *%s* could not be posted, so it was cancelled.%s",
				escapeMrkdwn(p.Question), pollFailureDetail(ctx, pool, id)))
		}
		return
	}
	if err := MarkPollOpen(ctx, pool, id); err != nil {
		log.Printf("slack/poll: open %s: %v", id, err)
		return
	}
	msg := fmt.Sprintf(":bar_chart: Your poll *%s* is live. It closes %s.", escapeMrkdwn(p.Question), pollDateToken(p.ClosesAt, pollLocation(ctx)))
	if p.Audience == PollAudienceEveryone {
		msg += fmt.Sprintf("\nSent to %d of %d members.", p.Posted, p.Messages)
	}
	if p.Failed > 0 {
		msg += fmt.Sprintf(" %d could not be reached.%s", p.Failed, pollFailureDetail(ctx, pool, id))
	}
	notifyPollCreator(ctx, client, p, msg, pollControlsBlocks(msg, id)...)
}

// pollFailureDetail is the first failed message's error, for telling the creator why.
func pollFailureDetail(ctx context.Context, pool *pgxpool.Pool, id string) string {
	rows, err := pool.Query(ctx, `SELECT COALESCE(error, '') FROM slack.poll_messages WHERE poll_id = $1 AND status = 'failed' ORDER BY id LIMIT 1`, id)
	if err != nil {
		return ""
	}
	defer rows.Close()
	if rows.Next() {
		var detail string
		if rows.Scan(&detail) == nil && strings.TrimSpace(detail) != "" {
			return "\nSlack said: `" + strings.ReplaceAll(detail, "`", "'") + "`"
		}
	}
	return ""
}

func notifyPollCreator(ctx context.Context, client *slacklib.Client, p *Poll, text string, blocks ...slacklib.Block) {
	if p.CreatorSlackID == "" {
		return
	}
	if _, _, err := SendDM(ctx, client, p.CreatorSlackID, text, blocks...); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("slack/poll: notify creator of %s: %v", p.ID, err)
	}
}
