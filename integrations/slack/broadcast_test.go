package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (e *invEnv) setStatus(t *testing.T, slackID, status string) {
	t.Helper()
	_, err := e.pool.Exec(e.ctx, `UPDATE user_identities SET platform_status = $2 WHERE provider = 'slack' AND external_id = $1`, slackID, status)
	require.NoError(t, err)
}

func broadcastEnv(t *testing.T) *invEnv {
	t.Helper()
	prev := broadcastInterval
	broadcastInterval = 0
	t.Cleanup(func() { broadcastInterval = prev })
	e := newInvEnv(t)
	e.usePackageClient(t)
	return e
}

func TestCreateBroadcastSnapshotsOnlyActiveNonBotMembers(t *testing.T) {
	e := broadcastEnv(t)
	e.user(t, "U_ANN", "Ann")
	e.user(t, "U_DEV", "Dev")
	e.user(t, "U_INV", "Inv")
	e.setStatus(t, "U_ANN", "active")
	e.setStatus(t, "U_DEV", "deactivated")
	e.setStatus(t, "U_INV", "invited")
	bot := e.user(t, "U_BOT", "Robot")
	e.setStatus(t, "U_BOT", "active")
	_, err := e.pool.Exec(e.ctx, `UPDATE users SET bot = TRUE WHERE id = $1`, bot.ID)
	require.NoError(t, err)
	author := e.user(t, "U_ADM", "Admin")

	n, err := CountBroadcastRecipients(e.ctx, e.pool)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	id, err := CreateBroadcast(e.ctx, e.pool, author.ID, "Admin", "hi", nil)
	require.NoError(t, err)
	recs, err := ListBroadcastRecipients(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, "Ann", recs[0].DisplayName)
	assert.Equal(t, RecipientPending, recs[0].Status)

	b, err := GetBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, author.ID, b.AuthorUserID)
	assert.Equal(t, "Admin", b.AuthorName)
	assert.Equal(t, 1, b.Pending)
}

func TestRunBroadcastSendsPersonalisedDMsAndCompletes(t *testing.T) {
	e := broadcastEnv(t)
	for slackID, name := range map[string]string{"U_ANN": "Ann", "U_BEN": "Ben"} {
		e.user(t, slackID, name)
		e.setStatus(t, slackID, "active")
	}
	id, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "Hello {{member_name}}!", nil)
	require.NoError(t, err)

	runBroadcast(e.ctx, e.pool, id)

	assert.Equal(t, "Hello Ann!", dmsTo(e.fake.postsSnapshot(), "DM_U_ANN")[0].Text)
	assert.Equal(t, "Hello Ben!", dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")[0].Text)
	b, err := GetBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, BroadcastDone, b.Status)
	assert.Equal(t, 2, b.Sent)
	assert.Equal(t, 0, b.Pending)
	assert.NotNil(t, b.CompletedAt)
}

func TestRunBroadcastResumeOnlyTouchesPendingRecipients(t *testing.T) {
	e := broadcastEnv(t)
	for slackID, name := range map[string]string{"U_ANN": "Ann", "U_BEN": "Ben"} {
		e.user(t, slackID, name)
		e.setStatus(t, slackID, "active")
	}
	id, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "hi", nil)
	require.NoError(t, err)
	recs, err := ListBroadcastRecipients(e.ctx, e.pool, id)
	require.NoError(t, err)
	for _, r := range recs {
		if r.DisplayName == "Ann" {
			require.NoError(t, MarkBroadcastRecipient(e.ctx, e.pool, r.ID, RecipientSent, ""))
		}
	}
	// A crash mid-send leaves the broadcast claimed; startup clears that.
	claimed, err := ClaimBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.True(t, claimed)
	ids, err := ListSendingBroadcastIDs(e.ctx, e.pool)
	require.NoError(t, err)
	require.Equal(t, []string{id}, ids)

	runBroadcast(e.ctx, e.pool, id)

	assert.Empty(t, dmsTo(e.fake.postsSnapshot(), "DM_U_ANN"), "already sent, must not be messaged again")
	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_BEN"), 1)
}

func TestClaimBroadcastIsExclusive(t *testing.T) {
	e := broadcastEnv(t)
	id, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "hi", nil)
	require.NoError(t, err)
	first, err := ClaimBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	second, err := ClaimBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.True(t, first)
	assert.False(t, second)
}

func TestResetFailedBroadcastRecipientsReopensBroadcast(t *testing.T) {
	e := broadcastEnv(t)
	e.user(t, "U_ANN", "Ann")
	e.setStatus(t, "U_ANN", "active")
	id, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "hi", nil)
	require.NoError(t, err)
	recs, err := ListBroadcastRecipients(e.ctx, e.pool, id)
	require.NoError(t, err)
	require.NoError(t, MarkBroadcastRecipient(e.ctx, e.pool, recs[0].ID, RecipientFailed, "channel_not_found"))
	require.NoError(t, CompleteBroadcast(e.ctx, e.pool, id))

	n, err := ResetFailedBroadcastRecipients(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	b, err := GetBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, BroadcastSending, b.Status)
	assert.Equal(t, 1, b.Pending)

	runBroadcast(e.ctx, e.pool, id)
	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_ANN"), 1)
}

func TestRenderBroadcastEscapesMemberName(t *testing.T) {
	assert.Equal(t, "Hi A &lt;@channel&gt;", renderBroadcast("Hi {{member_name}}", "A <@channel>"))
}

func TestScheduledBroadcastSnapshotsRecipientsWhenDue(t *testing.T) {
	e := broadcastEnv(t)
	e.user(t, "U_ANN", "Ann")
	e.setStatus(t, "U_ANN", "active")
	later := time.Now().Add(time.Hour)
	id, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "Hi {{member_name}}", &later)
	require.NoError(t, err)

	b, err := GetBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, BroadcastScheduled, b.Status)
	assert.Equal(t, 0, b.Total, "no recipients until it is due")

	ids, err := ActivateDueBroadcasts(e.ctx, e.pool)
	require.NoError(t, err)
	assert.Empty(t, ids, "not due yet")

	// A member who joins after scheduling but before it is due is included.
	e.user(t, "U_BEN", "Ben")
	e.setStatus(t, "U_BEN", "active")
	_, err = e.pool.Exec(e.ctx, `UPDATE slack.broadcasts SET scheduled_for = NOW() - interval '1 minute' WHERE id = $1`, id)
	require.NoError(t, err)

	ids, err = ActivateDueBroadcasts(e.ctx, e.pool)
	require.NoError(t, err)
	require.Equal(t, []string{id}, ids)
	again, err := ActivateDueBroadcasts(e.ctx, e.pool)
	require.NoError(t, err)
	assert.Empty(t, again, "activating twice must not start it twice")

	runBroadcast(e.ctx, e.pool, id)
	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_ANN"), 1)
	assert.Len(t, dmsTo(e.fake.postsSnapshot(), "DM_U_BEN"), 1)
	b, err = GetBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Equal(t, BroadcastDone, b.Status)
	assert.Equal(t, 2, b.Sent)
}

func TestCancelScheduledBroadcastOnlyBeforeItStarts(t *testing.T) {
	e := broadcastEnv(t)
	later := time.Now().Add(time.Hour)
	id, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "hi", &later)
	require.NoError(t, err)
	ok, err := CancelScheduledBroadcast(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = e.pool.Exec(e.ctx, `UPDATE slack.broadcasts SET scheduled_for = NOW() - interval '1 minute' WHERE id = $1`, id)
	require.NoError(t, err)
	ids, err := ActivateDueBroadcasts(e.ctx, e.pool)
	require.NoError(t, err)
	assert.Empty(t, ids, "a cancelled broadcast never starts")

	started, err := CreateBroadcast(e.ctx, e.pool, "", "Admin", "now", nil)
	require.NoError(t, err)
	ok, err = CancelScheduledBroadcast(e.ctx, e.pool, started)
	require.NoError(t, err)
	assert.False(t, ok, "one already sending can't be cancelled")
}
