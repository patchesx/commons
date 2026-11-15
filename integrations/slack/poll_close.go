package slack

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
)

const (
	// pollCloseInterval is how often open polls are checked for being past their deadline. Reactions added
	// between the deadline and the next check are still counted, since Slack gives no reaction timestamps.
	pollCloseInterval = 15 * time.Second

	// pollCallAttempts is how many times a call is tried when Slack answers with a server error.
	pollCallAttempts = 3
	// maxPollRateLimitWaits is how many times a call waits out a rate limit before giving up. Slack says how
	// long to wait, so waiting is almost always enough, and a vote shouldn't be lost to a busy moment.
	maxPollRateLimitWaits = 10
	maxPollRetryAfter     = 30 * time.Second
)

var (
	// pollAPIInterval spaces the reactions.get and chat.update calls an everyone poll makes, one per member. Both
	// are Slack Tier 3 (50+ a minute, and Slack advises about one a second). This is a little faster than that,
	// which Slack tolerates in short bursts, and pollCall waits out any 429 it does get. Results come a little
	// slower than they could rather than risk pushing Slack's limits.
	pollAPIInterval = 500 * time.Millisecond
	// pollRetryDelay is the wait before retrying a call Slack answered with a server error.
	pollRetryDelay = 2 * time.Second
)

// RunPollScheduler closes polls once their deadline has passed. It checks immediately, so a poll that came due
// while the app was down closes on boot, and then every pollCloseInterval until ctx is cancelled.
func RunPollScheduler(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(pollCloseInterval)
	defer ticker.Stop()
	for {
		startDuePolls(ctx, pool)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func startDuePolls(ctx context.Context, pool *pgxpool.Pool) {
	ids, err := ListDuePollIDs(ctx, pool)
	if err != nil {
		log.Printf("slack/poll: list due polls: %v", err)
		return
	}
	for _, id := range ids {
		log.Printf("slack/poll: closing %s", id)
		go closePoll(context.Background(), pool, id, false)
	}
}

// pollCall runs a Slack call, waiting out a rate limit for as long as Slack asks and retrying server errors a few
// times.
func pollCall(ctx context.Context, fn func() error) error {
	transient, limited := 0, 0
	for {
		err := fn()
		if err == nil {
			return nil
		}
		var wait time.Duration
		var rl *slacklib.RateLimitedError
		switch {
		case errors.As(err, &rl):
			if limited++; limited > maxPollRateLimitWaits {
				return err
			}
			wait = min(rl.RetryAfter, maxPollRetryAfter)
		case isRetryableSendError(err):
			if transient++; transient >= pollCallAttempts {
				return err
			}
			wait = pollRetryDelay * time.Duration(transient)
		default:
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// stripSkinTone turns the reaction name Slack reports for a skin-toned emoji ("+1::skin-tone-3") into the plain name.
func stripSkinTone(name string) string {
	if i := strings.Index(name, "::"); i >= 0 {
		return name[:i]
	}
	return name
}

// pollTally is what reading a poll's reactions found.
type pollTally struct {
	Votes      []PollVote
	Read       int    // messages whose reactions were read
	Unreadable int    // messages whose reactions could not be read
	LastError  string // why the last unreadable message failed
}

// tallyPoll reads the reactions on every posted copy of the poll. A member who reacted with several of the
// options votes for each of them. Reactions that aren't one of the options are ignored.
func tallyPoll(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, p *Poll) (pollTally, error) {
	byKey := map[string]PollOption{}
	for _, o := range p.Options {
		byKey[emojiKey(o.Emoji)] = o
	}
	msgs, err := ListPostedPollMessages(ctx, pool, p.ID)
	if err != nil {
		return pollTally{}, err
	}

	var t pollTally
	type vote struct{ option, user string }
	seen := map[vote]bool{}
	var order []vote
	for i, m := range msgs {
		if i > 0 {
			time.Sleep(pollAPIInterval)
		}
		var item slacklib.ReactedItem
		err := pollCall(ctx, func() error {
			var gerr error
			item, gerr = client.GetReactionsContext(ctx, slacklib.NewRefToMessage(m.ChannelID, m.TS), slacklib.GetReactionsParameters{Full: true})
			return gerr
		})
		if err != nil {
			t.Unreadable++
			t.LastError = slackErrDetail(err)
			log.Printf("slack/poll: read reactions of %s in %s: %v", p.ID, m.ChannelID, err)
			continue
		}
		t.Read++
		for _, r := range item.Reactions {
			opt, ok := byKey[emojiKey(stripSkinTone(r.Name))]
			if !ok {
				continue
			}
			for _, u := range r.Users {
				v := vote{opt.ID, u}
				if !seen[v] {
					seen[v] = true
					order = append(order, v)
				}
			}
		}
	}

	ids := make([]string, 0, len(order))
	have := map[string]bool{}
	for _, v := range order {
		if !have[v.user] {
			have[v.user] = true
			ids = append(ids, v.user)
		}
	}
	names, err := SlackDisplayNames(ctx, pool, ids)
	if err != nil {
		log.Printf("slack/poll: look up voter names for %s: %v", p.ID, err)
	}
	for _, v := range order {
		name := names[v.user]
		if name == "" {
			name = v.user
		}
		t.Votes = append(t.Votes, PollVote{OptionID: v.option, SlackUserID: v.user, DisplayName: name})
	}
	return t, nil
}

// closePoll tallies a poll and shows the result: it saves the votes, rewrites every copy of the poll to say it
// closed with the totals, and DMs the creator a report. early is set when the creator closed it before the
// deadline, so the message says when that happened instead of the deadline. It returns false if another
// goroutine already holds the poll or it isn't open.
func closePoll(ctx context.Context, pool *pgxpool.Pool, id string, early bool) bool {
	claimed, err := ClaimPoll(ctx, pool, id, PollOpen)
	if err != nil {
		log.Printf("slack/poll: claim %s for closing: %v", id, err)
		return false
	}
	if !claimed {
		return false
	}
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/poll: %s: bot_token not configured", id)
		ReleasePoll(ctx, pool, id)
		return false
	}
	p, err := GetPoll(ctx, pool, id)
	if err != nil {
		log.Printf("slack/poll: load %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return false
	}
	tally, err := tallyPoll(ctx, pool, client, p)
	if err != nil {
		log.Printf("slack/poll: tally %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return false
	}
	available := tally.Read > 0 || tally.Unreadable == 0
	if err := SavePollVotes(ctx, pool, id, tally.Votes); err != nil {
		log.Printf("slack/poll: save votes for %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return false
	}
	if p, err = GetPoll(ctx, pool, id); err != nil {
		log.Printf("slack/poll: reload %s: %v", id, err)
		ReleasePoll(ctx, pool, id)
		return false
	}

	closedAt := p.ClosesAt
	if early {
		closedAt = time.Now()
	}
	loc := pollLocation(ctx)
	text, blocks := pollClosedMessage(p, loc, closedAt, available)
	notUpdated := updatePollMessages(ctx, pool, client, id, text, blocks)

	notifyPollCreator(ctx, client, p, pollReport(p, tally, available, early, notUpdated, tally.Votes))
	if err := ClosePoll(ctx, pool, id); err != nil {
		log.Printf("slack/poll: close %s: %v", id, err)
	}
	return true
}

// updatePollMessages rewrites every posted copy of a poll and returns how many could not be updated.
func updatePollMessages(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, id, text string, blocks []slacklib.Block) int {
	msgs, err := ListPostedPollMessages(ctx, pool, id)
	if err != nil {
		log.Printf("slack/poll: list messages of %s: %v", id, err)
		return 0
	}
	failed := 0
	for i, m := range msgs {
		if i > 0 {
			time.Sleep(pollAPIInterval)
		}
		err := pollCall(ctx, func() error {
			_, _, _, uerr := client.UpdateMessageContext(ctx, m.ChannelID, m.TS,
				slacklib.MsgOptionText(text, false), slacklib.MsgOptionBlocks(blocks...))
			return uerr
		})
		if err != nil {
			failed++
			log.Printf("slack/poll: update message of %s in %s: %v", id, m.ChannelID, err)
		}
	}
	return failed
}

// refreshPollMessages rewrites every posted copy of a poll to match its current state.
func refreshPollMessages(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, id string) int {
	p, err := GetPoll(ctx, pool, id)
	if err != nil {
		log.Printf("slack/poll: reload %s: %v", id, err)
		return 0
	}
	text, blocks := pollMessage(p, pollLocation(ctx))
	return updatePollMessages(ctx, pool, client, id, text, blocks)
}

// pollReport is the creator's summary: the totals, how many voted, a warning if anyone picked more than one
// option, and anything that couldn't be read or updated. It names no one.
func pollReport(p *Poll, t pollTally, available, early bool, notUpdated int, votes []PollVote) string {
	var b strings.Builder
	verb := "has closed"
	if early {
		verb = "was closed early"
	}
	fmt.Fprintf(&b, ":white_check_mark: Your poll *%s* %s.", escapeMrkdwn(p.Question), verb)
	if !available {
		fmt.Fprintf(&b, "\n:warning: The reactions could not be read (`%s`), so there are no results.", strings.ReplaceAll(t.LastError, "`", "'"))
		return b.String()
	}
	b.WriteString("\n" + pollOptionLines(p, true))
	fmt.Fprintf(&b, "\n%s.", pollVotersLine(p))

	switch n := MultiVoterCount(votes); n {
	case 0:
	case 1:
		b.WriteString("\n:warning: 1 person picked more than one option and counts toward each, which can skew the results.")
	default:
		fmt.Fprintf(&b, "\n:warning: %d people picked more than one option and count toward each, which can skew the results.", n)
	}
	if t.Unreadable > 0 {
		fmt.Fprintf(&b, "\n:warning: The reactions on %d of %d messages could not be read, so those votes are missing (`%s`).",
			t.Unreadable, t.Read+t.Unreadable, strings.ReplaceAll(t.LastError, "`", "'"))
	}
	if notUpdated > 0 {
		fmt.Fprintf(&b, "\n:warning: %d poll messages could not be updated with the results.", notUpdated)
	}
	return b.String()
}

// MultiVoterCount is how many members voted for more than one option.
func MultiVoterCount(votes []PollVote) int {
	picks := map[string]int{}
	for _, v := range votes {
		picks[v.SlackUserID]++
	}
	n := 0
	for _, c := range picks {
		if c > 1 {
			n++
		}
	}
	return n
}

// handlePollControl runs the creator's Close now or Cancel poll button. Only the creator may use them, and the
// message the button is on is edited to show the outcome.
func handlePollControl(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, pollID string, cancel bool) {
	channelID, ts := messageRef(payload)
	p, err := GetPoll(ctx, pool, pollID)
	if err != nil {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "That poll no longer exists.")
		return
	}
	if p.CreatorSlackID != payload.User.ID {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "Only the person who created this poll can do that.")
		return
	}
	done := func(text string) {
		editMessage(ctx, client, channelID, ts, text, []slacklib.Block{slacklib.NewSectionBlock(pollMD(text), nil, nil)})
	}
	question := escapeMrkdwn(p.Question)
	if p.Status == PollClosed || p.Status == PollCancelled {
		done(fmt.Sprintf("Your poll *%s* is already %s.", question, p.Status))
		return
	}

	if cancel {
		ok, err := CancelPoll(ctx, pool, pollID)
		if err != nil {
			log.Printf("slack/poll: cancel %s: %v", pollID, err)
			tellOnlyYou(ctx, client, channelID, payload.User.ID, "The poll couldn't be cancelled. Please try again.")
			return
		}
		if !ok {
			tellOnlyYou(ctx, client, channelID, payload.User.ID, "That poll just finished, so it can't be cancelled.")
			return
		}
		refreshPollMessages(ctx, pool, client, pollID)
		done(fmt.Sprintf(":no_entry_sign: Your poll *%s* was cancelled. No results were tallied.", question))
		return
	}

	if p.Status == PollPosting {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "The poll is still being sent. Try again once it has finished.")
		return
	}
	if !closePoll(ctx, pool, pollID, true) {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "That poll is already being closed.")
		return
	}
	done(fmt.Sprintf(":white_check_mark: Your poll *%s* was closed early. The results are below.", question))
}
