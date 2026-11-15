package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slacklib "github.com/slack-go/slack"
)

func closeEnv(t *testing.T) *invEnv {
	t.Helper()
	prevAPI, prevRetry := pollAPIInterval, pollRetryDelay
	pollAPIInterval, pollRetryDelay = 0, time.Millisecond
	t.Cleanup(func() { pollAPIInterval, pollRetryDelay = prevAPI, prevRetry })
	return pollEnv(t)
}

func reaction(name string, users ...string) map[string]any {
	return map[string]any{"name": name, "count": len(users), "users": users}
}

// openPoll creates a poll and posts it, so it is open with its messages recorded.
func (e *invEnv) openPoll(t *testing.T, in CreatePollInput) string {
	t.Helper()
	id, err := CreatePoll(e.ctx, e.pool, in)
	require.NoError(t, err)
	runPollPosting(e.ctx, e.pool, id)
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Equal(t, PollOpen, p.Status)
	return id
}

func (e *invEnv) creatorDMs() []invMessage {
	return dmsTo(e.fake.postsSnapshot(), "DM_U_PAT")
}

func TestTallyCountsEveryOptionAMemberPicked(t *testing.T) {
	e := closeEnv(t)
	e.user(t, "U_ANN", "Ann Lee")
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	e.fake.reactions = map[string][]map[string]any{"C_LUNCH": {
		reaction("pizza", "U_ANN", "U_BEN", "U_ANN"),
		reaction("taco", "U_ANN"),
		reaction("fire", "U_CAL"), // not an option
	}}
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)

	tally, err := tallyPoll(e.ctx, e.pool, e.client, p)
	require.NoError(t, err)

	assert.Equal(t, 1, tally.Read)
	assert.Zero(t, tally.Unreadable)
	byUser := map[string][]string{}
	names := map[string]string{}
	for _, v := range tally.Votes {
		byUser[v.SlackUserID] = append(byUser[v.SlackUserID], v.OptionID)
		names[v.SlackUserID] = v.DisplayName
	}
	assert.Len(t, byUser["U_ANN"], 2, "Ann counts toward both options")
	assert.Len(t, byUser["U_BEN"], 1)
	assert.NotContains(t, byUser, "U_CAL", "reactions that aren't options are ignored")
	assert.Len(t, tally.Votes, 3, "a repeated user in one reaction counts once")
	assert.Equal(t, "Ann Lee", names["U_ANN"], "names come from members")
	assert.Equal(t, "U_BEN", names["U_BEN"], "unknown members fall back to their Slack ID")
}

func TestTallyMatchesAliasesAndSkinTones(t *testing.T) {
	e := closeEnv(t)
	in := newPollIn(PollAudienceChannel, "C_LUNCH")
	in.Options = []PollOptionInput{{Emoji: "thumbsup", Label: "Yes"}, {Emoji: "-1", Label: "No"}}
	id := e.openPoll(t, in)
	e.fake.reactions = map[string][]map[string]any{"C_LUNCH": {
		reaction("+1::skin-tone-3", "U_ANN"), // Slack's name for the same emoji, with a skin tone
		reaction("-1", "U_BEN"),
	}}
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)

	tally, err := tallyPoll(e.ctx, e.pool, e.client, p)
	require.NoError(t, err)
	require.Len(t, tally.Votes, 2)
	assert.Equal(t, p.Options[0].ID, tally.Votes[0].OptionID, ":+1: with a skin tone is the :thumbsup: option")
	assert.Equal(t, p.Options[1].ID, tally.Votes[1].OptionID)
}

func TestClosePollOnChannelUpdatesMessageAndReportsToCreator(t *testing.T) {
	e := closeEnv(t)
	e.user(t, "U_ANN", "Ann")
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	e.fake.reactions = map[string][]map[string]any{"C_LUNCH": {
		reaction("pizza", "U_ANN", "U_BEN"),
		reaction("taco", "U_ANN"),
	}}
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)

	require.True(t, closePoll(e.ctx, e.pool, id, false))

	p, err = GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollClosed, p.Status)
	assert.Equal(t, 2, p.Voters)
	assert.Equal(t, 2, p.Options[0].Votes)
	assert.Equal(t, 1, p.Options[1].Votes)

	updates := e.fake.updatesSnapshot()
	require.Len(t, updates, 1)
	assert.Equal(t, "C_LUNCH", updates[0].Channel)
	assert.Equal(t, "1.1", updates[0].TS)
	assert.Equal(t, "Poll closed: Lunch?", updates[0].Text)
	assert.Contains(t, updates[0].Blocks, "Poll closed")
	assert.NotContains(t, updates[0].Blocks, "Poll closes")
	assert.NotContains(t, updates[0].Blocks, "React with")
	assert.Contains(t, updates[0].Blocks, "Pizza  —  *2*")
	assert.Contains(t, updates[0].Blocks, "Tacos  —  *1*")
	assert.Contains(t, updates[0].Blocks, "2 people voted")
	assert.Equal(t, p.ClosesAt.Unix(), dateTokenUnix(t, updates[0].Blocks), "it says it closed at the deadline")

	dms := e.creatorDMs()
	require.Len(t, dms, 2, "the live confirmation, then the report")
	report := dms[1].Text
	assert.Contains(t, report, "Your poll *Lunch?* has closed")
	assert.Contains(t, report, ":pizza:  Pizza  —  *2*")
	assert.Contains(t, report, ":taco:  Tacos  —  *1*")
	assert.Contains(t, report, "2 people voted")
	assert.Contains(t, report, "1 person picked more than one option and counts toward each, which can skew the results")
	assert.NotContains(t, report, "<@U_ANN>", "the report names no one")
	assert.NotContains(t, report, "Ann", "the report names no one")
}

func TestClosePollOnEveryoneReportsPartialReads(t *testing.T) {
	e := closeEnv(t)
	for _, id := range []string{"U_ANN", "U_BEN", "U_CAL"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}
	id := e.openPoll(t, newPollIn(PollAudienceEveryone, ""))
	e.fake.reactions = map[string][]map[string]any{
		"DM_U_ANN": {reaction("pizza", "U_ANN")},
		"DM_U_BEN": {reaction("taco", "U_BEN")},
	}
	e.fake.reactionFail = map[string]string{"DM_U_CAL": "message_not_found"}

	require.True(t, closePoll(e.ctx, e.pool, id, false))

	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollClosed, p.Status)
	assert.Equal(t, 2, p.Voters)
	assert.Len(t, e.fake.updatesSnapshot(), 3, "every copy is updated, even the one whose reactions couldn't be read")
	dms := e.creatorDMs()
	report := dms[len(dms)-1].Text
	assert.Contains(t, report, "2 of 3 members voted")
	assert.Contains(t, report, "reactions on 1 of 3 messages could not be read")
	assert.Contains(t, report, "message_not_found")
	assert.NotContains(t, report, "picked more than one")
	assert.NotContains(t, report, "U_ANN")
	assert.NotContains(t, report, "U_BEN")
}

func TestClosePollWhenNoReactionsCanBeRead(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	e.fake.reactionFail = map[string]string{"C_LUNCH": "missing_scope"}

	require.True(t, closePoll(e.ctx, e.pool, id, false))

	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollClosed, p.Status, "it still closes rather than being retried forever")
	updates := e.fake.updatesSnapshot()
	require.Len(t, updates, 1)
	assert.Contains(t, updates[0].Blocks, "Results are unavailable")
	assert.NotContains(t, updates[0].Blocks, "—  *0*")
	dms := e.creatorDMs()
	report := dms[len(dms)-1].Text
	assert.Contains(t, report, "could not be read")
	assert.Contains(t, report, "missing_scope")
	assert.Contains(t, report, "no results")
}

func TestClosePollNotesMessagesThatCouldNotBeUpdated(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	e.fake.updateFail = map[string]string{"C_LUNCH": "message_not_found"}

	require.True(t, closePoll(e.ctx, e.pool, id, false))

	dms := e.creatorDMs()
	assert.Contains(t, dms[len(dms)-1].Text, "1 poll messages could not be updated")
}

func TestClosePollEarlySaysWhenItActuallyClosed(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)

	require.True(t, closePoll(e.ctx, e.pool, id, true))

	updates := e.fake.updatesSnapshot()
	require.Len(t, updates, 1)
	closed := dateTokenUnix(t, updates[0].Blocks)
	assert.NotEqual(t, p.ClosesAt.Unix(), closed, "not the deadline an hour away")
	assert.WithinDuration(t, time.Now(), time.Unix(closed, 0), time.Minute, "the time it was closed")
	dms := e.creatorDMs()
	assert.Contains(t, dms[len(dms)-1].Text, "was closed early")
}

func TestClosePollOnlyOnceAndOnlyWhenOpen(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	assert.True(t, closePoll(e.ctx, e.pool, id, false))
	assert.False(t, closePoll(e.ctx, e.pool, id, false), "already closed")
	assert.Len(t, e.fake.updatesSnapshot(), 1)

	posting, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceChannel, "C2"))
	require.NoError(t, err)
	assert.False(t, closePoll(e.ctx, e.pool, posting, false), "a poll still posting can't be closed")
}

func TestSchedulerClosesOnlyDuePolls(t *testing.T) {
	e := closeEnv(t)
	due := e.openPoll(t, newPollIn(PollAudienceChannel, "C_DUE"))
	later := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LATER"))
	_, err := e.pool.Exec(e.ctx, `UPDATE slack.polls SET closes_at = NOW() - interval '1 minute' WHERE id = $1`, due)
	require.NoError(t, err)

	startDuePolls(e.ctx, e.pool)

	require.Eventually(t, func() bool {
		p, err := GetPoll(e.ctx, e.pool, due)
		return err == nil && p.Status == PollClosed
	}, 5*time.Second, 20*time.Millisecond)
	p, err := GetPoll(e.ctx, e.pool, later)
	require.NoError(t, err)
	assert.Equal(t, PollOpen, p.Status)
}

func controlClick(e *invEnv, t *testing.T, slackUser, actionID, pollID string) {
	t.Helper()
	e.click(t, slackUser, actionID, pollID)
}

func (e *invEnv) waitForStatus(t *testing.T, id, status string) {
	t.Helper()
	require.Eventually(t, func() bool {
		p, err := GetPoll(e.ctx, e.pool, id)
		return err == nil && p.Status == status
	}, 5*time.Second, 20*time.Millisecond, "poll should become %s", status)
}

func TestCloseNowButtonClosesAndEditsTheControls(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	e.fake.reactions = map[string][]map[string]any{"C_LUNCH": {reaction("pizza", "U_ANN")}}

	controlClick(e, t, "U_PAT", ActionPollClose, id)

	e.waitForStatus(t, id, PollClosed)
	require.Eventually(t, func() bool {
		for _, u := range e.fake.updatesSnapshot() {
			if u.Channel == "D_CH" && u.TS == "111.222" {
				return strings.Contains(u.Text, "was closed early")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the controls message says what happened")
	for _, u := range e.fake.updatesSnapshot() {
		if u.Channel == "D_CH" {
			assert.NotContains(t, u.Blocks, "poll_close", "the buttons are gone")
		}
	}
}

func TestCancelPollButtonCancelsWithoutATally(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	e.fake.reactions = map[string][]map[string]any{"C_LUNCH": {reaction("pizza", "U_ANN")}}
	before := len(e.creatorDMs())

	controlClick(e, t, "U_PAT", ActionPollCancel, id)

	e.waitForStatus(t, id, PollCancelled)
	require.Eventually(t, func() bool {
		for _, u := range e.fake.updatesSnapshot() {
			if u.Channel == "C_LUNCH" {
				return u.Text == "Poll cancelled: Lunch?"
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the poll message is replaced with a cancelled notice")
	votes, err := ListPollVotes(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Empty(t, votes, "no tally")
	assert.Len(t, e.creatorDMs(), before, "no report is sent")
	for _, u := range e.fake.updatesSnapshot() {
		if u.Channel == "C_LUNCH" {
			assert.Contains(t, u.Blocks, "Poll cancelled")
			assert.NotContains(t, u.Blocks, "React with")
		}
	}
}

func TestControlButtonsAreCreatorOnly(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))

	controlClick(e, t, "U_INTRUDER", ActionPollCancel, id)

	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if m.User == "U_INTRUDER" {
				return strings.Contains(m.Text, "Only the person who created this poll")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollOpen, p.Status, "someone else's click changes nothing")
}

func TestControlButtonsOnAFinishedPoll(t *testing.T) {
	e := closeEnv(t)
	id := e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	require.True(t, closePoll(e.ctx, e.pool, id, false))
	before := len(e.fake.updatesSnapshot())

	controlClick(e, t, "U_PAT", ActionPollCancel, id)

	require.Eventually(t, func() bool {
		updates := e.fake.updatesSnapshot()
		return len(updates) > before && strings.Contains(updates[len(updates)-1].Text, "already closed")
	}, 5*time.Second, 20*time.Millisecond, "the controls say it is already closed")
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollClosed, p.Status, "a closed poll can't be cancelled")
}

func TestCloseNowWhileStillPostingIsRefused(t *testing.T) {
	e := closeEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceChannel, "C_LUNCH"))
	require.NoError(t, err)

	controlClick(e, t, "U_PAT", ActionPollClose, id)

	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if m.User == "U_PAT" {
				return strings.Contains(m.Text, "still being sent")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollPosting, p.Status)
}

func TestCancellingWhilePostingStopsTheRest(t *testing.T) {
	e := closeEnv(t)
	for _, id := range []string{"U_ANN", "U_BEN", "U_CAL"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}
	id, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceEveryone, ""))
	require.NoError(t, err)
	var once bool
	e.fake.onPost = func(channel string) {
		if strings.HasPrefix(channel, "DM_U_") && channel != "DM_U_PAT" && !once {
			once = true
			CancelPoll(e.ctx, e.pool, id) // the creator cancels after the first member received it
		}
	}

	runPollPosting(e.ctx, e.pool, id)

	var delivered int
	for _, m := range e.fake.postsSnapshot() {
		if m.Channel != "DM_U_PAT" {
			delivered++
		}
	}
	assert.Equal(t, 1, delivered, "nobody after the cancel receives the poll")
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollCancelled, p.Status)
	updates := e.fake.updatesSnapshot()
	require.Len(t, updates, 1, "the one copy already sent is replaced with the cancelled notice")
	assert.Equal(t, "Poll cancelled: Lunch?", updates[0].Text)
}

func TestLiveMessageHasCloseAndCancelButtons(t *testing.T) {
	e := closeEnv(t)
	e.openPoll(t, newPollIn(PollAudienceChannel, "C_LUNCH"))
	dms := e.creatorDMs()
	require.Len(t, dms, 1)
	assert.Contains(t, dms[0].Blocks, ActionPollClose)
	assert.Contains(t, dms[0].Blocks, ActionPollCancel)
	assert.Contains(t, dms[0].Blocks, "Close now")
	assert.Contains(t, dms[0].Blocks, "Cancel poll")
}

func TestPollMessageForEachState(t *testing.T) {
	loc := time.UTC
	p := &Poll{
		Question: "Lunch?", CreatorSlackID: "U_PAT", Audience: PollAudienceEveryone, ClosesAt: time.Unix(1795210200, 0),
		Voters: 2, Posted: 5,
		Options: []PollOption{{Emoji: "pizza", Label: "Pizza", Votes: 2}, {Emoji: "taco", Label: "Tacos", Votes: 0}},
	}
	text, closed := pollMessage(&Poll{Status: PollClosed, Question: p.Question, CreatorSlackID: "U_PAT", Audience: PollAudienceEveryone,
		ClosesAt: p.ClosesAt, Voters: 2, Posted: 5, Options: p.Options}, loc)
	assert.Equal(t, "Poll closed: Lunch?", text)
	assert.Contains(t, blocksJSON(t, closed), "2 of 5 members voted")

	text, cancelled := pollMessage(&Poll{Status: PollCancelled, Question: "Lunch?", CreatorSlackID: "U_PAT"}, loc)
	assert.Equal(t, "Poll cancelled: Lunch?", text)
	assert.Contains(t, blocksJSON(t, cancelled), "~Lunch?~", "the question is struck through")

	text, open := pollMessage(&Poll{Status: PollOpen, Question: "Lunch?", CreatorSlackID: "U_PAT", ClosesAt: p.ClosesAt, Options: p.Options}, loc)
	assert.Equal(t, "Poll: Lunch?", text)
	assert.Contains(t, blocksJSON(t, open), "React with")
}

func blocksJSON(t *testing.T, blocks []slacklib.Block) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(blocks))
	return b.String()
}

func TestMultiVoterCountCountsMembersNotVotes(t *testing.T) {
	assert.Equal(t, 2, MultiVoterCount([]PollVote{
		{OptionID: "o1", SlackUserID: "U_B"}, {OptionID: "o3", SlackUserID: "U_B"}, {OptionID: "o2", SlackUserID: "U_B"},
		{OptionID: "o1", SlackUserID: "U_A"}, {OptionID: "o2", SlackUserID: "U_A"},
		{OptionID: "o2", SlackUserID: "U_C"},
	}), "B and A picked several, C one")
	assert.Zero(t, MultiVoterCount(nil))
}

func TestReportWarnsPluralAndSingular(t *testing.T) {
	p := &Poll{Question: "Q", Voters: 3, Options: []PollOption{{ID: "o1", Emoji: "pizza", Label: "A"}, {ID: "o2", Emoji: "taco", Label: "B"}}}
	votes := []PollVote{
		{OptionID: "o1", SlackUserID: "U_A"}, {OptionID: "o2", SlackUserID: "U_A"},
		{OptionID: "o1", SlackUserID: "U_B"}, {OptionID: "o2", SlackUserID: "U_B"},
		{OptionID: "o1", SlackUserID: "U_C"},
	}
	report := pollReport(p, pollTally{Read: 1}, true, false, 0, votes)
	assert.Contains(t, report, "2 people picked more than one option and count toward each, which can skew the results")
	assert.NotContains(t, report, "U_A")
}

func TestStripSkinTone(t *testing.T) {
	assert.Equal(t, "+1", stripSkinTone("+1::skin-tone-2"))
	assert.Equal(t, "pizza", stripSkinTone("pizza"))
}

func TestPollCallRetriesRateLimitsAndStopsOnPermanentErrors(t *testing.T) {
	calls := 0
	err := pollCall(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &slacklib.RateLimitedError{RetryAfter: time.Millisecond}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)

	calls = 0
	err = pollCall(context.Background(), func() error {
		calls++
		return fmt.Errorf("channel_not_found")
	})
	assert.Error(t, err)
	assert.Equal(t, 1, calls, "a permanent error isn't retried")

	calls = 0
	err = pollCall(context.Background(), func() error {
		calls++
		return &slacklib.RateLimitedError{RetryAfter: time.Millisecond}
	})
	assert.Error(t, err)
	assert.Equal(t, maxPollRateLimitWaits+1, calls, "waits out a rate limit many times before giving up")

	calls = 0
	err = pollCall(context.Background(), func() error {
		calls++
		return slacklib.StatusCodeError{Code: 502, Status: "502 Bad Gateway"}
	})
	assert.Error(t, err)
	assert.Equal(t, pollCallAttempts, calls, "a server error is tried a few times, not endlessly")
}

// unescape undoes the JSON escaping of < > & in the raw blocks the fake Slack records.
func unescape(s string) string {
	return strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&").Replace(s)
}

// dateTokenUnix is the time in the first <!date^N^...> token of some blocks.
func dateTokenUnix(t *testing.T, blocks string) int64 {
	t.Helper()
	blocks = unescape(blocks)
	i := strings.Index(blocks, "<!date^")
	require.GreaterOrEqual(t, i, 0, "no date token in %s", blocks)
	rest := blocks[i+len("<!date^"):]
	var n int64
	_, err := fmt.Sscanf(rest[:strings.Index(rest, "^")], "%d", &n)
	require.NoError(t, err)
	return n
}
