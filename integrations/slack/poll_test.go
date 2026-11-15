package slack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/permissions"
	"commons/store"
)

func TestNormalizePollEmoji(t *testing.T) {
	assert.Equal(t, "pizza", normalizePollEmoji(":Pizza:"))
	assert.Equal(t, "pizza", normalizePollEmoji("  pizza "))
	assert.Equal(t, "pizza", normalizePollEmoji("🍕"), "an emoji character becomes its shortcode")
	assert.Equal(t, "+1", normalizePollEmoji("👍"))
	assert.Equal(t, "heart", normalizePollEmoji("❤️"), "variation selector is ignored")
	assert.Equal(t, "partyparrot", normalizePollEmoji(":partyparrot:"))
	assert.Equal(t, emojiKey("+1"), emojiKey("thumbsup"))
	assert.NotEqual(t, emojiKey("+1"), emojiKey("-1"))
}

func TestParsePollDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90m": 90 * time.Minute, "2h": 2 * time.Hour, "3d": 72 * time.Hour, "3 days": 72 * time.Hour,
		"1d 12h": 36 * time.Hour, "45 minutes": 45 * time.Minute, " 2H ": 2 * time.Hour,
	} {
		got, err := parsePollDuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "soon", "0m", "5", "h", "2 fortnights", "1d x"} {
		_, err := parsePollDuration(in)
		assert.Error(t, err, in)
	}
}

func TestPollMessageLayout(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	closes := time.Date(2026, 11, 20, 21, 30, 0, 0, time.UTC)
	text, blocks := pollMessage(&Poll{
		Question: "Lunch <@everyone>?", CreatorSlackID: "U_PAT", ClosesAt: closes,
		Options: []PollOption{{Emoji: "pizza", Label: "Pizza & chips"}, {Emoji: "taco", Label: "Tacos"}},
	}, loc)
	assert.Equal(t, "Poll: Lunch <@everyone>?", text)
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(blocks))
	body := buf.String()
	assert.Contains(t, body, "Poll closes <!date^"+"1795210200"+"^")
	assert.Contains(t, body, "|Nov 20, 2026 3:30 PM CST>", "fallback in the org timezone")
	assert.Contains(t, body, ":pizza:  Pizza &amp; chips")
	assert.Contains(t, body, "Lunch &lt;@everyone&gt;?", "the question is escaped so it can't ping")
	assert.Contains(t, body, "@U_PAT", "credits the creator")
}

func newPollIn(audience, channel string) CreatePollInput {
	in := pollInput(audience, channel)
	in.Options = []PollOptionInput{{Emoji: "pizza", Label: "Pizza"}, {Emoji: "taco", Label: "Tacos"}}
	return in
}

func pollEnv(t *testing.T) *invEnv {
	t.Helper()
	prev := broadcastInterval
	broadcastInterval = 0
	t.Cleanup(func() { broadcastInterval = prev })
	e := newInvEnv(t)
	e.usePackageClient(t)
	return e
}

func TestRunPollPostingToAChannel(t *testing.T) {
	e := pollEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceChannel, "C_LUNCH"))
	require.NoError(t, err)

	runPollPosting(e.ctx, e.pool, id)

	e.fake.mu.Lock()
	assert.Equal(t, []string{"C_LUNCH"}, e.fake.joins, "the bot joins the channel first")
	e.fake.mu.Unlock()
	posts := dmsTo(e.fake.postsSnapshot(), "C_LUNCH")
	require.Len(t, posts, 1)
	assert.Equal(t, "Poll: Lunch?", posts[0].Text)
	assert.Contains(t, posts[0].Blocks, ":pizza:  Pizza")

	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollOpen, p.Status)
	posted, err := ListPostedPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Len(t, posted, 1)
	assert.Equal(t, "C_LUNCH", posted[0].ChannelID)
	assert.Equal(t, "1.1", posted[0].TS)

	creator := dmsTo(e.fake.postsSnapshot(), "DM_U_PAT")
	require.Len(t, creator, 1)
	assert.Contains(t, creator[0].Text, "Your poll *Lunch?* is live")
}

func TestRunPollPostingToEveryone(t *testing.T) {
	e := pollEnv(t)
	for _, id := range []string{"U_ANN", "U_BEN", "U_CAL"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}
	e.setStatus(t, "U_CAL", "deactivated")
	pollID, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceEveryone, ""))
	require.NoError(t, err)

	runPollPosting(e.ctx, e.pool, pollID)

	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_ANN"), 1)
	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_BEN"), 1)
	assert.Empty(t, dmsTo(e.fake.postsSnapshot(), "DM_U_CAL"), "deactivated members don't get it")
	p, err := GetPoll(e.ctx, e.pool, pollID)
	require.NoError(t, err)
	assert.Equal(t, PollOpen, p.Status)
	assert.Equal(t, 2, p.Posted)
	creator := dmsTo(e.fake.postsSnapshot(), "DM_U_PAT")
	require.Len(t, creator, 1)
	assert.Contains(t, creator[0].Text, "Sent to 2 of 2 members")
}

func TestRunPollPostingPartialFailureKeepsPollOpen(t *testing.T) {
	e := pollEnv(t)
	for _, id := range []string{"U_ANN", "U_BEN"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}
	e.fake.postFail = map[string]string{"DM_U_BEN": "user_disabled"}
	pollID, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceEveryone, ""))
	require.NoError(t, err)

	runPollPosting(e.ctx, e.pool, pollID)

	p, err := GetPoll(e.ctx, e.pool, pollID)
	require.NoError(t, err)
	assert.Equal(t, PollOpen, p.Status)
	assert.Equal(t, 1, p.Posted)
	assert.Equal(t, 1, p.Failed)
	creator := dmsTo(e.fake.postsSnapshot(), "DM_U_PAT")
	require.Len(t, creator, 1)
	assert.Contains(t, creator[0].Text, "Sent to 1 of 2 members")
	assert.Contains(t, creator[0].Text, "1 could not be reached")
	assert.Contains(t, creator[0].Text, "user_disabled")
}

func TestRunPollPostingCancelsWhenNothingCouldBePosted(t *testing.T) {
	e := pollEnv(t)
	e.fake.postFail = map[string]string{"C_PRIVATE": "not_in_channel"}
	id, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceChannel, "C_PRIVATE"))
	require.NoError(t, err)

	runPollPosting(e.ctx, e.pool, id)

	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollCancelled, p.Status)
	creator := dmsTo(e.fake.postsSnapshot(), "DM_U_PAT")
	require.Len(t, creator, 1)
	assert.Contains(t, creator[0].Text, "could not be posted")
	assert.Contains(t, creator[0].Text, "not_in_channel")
}

func TestRunPollPostingResumesOnlyPendingMessages(t *testing.T) {
	e := pollEnv(t)
	for _, id := range []string{"U_ANN", "U_BEN"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}
	pollID, err := CreatePoll(e.ctx, e.pool, newPollIn(PollAudienceEveryone, ""))
	require.NoError(t, err)
	msgs, err := ListPendingPollMessages(e.ctx, e.pool, pollID)
	require.NoError(t, err)
	for _, m := range msgs {
		if m.RecipientSlackID == "U_ANN" {
			require.NoError(t, RecordPollMessagePosted(e.ctx, e.pool, m.ID, "DM_U_ANN", "9.9"))
		}
	}
	// A crash mid-posting leaves the claim; startup clears it and resumes.
	claimed, err := ClaimPoll(e.ctx, e.pool, pollID, PollPosting)
	require.NoError(t, err)
	require.True(t, claimed)
	ids, err := ListUnfinishedPollIDs(e.ctx, e.pool)
	require.NoError(t, err)
	require.Equal(t, []string{pollID}, ids)

	runPollPosting(e.ctx, e.pool, pollID)

	assert.Empty(t, dmsTo(e.fake.postsSnapshot(), "DM_U_ANN"), "already received it")
	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_BEN"), 1)
}

func (e *invEnv) grantPoll(t *testing.T, slackID, name string) *store.User {
	t.Helper()
	u := e.user(t, slackID, name)
	role, err := store.CreateRole(e.ctx, e.pool, "role-"+slackID, "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(e.ctx, e.pool, role.ID, permissions.PollCreate))
	group, err := store.CreateRoleGroup(e.ctx, e.pool, "group-"+slackID, "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(e.ctx, e.pool, group.ID, role.ID))
	require.NoError(t, store.AssignGroupToUser(e.ctx, e.pool, u.ID, group.ID))
	return u
}

func TestPollEmojiKnownUsesWorkspaceEmoji(t *testing.T) {
	pollEnv(t)
	customEmojiMu.Lock()
	customEmojiNames, customEmojiAt = nil, time.Time{}
	customEmojiMu.Unlock()
	t.Cleanup(func() {
		customEmojiMu.Lock()
		customEmojiNames, customEmojiAt = nil, time.Time{}
		customEmojiMu.Unlock()
	})

	known := pollEmojiKnown(context.Background())
	assert.True(t, known("pizza"), "standard")
	assert.True(t, known("partyparrot"), "custom")
	assert.True(t, known("parrot"), "custom alias")
	assert.False(t, known("nonsense"))
}
