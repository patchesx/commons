package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pollInput(audience, channel string) CreatePollInput {
	return CreatePollInput{
		CreatorName:    "Pat",
		CreatorSlackID: "U_PAT",
		Question:       "Lunch?",
		Audience:       audience,
		ChannelID:      channel,
		ClosesAt:       time.Now().Add(time.Hour),
		Options:        []PollOptionInput{{Emoji: "pizza", Label: "Pizza"}, {Emoji: "taco", Label: "Tacos"}},
	}
}

func TestCreateChannelPollHasOneMessageAndOrderedOptions(t *testing.T) {
	e := newInvEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C_LUNCH"))
	require.NoError(t, err)

	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollPosting, p.Status)
	assert.Equal(t, "C_LUNCH", p.ChannelID)
	require.Len(t, p.Options, 2)
	assert.Equal(t, "pizza", p.Options[0].Emoji)
	assert.Equal(t, "taco", p.Options[1].Emoji)
	assert.Equal(t, 1, p.Messages)

	msgs, err := ListPendingPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "C_LUNCH", msgs[0].ChannelID)
	assert.Empty(t, msgs[0].RecipientSlackID)
}

func TestCreateEveryonePollSnapshotsActiveMembers(t *testing.T) {
	e := newInvEnv(t)
	for slackID, status := range map[string]string{"U_ANN": "active", "U_DEV": "deactivated", "U_INV": "invited"} {
		e.user(t, slackID, slackID)
		e.setStatus(t, slackID, status)
	}
	id, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceEveryone, ""))
	require.NoError(t, err)

	msgs, err := ListPendingPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "U_ANN", msgs[0].RecipientSlackID)
	assert.NotEmpty(t, msgs[0].RecipientUserID)
}

func TestPollRejectsDuplicateEmojiAndBadAudience(t *testing.T) {
	e := newInvEnv(t)
	in := pollInput(PollAudienceChannel, "C1")
	in.Options = []PollOptionInput{{Emoji: "pizza", Label: "A"}, {Emoji: "pizza", Label: "B"}}
	_, err := CreatePoll(e.ctx, e.pool, in)
	assert.Error(t, err, "the same emoji can't mean two answers")

	_, err = CreatePoll(e.ctx, e.pool, pollInput("nobody", "C1"))
	assert.Error(t, err)

	list, err := ListPolls(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list, "a failed create leaves nothing behind")
}

func TestPollClaimIsExclusiveAndStateChecked(t *testing.T) {
	e := newInvEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C1"))
	require.NoError(t, err)

	notOpen, err := ClaimPoll(e.ctx, e.pool, id, PollOpen)
	require.NoError(t, err)
	assert.False(t, notOpen, "a poll still posting can't be claimed for closing")

	first, err := ClaimPoll(e.ctx, e.pool, id, PollPosting)
	require.NoError(t, err)
	second, err := ClaimPoll(e.ctx, e.pool, id, PollPosting)
	require.NoError(t, err)
	assert.True(t, first)
	assert.False(t, second)

	require.NoError(t, MarkPollOpen(e.ctx, e.pool, id))
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollOpen, p.Status)
	again, err := ClaimPoll(e.ctx, e.pool, id, PollOpen)
	require.NoError(t, err)
	assert.True(t, again, "MarkPollOpen released the claim")
}

func TestListDuePollsOnlyOpenAndPastDeadline(t *testing.T) {
	e := newInvEnv(t)
	past, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C1"))
	require.NoError(t, err)
	future, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C2"))
	require.NoError(t, err)
	posting, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C3"))
	require.NoError(t, err)
	for _, id := range []string{past, future} {
		require.NoError(t, MarkPollOpen(e.ctx, e.pool, id))
	}
	for _, id := range []string{past, posting} {
		_, err := e.pool.Exec(e.ctx, `UPDATE slack.polls SET closes_at = NOW() - interval '1 minute' WHERE id = $1`, id)
		require.NoError(t, err)
	}

	ids, err := ListDuePollIDs(e.ctx, e.pool)
	require.NoError(t, err)
	assert.Equal(t, []string{past}, ids, "not the future one, and not the one still posting")
}

func TestUnfinishedPollsClearClaimsAndResumePosting(t *testing.T) {
	e := newInvEnv(t)
	postingID, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C1"))
	require.NoError(t, err)
	openID, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C2"))
	require.NoError(t, err)
	require.NoError(t, MarkPollOpen(e.ctx, e.pool, openID))
	_, err = ClaimPoll(e.ctx, e.pool, postingID, PollPosting)
	require.NoError(t, err)
	_, err = ClaimPoll(e.ctx, e.pool, openID, PollOpen)
	require.NoError(t, err)

	posting, err := ListUnfinishedPollIDs(e.ctx, e.pool)
	require.NoError(t, err)
	assert.Equal(t, []string{postingID}, posting)

	again, err := ClaimPoll(e.ctx, e.pool, openID, PollOpen)
	require.NoError(t, err)
	assert.True(t, again, "a claim left by a crashed process is cleared")
}

func TestPollMessagesAndVotes(t *testing.T) {
	e := newInvEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C1"))
	require.NoError(t, err)
	msgs, err := ListPendingPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.NoError(t, RecordPollMessagePosted(e.ctx, e.pool, msgs[0].ID, "C1", "1700000000.000100"))

	pending, err := ListPendingPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Empty(t, pending)
	posted, err := ListPostedPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Len(t, posted, 1)
	assert.Equal(t, "1700000000.000100", posted[0].TS)

	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	pizza, taco := p.Options[0].ID, p.Options[1].ID
	// Ann picked both options, Ben only pizza.
	votes := []PollVote{
		{OptionID: pizza, SlackUserID: "U_ANN", DisplayName: "Ann"},
		{OptionID: taco, SlackUserID: "U_ANN", DisplayName: "Ann"},
		{OptionID: pizza, SlackUserID: "U_BEN", DisplayName: "Ben"},
		{OptionID: pizza, SlackUserID: "U_BEN", DisplayName: "Ben"}, // duplicate ignored
	}
	require.NoError(t, SavePollVotes(e.ctx, e.pool, id, votes))
	require.NoError(t, SavePollVotes(e.ctx, e.pool, id, votes), "saving again replaces, not doubles")

	p, err = GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, 2, p.Options[0].Votes)
	assert.Equal(t, 1, p.Options[1].Votes)
	assert.Equal(t, 2, p.Voters, "Ann counts once as a voter despite two options")

	got, err := ListPollVotes(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "Ann", got[0].DisplayName)
	assert.Equal(t, pizza, got[0].OptionID, "ordered by option position, then name")
}

func TestPollMessageFailureAndClose(t *testing.T) {
	e := newInvEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C1"))
	require.NoError(t, err)
	msgs, err := ListPendingPollMessages(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.NoError(t, MarkPollMessageFailed(e.ctx, e.pool, msgs[0].ID, "not_in_channel"))
	p, err := GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Failed)
	assert.Equal(t, 0, p.Posted)

	require.NoError(t, ClosePoll(e.ctx, e.pool, id))
	p, err = GetPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, PollClosed, p.Status)
	assert.NotNil(t, p.ClosedAt)
}

func TestCancelPollOnlyBeforeItCloses(t *testing.T) {
	e := newInvEnv(t)
	id, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C1"))
	require.NoError(t, err)
	ok, err := CancelPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = CancelPoll(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.False(t, ok, "already cancelled")

	other, err := CreatePoll(e.ctx, e.pool, pollInput(PollAudienceChannel, "C2"))
	require.NoError(t, err)
	require.NoError(t, ClosePoll(e.ctx, e.pool, other))
	ok, err = CancelPoll(e.ctx, e.pool, other)
	require.NoError(t, err)
	assert.False(t, ok, "a closed poll can't be cancelled")
}

func TestPollPermissionIsGrantedToAdminAndOwner(t *testing.T) {
	e := newInvEnv(t)
	rows, err := e.pool.Query(e.ctx, `
		SELECT r.name FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.key = 'poll.create' ORDER BY r.name`)
	require.NoError(t, err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	assert.Equal(t, []string{"admin", "owner"}, names)
}
