package slack

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slacklib "github.com/slack-go/slack"

	"commons/permissions"
	"commons/store"
)

func (e *invEnv) grantBroadcast(t *testing.T, slackID, name string) *store.User {
	t.Helper()
	u := e.user(t, slackID, name)
	role, err := store.CreateRole(e.ctx, e.pool, "role-"+slackID, "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(e.ctx, e.pool, role.ID, permissions.BroadcastSend))
	group, err := store.CreateRoleGroup(e.ctx, e.pool, "group-"+slackID, "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(e.ctx, e.pool, group.ID, role.ID))
	require.NoError(t, store.AssignGroupToUser(e.ctx, e.pool, u.ID, group.ID))
	return u
}

// dm delivers a plain DM from a member to the app.
func (e *invEnv) dm(slackUser, text string) {
	handleDirectMessage(e.ctx, e.pool, e.encKey, e.client, slackEventInner{
		Type: "message", ChannelType: "im", User: json.RawMessage(`"` + slackUser + `"`), Text: text,
	})
}

func (e *invEnv) draftOf(t *testing.T, slackUser string) *BroadcastDraft {
	t.Helper()
	d, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, slackUser)
	require.NoError(t, err)
	return d
}

func (e *invEnv) noDraft(t *testing.T, slackUser string) {
	t.Helper()
	_, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, slackUser)
	require.Error(t, err)
}

// draftReady walks an author through the button and the message, ending with a ready draft.
func (e *invEnv) draftReady(t *testing.T, slackUser, body string) *BroadcastDraft {
	t.Helper()
	e.click(t, slackUser, ActionBroadcastNew, "open")
	require.Eventually(t, func() bool {
		d, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, slackUser)
		return err == nil && d.State == DraftAwaiting
	}, 5*time.Second, 20*time.Millisecond)
	e.dm(slackUser, body)
	d := e.draftOf(t, slackUser)
	require.Equal(t, DraftReady, d.State)
	return d
}

func TestBroadcastButtonOnlyForThoseWhoMayBroadcast(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	e.user(t, "U_BEN", "Ben")

	Publish(e.ctx, e.pool, e.encKey, e.client, "U_PAT")
	assert.Contains(t, e.fake.lastView(t, "views.publish"), ActionBroadcastNew)
	Publish(e.ctx, e.pool, e.encKey, e.client, "U_BEN")
	assert.NotContains(t, e.fake.lastView(t, "views.publish"), ActionBroadcastNew)
}

func TestBroadcastButtonDMsAPromptAndStartsADraft(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")

	e.click(t, "U_PAT", ActionBroadcastNew, "open")

	require.Eventually(t, func() bool { return len(e.creatorDMs()) == 1 }, 5*time.Second, 20*time.Millisecond)
	prompt := e.creatorDMs()[0]
	assert.Contains(t, prompt.Text, "Write your broadcast")
	assert.Contains(t, prompt.Text, "The following is a broadcast message from <@U_PAT>:")
	assert.Contains(t, prompt.Text, "10 minutes")
	d := e.draftOf(t, "U_PAT")
	assert.Equal(t, DraftAwaiting, d.State)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), d.ExpiresAt, time.Minute)
	assert.Contains(t, prompt.Blocks, d.ID, "the Cancel button carries the draft")

	require.Len(t, e.fake.viewCalls("views.open"), 1)
	view := e.fake.lastView(t, "views.open")
	assert.Contains(t, view, "Check the Messages tab to write your broadcast")
	assert.Contains(t, view, "Got it")
	assert.NotContains(t, view, `"submit"`, "it is a notice, not a form")
}

func TestBroadcastButtonRefusedWithoutPermission(t *testing.T) {
	e := broadcastEnv(t)
	e.user(t, "U_BEN", "Ben")

	e.click(t, "U_BEN", ActionBroadcastNew, "open")

	require.Eventually(t, func() bool { return len(dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")[0].Text, "don't have permission")
	e.noDraft(t, "U_BEN")
	assert.Empty(t, e.fake.viewCalls("views.open"), "no pop-up pointing at a prompt that was never sent")
}

func TestNextMessageBecomesTheDraftAndIsPreviewed(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat Author")
	for _, id := range []string{"U_ANN", "U_BEN"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}

	d := e.draftReady(t, "U_PAT", "Hello {{member_name}}, potluck *Friday*")

	assert.Equal(t, "Hello {{member_name}}, potluck *Friday*", d.Body)
	dms := e.creatorDMs()
	require.Len(t, dms, 2, "the prompt, then the preview")
	preview := dms[1]
	assert.Contains(t, preview.Blocks, "This will go to *2 members*")
	assert.Contains(t, unescape(preview.Blocks), "The following is a broadcast message from <@U_PAT>:\\nHello Pat Author, potluck *Friday*", "the name is filled in with the author's own")
	for _, want := range []string{ActionBroadcastSend, ActionBroadcastSchedule, ActionBroadcastDiscard, "Send now", "Schedule", "Discard"} {
		assert.Contains(t, preview.Blocks, want)
	}
	assert.NotContains(t, strings.Join(e.fake.postTexts(), "\n"), "not a chat bot", "it isn't answered with the command help")

	// Another message replaces the draft and previews again.
	e.dm("U_PAT", "Changed my mind")
	assert.Equal(t, "Changed my mind", e.draftOf(t, "U_PAT").Body)
	assert.Len(t, e.creatorDMs(), 3)
}

func TestMessagesAreCommandsAgainWithoutADraft(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")

	e.dm("U_PAT", "hello there")

	dms := e.creatorDMs()
	require.Len(t, dms, 1)
	assert.Contains(t, dms[0].Text, "I'm not a chat bot")
	e.noDraft(t, "U_PAT")
}

func TestExpiredPromptIsNotTakenAsADraft(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	e.click(t, "U_PAT", ActionBroadcastNew, "open")
	require.Eventually(t, func() bool { return len(e.creatorDMs()) == 1 }, 5*time.Second, 20*time.Millisecond)
	_, err := e.pool.Exec(e.ctx, `UPDATE slack.broadcast_drafts SET expires_at = NOW() - interval '1 minute'`)
	require.NoError(t, err)

	e.dm("U_PAT", "too late")

	dms := e.creatorDMs()
	require.Len(t, dms, 2)
	assert.Contains(t, dms[1].Text, "expired")
	e.noDraft(t, "U_PAT")
}

func TestSendNowBroadcastsWithThePrefixAndRecordsTheAuthor(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat Author")
	for slackID, name := range map[string]string{"U_ANN": "Ann", "U_BEN": "Ben"} {
		e.user(t, slackID, name)
		e.setStatus(t, slackID, "active")
	}
	d := e.draftReady(t, "U_PAT", "Hello {{member_name}}!")

	e.click(t, "U_PAT", ActionBroadcastSend, d.ID)

	require.Eventually(t, func() bool {
		return len(dmsTo(e.fake.postsSnapshot(), "DM_U_ANN")) == 1 && len(dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")) == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "The following is a broadcast message from <@U_PAT>:\nHello Ann!", dmsTo(e.fake.postsSnapshot(), "DM_U_ANN")[0].Text)
	assert.Equal(t, "The following is a broadcast message from <@U_PAT>:\nHello Ben!", dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")[0].Text)

	e.noDraft(t, "U_PAT")
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "Pat Author", list[0].AuthorName, "it appears in the history under its author")
	assert.NotEmpty(t, list[0].AuthorUserID)
	assert.Equal(t, "The following is a broadcast message from <@U_PAT>:\nHello {{member_name}}!", list[0].Body)

	require.Eventually(t, func() bool {
		for _, u := range e.fake.updatesSnapshot() {
			if u.Channel == "D_CH" && strings.Contains(u.Text, "Broadcast is being sent") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the preview message is replaced with the outcome")
}

func TestOnlyTheAuthorCanActOnADraft(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	e.grantBroadcast(t, "U_SAM", "Sam")
	d := e.draftReady(t, "U_PAT", "Hello")

	e.click(t, "U_SAM", ActionBroadcastSend, d.ID)

	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if m.User == "U_SAM" {
				return strings.Contains(m.Text, "Only the author")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, DraftReady, e.draftOf(t, "U_PAT").State, "the draft is untouched")
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestDiscardAndCancelRemoveTheDraft(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")

	// Cancel on the prompt, before a message.
	e.click(t, "U_PAT", ActionBroadcastNew, "open")
	require.Eventually(t, func() bool {
		d, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, "U_PAT")
		return err == nil && d.State == DraftAwaiting
	}, 5*time.Second, 20*time.Millisecond)
	e.click(t, "U_PAT", ActionBroadcastDiscard, e.draftOf(t, "U_PAT").ID)
	require.Eventually(t, func() bool {
		_, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, "U_PAT")
		return err != nil
	}, 5*time.Second, 20*time.Millisecond)

	// Discard on the preview.
	d := e.draftReady(t, "U_PAT", "Hello")
	e.click(t, "U_PAT", ActionBroadcastDiscard, d.ID)
	require.Eventually(t, func() bool {
		_, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, "U_PAT")
		return err != nil
	}, 5*time.Second, 20*time.Millisecond)
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list, "nothing was sent")
}

func TestSendAndScheduleNeedAMessageFirst(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	e.click(t, "U_PAT", ActionBroadcastNew, "open")
	require.Eventually(t, func() bool {
		d, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, "U_PAT")
		return err == nil && d.State == DraftAwaiting
	}, 5*time.Second, 20*time.Millisecond)
	d := e.draftOf(t, "U_PAT")

	e.click(t, "U_PAT", ActionBroadcastSend, d.ID)

	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if m.User == "U_PAT" {
				return strings.Contains(m.Text, "Send me the message")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestExpiredDraftCannotBeSent(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	d := e.draftReady(t, "U_PAT", "Hello")
	_, err := e.pool.Exec(e.ctx, `UPDATE slack.broadcast_drafts SET expires_at = NOW() - interval '1 minute'`)
	require.NoError(t, err)

	e.click(t, "U_PAT", ActionBroadcastSend, d.ID)

	require.Eventually(t, func() bool {
		for _, u := range e.fake.updatesSnapshot() {
			if strings.Contains(u.Text, "expired") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
	e.noDraft(t, "U_PAT")
}

func scheduleSubmission(t *testing.T, e *invEnv, slackUser, draftID string, picked int64) ([]byte, *httptest.ResponseRecorder) {
	t.Helper()
	meta, err := json.Marshal(broadcastScheduleMeta{Draft: draftID, Channel: "D_CH", TS: "111.222"})
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{
		"type": "view_submission",
		"user": map[string]any{"id": slackUser},
		"view": map[string]any{
			"id": "V9", "callback_id": ModalIDBroadcastSchedule, "private_metadata": string(meta),
			"state": map[string]any{"values": map[string]any{
				broadcastScheduleBlock: map[string]any{broadcastScheduleAction: map[string]any{"type": "datetimepicker", "selected_date_time": picked}},
			}},
		},
	})
	require.NoError(t, err)
	body := []byte(url.Values{"payload": {string(raw)}}.Encode())
	w := httptest.NewRecorder()
	dispatchInteractionBody(e.ctx, w, e.pool, e.encKey, nil, body)
	return body, w
}

func TestScheduleButtonOpensTheDatePicker(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	d := e.draftReady(t, "U_PAT", "Hello")
	opened := len(e.fake.viewCalls("views.open")) // the Messages tab pop-up from starting the draft

	e.click(t, "U_PAT", ActionBroadcastSchedule, d.ID)

	require.Eventually(t, func() bool { return len(e.fake.viewCalls("views.open")) == opened+1 }, 5*time.Second, 20*time.Millisecond)
	view := e.fake.lastView(t, "views.open")
	assert.Contains(t, view, "datetimepicker")
	assert.Contains(t, view, "Schedule message")
	assert.Contains(t, view, d.ID, "the draft rides along in the modal")
}

func TestScheduleSubmissionSchedulesTheDraft(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat Author")
	d := e.draftReady(t, "U_PAT", "Hello {{member_name}}")
	at := time.Now().Add(3 * time.Hour).Truncate(time.Second)

	_, w := scheduleSubmission(t, e, "U_PAT", d.ID, at.Unix())

	assert.Empty(t, w.Body.String(), "a valid time closes the modal")
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	b := list[0]
	assert.Equal(t, BroadcastScheduled, b.Status)
	require.NotNil(t, b.ScheduledFor)
	assert.WithinDuration(t, at, *b.ScheduledFor, time.Second)
	assert.Equal(t, "Pat Author", b.AuthorName)
	assert.Equal(t, "The following is a broadcast message from <@U_PAT>:\nHello {{member_name}}", b.Body)
	assert.Zero(t, b.Total, "recipients are chosen when it is due")
	e.noDraft(t, "U_PAT")

	var edited bool
	for _, u := range e.fake.updatesSnapshot() {
		edited = edited || (u.Channel == "D_CH" && u.TS == "111.222" && strings.Contains(u.Text, "Broadcast scheduled for"))
	}
	assert.True(t, edited, "the preview message says it is scheduled")
}

func TestScheduleSubmissionRejectsABadTime(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	d := e.draftReady(t, "U_PAT", "Hello")

	body, w := scheduleSubmission(t, e, "U_PAT", d.ID, time.Now().Add(-time.Hour).Unix())
	assert.Contains(t, w.Body.String(), broadcastScheduleBlock)
	assert.Contains(t, w.Body.String(), "future")
	assert.Contains(t, broadcastScheduleErrors(body), broadcastScheduleBlock, "the webhook finds the same error before the pipeline")

	body, w = scheduleSubmission(t, e, "U_PAT", d.ID, 0)
	assert.Contains(t, w.Body.String(), "Pick a date and time")
	assert.NotNil(t, broadcastScheduleErrors(body))

	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.Equal(t, DraftReady, e.draftOf(t, "U_PAT").State, "the draft survives so they can pick again")

	good, _ := scheduleSubmission(t, e, "U_PAT", "00000000-0000-0000-0000-000000000000", time.Now().Add(time.Hour).Unix())
	assert.Nil(t, broadcastScheduleErrors(good), "a good time passes the webhook check")
}

func TestScheduleSubmissionFromSomeoneElseIsRefused(t *testing.T) {
	e := broadcastEnv(t)
	e.grantBroadcast(t, "U_PAT", "Pat")
	e.grantBroadcast(t, "U_SAM", "Sam")
	d := e.draftReady(t, "U_PAT", "Hello")

	_, w := scheduleSubmission(t, e, "U_SAM", d.ID, time.Now().Add(time.Hour).Unix())

	assert.Contains(t, w.Body.String(), "expired")
	list, err := ListBroadcasts(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestStartingADraftReplacesTheOldOne(t *testing.T) {
	e := broadcastEnv(t)
	author := e.grantBroadcast(t, "U_PAT", "Pat")
	first, err := StartBroadcastDraft(e.ctx, e.pool, author.ID, "U_PAT", "Pat", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, SetBroadcastDraftBody(e.ctx, e.pool, first, "old text", time.Now().Add(time.Minute)))

	second, err := StartBroadcastDraft(e.ctx, e.pool, author.ID, "U_PAT", "Pat", time.Now().Add(10*time.Minute))
	require.NoError(t, err)

	d := e.draftOf(t, "U_PAT")
	assert.Equal(t, DraftAwaiting, d.State)
	assert.Empty(t, d.Body, "the earlier text is gone")
	assert.Equal(t, first, second, "one draft per author")
}

func TestParseBroadcastSchedule(t *testing.T) {
	now := time.Now()
	values := func(picked int64) map[string]map[string]slacklib.BlockAction {
		return map[string]map[string]slacklib.BlockAction{broadcastScheduleBlock: {broadcastScheduleAction: {SelectedDateTime: picked}}}
	}
	at, errs := parseBroadcastSchedule(values(now.Add(time.Hour).Unix()), now)
	assert.Nil(t, errs)
	assert.WithinDuration(t, now.Add(time.Hour), at, time.Second)
	_, errs = parseBroadcastSchedule(values(now.Add(-time.Second).Unix()), now)
	assert.NotNil(t, errs)
	_, errs = parseBroadcastSchedule(values(0), now)
	assert.NotNil(t, errs)
}
