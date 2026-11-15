package adminui

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slackpkg "commons/integrations/slack"
	"commons/internal/testhelpers"
	"commons/permissions"
)

func TestBroadcastPermissionGates(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	id, err := slackpkg.CreateBroadcast(t.Context(), pool, "", "Someone", "hi", nil)
	require.NoError(t, err)
	path := map[string]string{"id": id}

	none := invGrant(t, pool, "none", permissions.LibraryView)
	sender := invGrant(t, pool, "sender", permissions.BroadcastSend)

	cases := []struct {
		name string
		h    http.Handler
		rq   invReq
	}{
		{"history", d.BroadcastsPage(), invReq{method: "GET", target: "/admin/broadcasts"}},
		{"new", d.BroadcastNewPage(), invReq{method: "GET", target: "/admin/broadcasts/new"}},
		{"send", d.BroadcastSend(), invReq{method: "POST", target: "/admin/broadcasts", form: url.Values{"body": {"hi"}}}},
		{"detail", d.BroadcastDetailPage(), invReq{method: "GET", target: "/x", path: path}},
		{"fragment", d.BroadcastDetailFragment(), invReq{method: "GET", target: "/x", path: path}},
		{"retry", d.BroadcastRetry(), invReq{method: "POST", target: "/x", path: path}},
		{"cancel", d.BroadcastCancel(), invReq{method: "POST", target: "/x", path: path}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			denied := c.rq
			denied.userID = none
			assert.Equal(t, http.StatusForbidden, invDo(t, c.h, denied).Code, "without broadcast.send")

			allowed := c.rq
			allowed.userID = sender
			assert.NotEqual(t, http.StatusForbidden, invDo(t, c.h, allowed).Code, "with broadcast.send")
		})
	}
}

func TestBroadcastSendNeedsAMessageAndSlack(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	sender := invGrant(t, pool, "sender", permissions.BroadcastSend)

	w := invDo(t, d.BroadcastSend(), invReq{userID: sender, method: "POST", target: "/admin/broadcasts", form: url.Values{"body": {"  "}}})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Write a message first.")

	w = invDo(t, d.BroadcastSend(), invReq{userID: sender, method: "POST", target: "/admin/broadcasts", form: url.Values{"body": {"hello"}}})
	assert.Contains(t, w.Body.String(), "Slack is not connected.")
	list, err := slackpkg.ListBroadcasts(t.Context(), pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list, "nothing is recorded when Slack is not connected")
}

func TestBroadcastHistoryShowsAuthor(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	sender := invGrant(t, pool, "sender", permissions.BroadcastSend)
	id, err := slackpkg.CreateBroadcast(t.Context(), pool, sender, "Pat Author", "Potluck on Friday", nil)
	require.NoError(t, err)

	w := invDo(t, d.BroadcastsPage(), invReq{userID: sender, method: "GET", target: "/admin/broadcasts"})
	assert.Contains(t, w.Body.String(), "Pat Author")
	assert.Contains(t, w.Body.String(), "Potluck on Friday")

	w = invDo(t, d.BroadcastDetailPage(), invReq{userID: sender, method: "GET", target: "/x", path: map[string]string{"id": id}})
	assert.Contains(t, w.Body.String(), "Pat Author")

	w = invDo(t, d.BroadcastDetailPage(), invReq{userID: sender, method: "GET", target: "/x", path: map[string]string{"id": "00000000-0000-0000-0000-000000000000"}})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestParseBroadcastTimeReadsOrgTimezone(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	at, msg := parseBroadcastTime("2026-11-20T09:30", chicago, now)
	assert.Empty(t, msg)
	assert.Equal(t, time.Date(2026, 11, 20, 15, 30, 0, 0, time.UTC), at.UTC(), "9:30 Central in November is 15:30 UTC")

	_, msg = parseBroadcastTime("2026-11-14T05:00", chicago, now)
	assert.Equal(t, "The scheduled time must be in the future.", msg, "11:00 UTC is before now")
	_, msg = parseBroadcastTime("", chicago, now)
	assert.Equal(t, "Pick a date and time to schedule the message.", msg)
	_, msg = parseBroadcastTime("tomorrow", chicago, now)
	assert.Equal(t, "Pick a date and time to schedule the message.", msg)
}

func TestBroadcastScheduleRejectsBadTimes(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	sender := invGrant(t, pool, "sender", permissions.BroadcastSend)

	for name, sendAt := range map[string]string{"missing": "", "past": "2000-01-01T09:00"} {
		t.Run(name, func(t *testing.T) {
			w := invDo(t, d.BroadcastSend(), invReq{userID: sender, method: "POST", target: "/admin/broadcasts",
				form: url.Values{"body": {"hello"}, "mode": {"schedule"}, "send_at": {sendAt}}})
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), "hello", "the message is kept so it isn't lost")
			list, err := slackpkg.ListBroadcasts(t.Context(), pool, 10)
			require.NoError(t, err)
			assert.Empty(t, list)
		})
	}
}

func TestBroadcastCancelScheduled(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	sender := invGrant(t, pool, "sender", permissions.BroadcastSend)
	at := time.Now().Add(time.Hour)
	id, err := slackpkg.CreateBroadcast(t.Context(), pool, sender, "Pat", "later", &at)
	require.NoError(t, err)

	w := invDo(t, d.BroadcastDetailPage(), invReq{userID: sender, method: "GET", target: "/x", path: map[string]string{"id": id}})
	assert.Contains(t, w.Body.String(), "Cancel broadcast")

	w = invDo(t, d.BroadcastCancel(), invReq{userID: sender, method: "POST", target: "/x", path: map[string]string{"id": id}})
	assert.Equal(t, http.StatusSeeOther, w.Code)
	b, err := slackpkg.GetBroadcast(t.Context(), pool, id)
	require.NoError(t, err)
	assert.Equal(t, slackpkg.BroadcastCancelled, b.Status)
}
