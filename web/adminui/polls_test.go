package adminui

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slackpkg "commons/integrations/slack"
	"commons/internal/testhelpers"
	"commons/permissions"
)

func newPoll(t *testing.T, d Deps, creator, question string) string {
	t.Helper()
	id, err := slackpkg.CreatePoll(t.Context(), d.Pool, slackpkg.CreatePollInput{
		CreatorName:    creator,
		CreatorSlackID: "U_PAT",
		Question:       question,
		Audience:       slackpkg.PollAudienceChannel,
		ChannelID:      "C_LUNCH",
		ClosesAt:       time.Now().Add(time.Hour),
		Options:        []slackpkg.PollOptionInput{{Emoji: "pizza", Label: "Pizza"}, {Emoji: "partyparrot", Label: "Tacos"}},
	})
	require.NoError(t, err)
	return id
}

func TestPollPermissionGates(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	id := newPoll(t, d, "Pat", "Lunch?")
	path := map[string]string{"id": id}
	none := invGrant(t, pool, "none", permissions.LibraryView)
	broadcaster := invGrant(t, pool, "broadcaster", permissions.BroadcastSend)
	holder := invGrant(t, pool, "holder", permissions.PollCreate)

	cases := []struct {
		name string
		h    http.Handler
		rq   invReq
	}{
		{"history", d.PollsPage(), invReq{method: "GET", target: "/admin/polls"}},
		{"detail", d.PollDetailPage(), invReq{method: "GET", target: "/x", path: path}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for name, user := range map[string]string{"no permission": none, "only broadcast.send": broadcaster} {
				denied := c.rq
				denied.userID = user
				assert.Equal(t, http.StatusForbidden, invDo(t, c.h, denied).Code, name)
			}
			allowed := c.rq
			allowed.userID = holder
			assert.Equal(t, http.StatusOK, invDo(t, c.h, allowed).Code, "with poll.create")
		})
	}
}

func TestPollHistoryListsCreatorQuestionAndStatus(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	holder := invGrant(t, pool, "holder", permissions.PollCreate)
	open := newPoll(t, d, "Pat Author", "Lunch on Friday?")
	cancelled := newPoll(t, d, "Sam Other", "Pick a colour")
	require.NoError(t, slackpkg.MarkPollOpen(t.Context(), pool, open))
	_, err := slackpkg.CancelPoll(t.Context(), pool, cancelled)
	require.NoError(t, err)

	w := invDo(t, d.PollsPage(), invReq{userID: holder, method: "GET", target: "/admin/polls"})
	body := w.Body.String()
	assert.Contains(t, body, "Pat Author")
	assert.Contains(t, body, "Lunch on Friday?")
	assert.Contains(t, body, "Sam Other")
	assert.Contains(t, body, "open")
	assert.Contains(t, body, "cancelled")
}

func TestPollDetailShowsResultsOfAClosedPoll(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	holder := invGrant(t, pool, "holder", permissions.PollCreate)
	id := newPoll(t, d, "Pat Author", "Lunch on Friday?")
	p, err := slackpkg.GetPoll(t.Context(), pool, id)
	require.NoError(t, err)
	pizza, taco := p.Options[0].ID, p.Options[1].ID
	require.NoError(t, slackpkg.SavePollVotes(t.Context(), pool, id, []slackpkg.PollVote{
		{OptionID: pizza, SlackUserID: "U_ANN", DisplayName: "Ann Secret"},
		{OptionID: taco, SlackUserID: "U_ANN", DisplayName: "Ann Secret"},
		{OptionID: pizza, SlackUserID: "U_BEN", DisplayName: "Ben Secret"},
		{OptionID: pizza, SlackUserID: "U_CAL", DisplayName: "Cal Secret"},
	}))
	require.NoError(t, slackpkg.MarkPollOpen(t.Context(), pool, id))
	require.NoError(t, slackpkg.ClosePoll(t.Context(), pool, id))

	w := invDo(t, d.PollDetailPage(), invReq{userID: holder, method: "GET", target: "/x", path: map[string]string{"id": id}})
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Lunch on Friday?")
	assert.Contains(t, body, "Pat Author")
	assert.Contains(t, body, "🍕", "a standard emoji shows as its character")
	assert.Contains(t, body, ":partyparrot:", "a custom emoji shows as its name")
	assert.Contains(t, body, "3 (75%)", "pizza has 3 of 4 votes")
	assert.Contains(t, body, "1 (25%)")
	assert.Contains(t, body, "3 voted", "Ann, Ben and Cal are 3 members")
	assert.Contains(t, body, "1 members picked more than one option")
	assert.Contains(t, body, "C_LUNCH")
	for _, name := range []string{"Ann Secret", "Ben Secret", "Cal Secret", "U_ANN"} {
		assert.NotContains(t, body, name, "the page shows how many voted, never who voted for what")
	}
}

func TestPollDetailBeforeAndAfterTally(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	holder := invGrant(t, pool, "holder", permissions.PollCreate)

	openID := newPoll(t, d, "Pat", "Still open?")
	require.NoError(t, slackpkg.MarkPollOpen(t.Context(), pool, openID))
	w := invDo(t, d.PollDetailPage(), invReq{userID: holder, method: "GET", target: "/x", path: map[string]string{"id": openID}})
	assert.Contains(t, w.Body.String(), "Results appear here once the poll closes")
	assert.NotContains(t, w.Body.String(), "(0%)")

	cancelledID := newPoll(t, d, "Pat", "Never mind?")
	_, err := slackpkg.CancelPoll(t.Context(), pool, cancelledID)
	require.NoError(t, err)
	w = invDo(t, d.PollDetailPage(), invReq{userID: holder, method: "GET", target: "/x", path: map[string]string{"id": cancelledID}})
	assert.Contains(t, w.Body.String(), "cancelled, so no results were tallied")

	w = invDo(t, d.PollDetailPage(), invReq{userID: holder, method: "GET", target: "/x", path: map[string]string{"id": "00000000-0000-0000-0000-000000000000"}})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPollDetailTimesAreInTheOrgTimezone(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	holder := invGrant(t, pool, "holder", permissions.PollCreate)
	id := newPoll(t, d, "Pat", "Lunch?")
	closes := time.Date(2026, 12, 1, 21, 30, 0, 0, time.UTC)
	_, err := pool.Exec(t.Context(), `UPDATE slack.polls SET closes_at = $2 WHERE id = $1`, id, closes)
	require.NoError(t, err)

	w := invDo(t, d.PollDetailPage(), invReq{userID: holder, method: "GET", target: "/x", path: map[string]string{"id": id}})
	assert.Contains(t, w.Body.String(), "Dec 1, 2026 3:30 PM CST", "21:30 UTC is 3:30 PM Central, the default org timezone")
}
