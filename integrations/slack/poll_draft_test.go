package slack

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/permissions"
	"commons/store"
)

func knownEmoji(n string) bool { return n != "nope" }

func TestParsePollOptionLines(t *testing.T) {
	cases := map[string][]PollOptionInput{
		":pizza: - Pizza\n:taco: - Tacos":           {{"pizza", "Pizza"}, {"taco", "Tacos"}},
		"- :pizza: Pizza\n• :taco:  Tacos":          {{"pizza", "Pizza"}, {"taco", "Tacos"}},
		":pizza:-Pizza":                             {{"pizza", "Pizza"}},
		":pizza: – Pizza and chips":                 {{"pizza", "Pizza and chips"}},
		":pizza: = Pizza":                           {{"pizza", "Pizza"}},
		":pizza: Pizza":                             {{"pizza", "Pizza"}},
		":Pizza: - Pizza":                           {{"pizza", "Pizza"}},
		"🍕 - Pizza\n🌮 Tacos":                        {{"pizza", "Pizza"}, {"taco", "Tacos"}},
		"\n  :pizza: - Pizza  \n\n:taco: - Tacos\n": {{"pizza", "Pizza"}, {"taco", "Tacos"}},
		":+1: - Yes\n:-1: - No":                     {{"+1", "Yes"}, {"-1", "No"}},
	}
	for in, want := range cases {
		got, problems := parsePollOptionLines(in, knownEmoji)
		assert.Empty(t, problems, in)
		assert.Equal(t, want, got, in)
	}

	bad := map[string]string{
		":nope: - Nothing":                     "I don't know the emoji",
		":pizza:":                              "say what :pizza: means",
		":pizza: -":                            "say what :pizza: means",
		"Fire hazard - bad":                    "start each option with an emoji",
		"pizza - Pizza":                        "start each option with an emoji",
		":pizza: - " + strings.Repeat("x", 81): "under 80 characters",
		":pizza Pizza":                         "I don't know the emoji",
	}
	for in, msg := range bad {
		got, problems := parsePollOptionLines(in, knownEmoji)
		assert.Empty(t, got, in)
		require.Len(t, problems, 1, in)
		assert.Contains(t, problems[0], msg, in)
	}

	got, problems := parsePollOptionLines(":pizza: - Pizza\n:nope: - X\n:taco: - Tacos", knownEmoji)
	assert.Len(t, got, 2, "one bad line doesn't lose the good ones")
	assert.Len(t, problems, 1)
}

func TestDurationText(t *testing.T) {
	assert.Equal(t, "2 hours", durationText(2*time.Hour))
	assert.Equal(t, "1 day 12 hours", durationText(36*time.Hour))
	assert.Equal(t, "30 minutes", durationText(30*time.Minute))
	assert.Equal(t, "1 hour 1 minute", durationText(61*time.Minute))
}

func (e *invEnv) lastCreatorDM(t *testing.T) invMessage {
	t.Helper()
	dms := e.creatorDMs()
	require.NotEmpty(t, dms)
	return dms[len(dms)-1]
}

func (e *invEnv) pollDraftOf(t *testing.T, slackUser string) *PollDraft {
	t.Helper()
	d, err := GetPollDraftBySlackID(e.ctx, e.pool, slackUser)
	require.NoError(t, err)
	return d
}

func (e *invEnv) noPollDraft(t *testing.T, slackUser string) {
	t.Helper()
	_, err := GetPollDraftBySlackID(e.ctx, e.pool, slackUser)
	require.Error(t, err)
}

func (e *invEnv) waitPollStep(t *testing.T, slackUser, step string) *PollDraft {
	t.Helper()
	var d *PollDraft
	require.Eventually(t, func() bool {
		var err error
		d, err = GetPollDraftBySlackID(e.ctx, e.pool, slackUser)
		return err == nil && d.Step == step
	}, 5*time.Second, 20*time.Millisecond, "draft should reach step %s", step)
	return d
}

func (e *invEnv) startPoll(t *testing.T, slackUser string) *PollDraft {
	t.Helper()
	e.click(t, slackUser, ActionPollNew, "open")
	return e.waitPollStep(t, slackUser, PollStepQuestion)
}

// pollAtOptions walks an author to the options step.
func (e *invEnv) pollAtOptions(t *testing.T, slackUser string) *PollDraft {
	t.Helper()
	e.startPoll(t, slackUser)
	e.dm(slackUser, "Lunch on Friday?")
	return e.waitPollStep(t, slackUser, PollStepOptions)
}

func (e *invEnv) pollAtAudience(t *testing.T, slackUser string) *PollDraft {
	t.Helper()
	e.pollAtOptions(t, slackUser)
	e.dm(slackUser, ":pizza: - Pizza\n:taco: - Tacos")
	e.click(t, slackUser, ActionPollOptionsDone, e.pollDraftOf(t, slackUser).ID)
	return e.waitPollStep(t, slackUser, PollStepAudience)
}

func (e *invEnv) chooseChannel(t *testing.T, slackUser, draftID, channel string) {
	t.Helper()
	e.dispatch(t, map[string]any{
		"type":       "block_actions",
		"user":       map[string]any{"id": slackUser},
		"trigger_id": "trig",
		"container":  map[string]any{"type": "message", "message_ts": "111.222", "channel_id": "D_CH"},
		"actions": []any{map[string]any{
			"type": "conversations_select", "block_id": pollAudienceBlockPrefix + draftID,
			"action_id": ActionPollAudienceChannel, "selected_conversation": channel,
		}},
	})
}

// pollAtConfirm walks an author to the preview, with a channel poll that closes after the given duration.
func (e *invEnv) pollAtConfirm(t *testing.T, slackUser, channel, duration string) *PollDraft {
	t.Helper()
	d := e.pollAtAudience(t, slackUser)
	e.chooseChannel(t, slackUser, d.ID, channel)
	e.waitPollStep(t, slackUser, PollStepDuration)
	e.dm(slackUser, duration)
	return e.waitPollStep(t, slackUser, PollStepConfirm)
}

func TestPollButtonStartsAConversation(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")

	d := e.startPoll(t, "U_PAT")

	require.Eventually(t, func() bool { return len(e.creatorDMs()) == 1 }, 5*time.Second, 20*time.Millisecond)
	prompt := e.creatorDMs()[0]
	assert.Contains(t, prompt.Text, "What's your question?")
	assert.Contains(t, prompt.Text, "30 minutes")
	assert.Contains(t, prompt.Blocks, d.ID, "Cancel carries the draft")
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), d.ExpiresAt, time.Minute)

	require.Len(t, e.fake.viewCalls("views.open"), 1)
	view := e.fake.lastView(t, "views.open")
	assert.Contains(t, view, "Check the Messages tab to write your poll")
	assert.NotContains(t, view, `"submit"`, "a notice, not a form")
}

func TestPollButtonRefusedWithoutPermission(t *testing.T) {
	e := closeEnv(t)
	e.user(t, "U_BEN", "Ben")

	e.click(t, "U_BEN", ActionPollNew, "open")

	require.Eventually(t, func() bool { return len(dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, dmsTo(e.fake.postsSnapshot(), "DM_U_BEN")[0].Text, "don't have permission to create polls")
	e.noPollDraft(t, "U_BEN")
	assert.Empty(t, e.fake.viewCalls("views.open"))
}

func TestPollButtonOnlyInAppHomeForThoseWhoMayCreate(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.user(t, "U_BEN", "Ben")

	Publish(e.ctx, e.pool, e.encKey, e.client, "U_PAT")
	assert.Contains(t, e.fake.lastView(t, "views.publish"), ActionPollNew)
	Publish(e.ctx, e.pool, e.encKey, e.client, "U_BEN")
	assert.NotContains(t, e.fake.lastView(t, "views.publish"), ActionPollNew)
}

func TestQuestionThenOptionsPrompt(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.startPoll(t, "U_PAT")

	e.dm("U_PAT", "Lunch on Friday?")

	d := e.waitPollStep(t, "U_PAT", PollStepOptions)
	assert.Equal(t, "Lunch on Friday?", d.Question)
	prompt := e.lastCreatorDM(t)
	assert.Contains(t, prompt.Text, "Now add the options")
	assert.Contains(t, prompt.Text, "Lunch on Friday?")
	assert.Contains(t, prompt.Text, ":pizza: - Pizza")
	assert.Contains(t, prompt.Text, "emoji picker")
	assert.Contains(t, prompt.Text, "2 to 6")
}

func TestQuestionTooLongStaysOnTheStep(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.startPoll(t, "U_PAT")

	e.dm("U_PAT", strings.Repeat("q", maxPollQuestionLen+1))

	assert.Equal(t, PollStepQuestion, e.pollDraftOf(t, "U_PAT").Step)
	assert.Contains(t, e.lastCreatorDM(t).Text, "Keep the question under 250 characters")
}

func TestOptionsInOneMessageAndAcrossMessages(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.pollAtOptions(t, "U_PAT")

	// One option isn't enough, so there is no Done yet.
	e.dm("U_PAT", ":pizza: - Pizza")
	first := e.lastCreatorDM(t)
	assert.Contains(t, first.Text, "Options so far (1)")
	assert.Contains(t, first.Text, ":pizza:  Pizza")
	assert.Contains(t, first.Text, "Add at least 2 options")
	assert.NotContains(t, first.Blocks, ActionPollOptionsDone)
	assert.Contains(t, first.Blocks, ActionPollOptionsReset)
	assert.Contains(t, first.Blocks, ActionPollDraftCancel)

	// A second message adds to the list, and a duplicate and an unknown emoji are reported, not lost.
	e.dm("U_PAT", ":taco: - Tacos\n:pizza: - Pizza again\n:nope: - Nothing")
	second := e.lastCreatorDM(t)
	assert.Contains(t, second.Text, "Options so far (2)")
	assert.Contains(t, second.Text, ":taco:  Tacos")
	assert.Contains(t, second.Text, "Already added: :pizza:")
	assert.Contains(t, second.Text, "I couldn't use")
	assert.Contains(t, second.Text, ":nope:")
	assert.Contains(t, second.Text, "press *Done*")
	assert.Contains(t, second.Blocks, ActionPollOptionsDone)

	d := e.pollDraftOf(t, "U_PAT")
	assert.Equal(t, []PollOptionInput{{"pizza", "Pizza"}, {"taco", "Tacos"}}, d.Options)
	assert.Equal(t, PollStepOptions, d.Step)
}

func TestOptionMessagesSentAtOnceAllLand(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.pollAtOptions(t, "U_PAT")

	var wg sync.WaitGroup
	for _, line := range []string{":pizza: - Pizza", ":taco: - Tacos", ":sushi: - Sushi", ":hamburger: - Burgers"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.dm("U_PAT", line)
		}()
	}
	wg.Wait()

	assert.Len(t, e.pollDraftOf(t, "U_PAT").Options, 4, "none overwrote another")
}

func TestSixOptionsMoveOnByThemselvesAndOverflowIsReported(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.pollAtOptions(t, "U_PAT")

	e.dm("U_PAT", ":one: - A\n:two: - B\n:three: - C\n:four: - D\n:five: - E\n:six: - F\n:seven: - G")

	d := e.waitPollStep(t, "U_PAT", PollStepAudience)
	assert.Len(t, d.Options, 6)
	dms := e.creatorDMs()
	require.GreaterOrEqual(t, len(dms), 2)
	list := dms[len(dms)-2]
	assert.Contains(t, list.Text, "A poll can have at most 6 options, so 1 weren't added")
	assert.Contains(t, list.Text, "on to the next step")
	assert.Contains(t, e.lastCreatorDM(t).Text, "Who should get this poll?")
}

func TestStartOverAndDone(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.pollAtOptions(t, "U_PAT")
	e.dm("U_PAT", ":pizza: - Pizza")
	id := e.pollDraftOf(t, "U_PAT").ID

	// Done with one option is refused.
	e.click(t, "U_PAT", ActionPollOptionsDone, id)
	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if strings.Contains(m.Text, "Add at least 2 options first") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, PollStepOptions, e.pollDraftOf(t, "U_PAT").Step)

	e.click(t, "U_PAT", ActionPollOptionsReset, id)
	require.Eventually(t, func() bool { return len(e.pollDraftOf(t, "U_PAT").Options) == 0 }, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, e.lastCreatorDM(t).Text, "Options cleared")
}

func TestAudienceStepOffersEveryoneAndAChannelPicker(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.pollAtAudience(t, "U_PAT")

	prompt := e.lastCreatorDM(t)
	assert.Contains(t, prompt.Text, "Who should get this poll?")
	assert.Contains(t, prompt.Blocks, "conversations_select")
	assert.Contains(t, prompt.Blocks, ActionPollAudienceEveryone)
	assert.Contains(t, prompt.Blocks, pollAudienceBlockPrefix+e.pollDraftOf(t, "U_PAT").ID)
}

func TestChoosingEveryoneOrAChannel(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.pollAtAudience(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollAudienceEveryone, d.ID)
	got := e.waitPollStep(t, "U_PAT", PollStepDuration)
	assert.Equal(t, PollAudienceEveryone, got.Audience)
	assert.Empty(t, got.ChannelID)
	assert.Contains(t, e.lastCreatorDM(t).Text, "How long should it stay open?")

	e2 := closeEnv(t)
	e2.grantPoll(t, "U_PAT", "Pat")
	d2 := e2.pollAtAudience(t, "U_PAT")
	e2.chooseChannel(t, "U_PAT", d2.ID, "C_LUNCH")
	got = e2.waitPollStep(t, "U_PAT", PollStepDuration)
	assert.Equal(t, PollAudienceChannel, got.Audience)
	assert.Equal(t, "C_LUNCH", got.ChannelID)
}

func TestDurationStepRejectsWhatItCannotUse(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.pollAtAudience(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollAudienceEveryone, d.ID)
	e.waitPollStep(t, "U_PAT", PollStepDuration)
	assert.Contains(t, e.lastCreatorDM(t).Blocks, ActionPollPickTime)

	for text, want := range map[string]string{
		"soonish": "couldn't read that as a duration",
		"10m":     "at least 30 minutes",
		"45d":     "30 days at most",
	} {
		e.dm("U_PAT", text)
		assert.Contains(t, e.lastCreatorDM(t).Text, want, text)
		assert.Equal(t, PollStepDuration, e.pollDraftOf(t, "U_PAT").Step, "stays on the step after %q", text)
	}
}

func TestPreviewShowsThePollAsMembersWillSeeIt(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")

	e.pollAtConfirm(t, "U_PAT", "C_LUNCH", "2h")

	preview := e.lastCreatorDM(t)
	body := unescape(preview.Blocks)
	assert.Contains(t, body, "Here's your poll")
	assert.Contains(t, body, "Lunch on Friday?")
	assert.Contains(t, body, ":pizza:  Pizza")
	assert.Contains(t, body, "React with the emoji for your answer")
	assert.Contains(t, body, "Goes to: <#C_LUNCH>")
	assert.Contains(t, body, "Closes 2 hours after you press Start poll")
	assert.Contains(t, body, ActionPollStart)
	assert.Contains(t, body, ActionPollDraftCancel)
	d := e.pollDraftOf(t, "U_PAT")
	assert.Equal(t, 7200, d.DurationSecs)
	assert.Nil(t, d.ClosesAt)
}

func TestPreviewForEveryoneCountsRecipients(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	for _, id := range []string{"U_ANN", "U_BEN"} {
		e.user(t, id, id)
		e.setStatus(t, id, "active")
	}
	d := e.pollAtAudience(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollAudienceEveryone, d.ID)
	e.waitPollStep(t, "U_PAT", PollStepDuration)
	e.dm("U_PAT", "1d")
	e.waitPollStep(t, "U_PAT", PollStepConfirm)

	assert.Contains(t, unescape(e.lastCreatorDM(t).Blocks), "Goes to: every active member (2)")
}

func TestStartPollCreatesAndPostsIt(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat Author")
	d := e.pollAtConfirm(t, "U_PAT", "C_LUNCH", "2h")

	e.click(t, "U_PAT", ActionPollStart, d.ID)

	require.Eventually(t, func() bool { return len(dmsTo(e.fake.postsSnapshot(), "C_LUNCH")) == 1 }, 5*time.Second, 20*time.Millisecond)
	e.noPollDraft(t, "U_PAT")
	polls, err := ListPolls(e.ctx, e.pool, 10)
	require.NoError(t, err)
	require.Len(t, polls, 1)
	assert.Equal(t, "Pat Author", polls[0].CreatorName)
	assert.Equal(t, "U_PAT", polls[0].CreatorSlackID)
	assert.Equal(t, "Lunch on Friday?", polls[0].Question)
	assert.Equal(t, PollAudienceChannel, polls[0].Audience)
	assert.Equal(t, "C_LUNCH", polls[0].ChannelID)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), polls[0].ClosesAt, time.Minute, "counted from when Start poll was pressed")
	full, err := GetPoll(e.ctx, e.pool, polls[0].ID)
	require.NoError(t, err)
	require.Len(t, full.Options, 2)
	assert.Equal(t, "pizza", full.Options[0].Emoji)
	assert.Equal(t, "Tacos", full.Options[1].Label)

	require.Eventually(t, func() bool {
		for _, u := range e.fake.updatesSnapshot() {
			if u.Channel == "D_CH" && strings.Contains(u.Text, "Starting your poll") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the preview is replaced")
}

func TestPickATimeOpensThePickerAndSavesIt(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.pollAtAudience(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollAudienceEveryone, d.ID)
	e.waitPollStep(t, "U_PAT", PollStepDuration)
	opened := len(e.fake.viewCalls("views.open"))

	e.click(t, "U_PAT", ActionPollPickTime, d.ID)
	require.Eventually(t, func() bool { return len(e.fake.viewCalls("views.open")) == opened+1 }, 5*time.Second, 20*time.Millisecond)
	view := e.fake.lastView(t, "views.open")
	assert.Contains(t, view, "datetimepicker")
	assert.Contains(t, view, d.ID)

	at := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	body, w := e.submitClosesAt(t, "U_PAT", d.ID, at.Unix())
	assert.Empty(t, w.Body.String(), "a valid time closes the modal")
	assert.Nil(t, pollClosesAtErrors(body))

	got := e.pollDraftOf(t, "U_PAT")
	assert.Equal(t, PollStepConfirm, got.Step)
	require.NotNil(t, got.ClosesAt)
	assert.WithinDuration(t, at, *got.ClosesAt, time.Second)
	assert.Zero(t, got.DurationSecs)
	assert.Contains(t, unescape(e.lastCreatorDM(t).Blocks), "Goes to: every active member")
	assert.NotContains(t, e.lastCreatorDM(t).Blocks, "after you press Start poll", "an exact time is not relative")
}

func (e *invEnv) submitClosesAt(t *testing.T, slackUser, draftID string, picked int64) ([]byte, *httptest.ResponseRecorder) {
	t.Helper()
	meta, err := json.Marshal(pollClosesAtMeta{Draft: draftID, Channel: "D_CH", TS: "111.222"})
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]any{
		"type": "view_submission",
		"user": map[string]any{"id": slackUser},
		"view": map[string]any{
			"id": "V8", "callback_id": ModalIDPollClosesAt, "private_metadata": string(meta),
			"state": map[string]any{"values": map[string]any{
				pollClosesAtBlock: map[string]any{pollClosesAtAction: map[string]any{"type": "datetimepicker", "selected_date_time": picked}},
			}},
		},
	})
	require.NoError(t, err)
	body := []byte(url.Values{"payload": {string(raw)}}.Encode())
	w := httptest.NewRecorder()
	dispatchInteractionBody(e.ctx, w, e.pool, e.encKey, nil, body)
	return body, w
}

func TestPickedTimeMustBeInTheWindow(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.pollAtAudience(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollAudienceEveryone, d.ID)
	e.waitPollStep(t, "U_PAT", PollStepDuration)

	for name, picked := range map[string]int64{
		"past":     time.Now().Add(-time.Hour).Unix(),
		"too soon": time.Now().Add(10 * time.Minute).Unix(),
		"too far":  time.Now().Add(45 * 24 * time.Hour).Unix(),
		"none":     0,
	} {
		body, w := e.submitClosesAt(t, "U_PAT", d.ID, picked)
		assert.Contains(t, w.Body.String(), pollClosesAtBlock, name)
		assert.NotNil(t, pollClosesAtErrors(body), "the webhook catches %s before the pipeline", name)
	}
	assert.Equal(t, PollStepDuration, e.pollDraftOf(t, "U_PAT").Step, "the draft waits for a good time")
}

func TestStartPollWithAStaleExactTimeAsksAgain(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.pollAtAudience(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollAudienceEveryone, d.ID)
	e.waitPollStep(t, "U_PAT", PollStepDuration)
	e.submitClosesAt(t, "U_PAT", d.ID, time.Now().Add(time.Hour).Unix())
	e.waitPollStep(t, "U_PAT", PollStepConfirm)
	_, err := e.pool.Exec(e.ctx, `UPDATE slack.poll_drafts SET closes_at = NOW() + interval '5 minutes'`)
	require.NoError(t, err)

	e.click(t, "U_PAT", ActionPollStart, d.ID)

	e.waitPollStep(t, "U_PAT", PollStepDuration)
	polls, err := ListPolls(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, polls)
	assert.Contains(t, e.lastCreatorDM(t).Text, "How long should it stay open?")
}

func TestTypingAtButtonStepsGivesAHint(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.pollAtAudience(t, "U_PAT")
	e.dm("U_PAT", "everyone please")
	assert.Contains(t, e.lastCreatorDM(t).Text, "Use the buttons above")
	assert.Equal(t, PollStepAudience, e.pollDraftOf(t, "U_PAT").Step)

	e2 := closeEnv(t)
	e2.grantPoll(t, "U_PAT", "Pat")
	e2.pollAtConfirm(t, "U_PAT", "C_LUNCH", "2h")
	e2.dm("U_PAT", "looks good")
	assert.Contains(t, e2.lastCreatorDM(t).Text, "Press *Start poll* or *Cancel*")
}

func TestCancelAtAnyStepDiscardsTheDraft(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.startPoll(t, "U_PAT")

	e.click(t, "U_PAT", ActionPollDraftCancel, d.ID)

	require.Eventually(t, func() bool { _, err := GetPollDraftBySlackID(e.ctx, e.pool, "U_PAT"); return err != nil }, 5*time.Second, 20*time.Millisecond)
	polls, err := ListPolls(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, polls)
}

func TestOnlyTheAuthorCanUseTheirDraftsButtons(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	e.grantPoll(t, "U_SAM", "Sam")
	d := e.pollAtConfirm(t, "U_PAT", "C_LUNCH", "2h")

	e.click(t, "U_SAM", ActionPollStart, d.ID)

	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if m.User == "U_SAM" {
				return strings.Contains(m.Text, "Only the author")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, PollStepConfirm, e.pollDraftOf(t, "U_PAT").Step)
	polls, err := ListPolls(e.ctx, e.pool, 10)
	require.NoError(t, err)
	assert.Empty(t, polls)
}

func TestButtonsFromAnEarlierStepSayTheStepIsDone(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.pollAtAudience(t, "U_PAT") // past the options step

	e.click(t, "U_PAT", ActionPollOptionsDone, d.ID)

	require.Eventually(t, func() bool {
		for _, m := range e.fake.ephemeralsSnapshot() {
			if strings.Contains(m.Text, "already moved past this step") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, PollStepAudience, e.pollDraftOf(t, "U_PAT").Step)
}

func TestExpiredPollConversation(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")
	d := e.startPoll(t, "U_PAT")
	_, err := e.pool.Exec(e.ctx, `UPDATE slack.poll_drafts SET expires_at = NOW() - interval '1 minute'`)
	require.NoError(t, err)

	e.dm("U_PAT", "too late")
	assert.Contains(t, e.lastCreatorDM(t).Text, "expired")
	e.noPollDraft(t, "U_PAT")

	// A button on an expired draft says so.
	d2 := e.startPoll(t, "U_PAT")
	_ = d
	_, err = e.pool.Exec(e.ctx, `UPDATE slack.poll_drafts SET expires_at = NOW() - interval '1 minute'`)
	require.NoError(t, err)
	e.click(t, "U_PAT", ActionPollDraftCancel, d2.ID)
	require.Eventually(t, func() bool {
		for _, u := range e.fake.updatesSnapshot() {
			if strings.Contains(u.Text, "expired") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}

func TestStartingOneConversationEndsTheOther(t *testing.T) {
	e := closeEnv(t)
	author := e.user(t, "U_PAT", "Pat")
	role, err := store.CreateRole(e.ctx, e.pool, "both-role", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(e.ctx, e.pool, role.ID, permissions.PollCreate))
	require.NoError(t, store.AddRolePermission(e.ctx, e.pool, role.ID, permissions.BroadcastSend))
	group, err := store.CreateRoleGroup(e.ctx, e.pool, "both-group", "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(e.ctx, e.pool, group.ID, role.ID))
	require.NoError(t, store.AssignGroupToUser(e.ctx, e.pool, author.ID, group.ID))

	e.click(t, "U_PAT", ActionBroadcastNew, "open")
	e.waitBroadcastDraft(t, "U_PAT")
	e.click(t, "U_PAT", ActionPollNew, "open")
	e.waitPollStep(t, "U_PAT", PollStepQuestion)
	_, err = GetBroadcastDraftBySlackID(e.ctx, e.pool, "U_PAT")
	assert.Error(t, err, "starting a poll discards the broadcast draft")

	e.click(t, "U_PAT", ActionBroadcastNew, "open")
	e.waitBroadcastDraft(t, "U_PAT")
	require.Eventually(t, func() bool { _, err := GetPollDraftBySlackID(e.ctx, e.pool, "U_PAT"); return err != nil }, 5*time.Second, 20*time.Millisecond)
}

func (e *invEnv) waitBroadcastDraft(t *testing.T, slackUser string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := GetBroadcastDraftBySlackID(e.ctx, e.pool, slackUser)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
}

func TestPollDraftExpiryWithoutADraftLeavesCommandsAlone(t *testing.T) {
	e := closeEnv(t)
	e.grantPoll(t, "U_PAT", "Pat")

	e.dm("U_PAT", "hello there")

	assert.Contains(t, e.lastCreatorDM(t).Text, "I'm not a chat bot")
}

func TestUpdatePollDraftIsAllOrNothing(t *testing.T) {
	e := closeEnv(t)
	author := e.grantPoll(t, "U_PAT", "Pat")
	id, err := StartPollDraft(e.ctx, e.pool, author.ID, "U_PAT", "Pat", time.Now().Add(time.Minute))
	require.NoError(t, err)

	_, err = UpdatePollDraft(e.ctx, e.pool, id, func(d *PollDraft) error {
		d.Question = "changed"
		return errPollStepChanged
	})
	assert.ErrorIs(t, err, errPollStepChanged)
	got, err := GetPollDraft(e.ctx, e.pool, id)
	require.NoError(t, err)
	assert.Empty(t, got.Question, "an error saves nothing")

	again, err := StartPollDraft(e.ctx, e.pool, author.ID, "U_PAT", "Pat", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, id, again, "one draft per author, reset when started again")
}

func TestOptionLinesAcceptSlackNamesMissingFromOtherEmojiSets(t *testing.T) {
	known := pollEmojiKnown(t.Context())
	got, problems := parsePollOptionLines(
		":ballot_box_with_ballot: - Vote\n:cheese_wedge: - Cheese\n:beach_with_umbrella: - Beach\n:thumbsup: - Yes\n👍🏽 - Same as thumbs up",
		known)
	assert.Equal(t, []string{"ballot_box_with_ballot", "cheese_wedge", "beach_with_umbrella", "thumbsup", "+1"},
		func() []string {
			var names []string
			for _, o := range got {
				names = append(names, o.Emoji)
			}
			return names
		}())
	assert.Empty(t, problems)
	assert.Equal(t, emojiKey("+1"), emojiKey("thumbsup"), "a skin-toned thumbs up is the thumbs up option")
}

func TestProblemLinesShowEmojiAsTheyWereTyped(t *testing.T) {
	_, problems := parsePollOptionLines(":pizza:", knownEmoji)
	require.Len(t, problems, 1)
	assert.NotContains(t, problems[0], "`", "no code formatting, so emoji render as emoji")
	assert.Contains(t, problems[0], "say what :pizza: means, like :pizza: - Pizza.")

	_, problems = parsePollOptionLines("<@U123> - hi", knownEmoji)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "&lt;@U123&gt;", "what was typed can't ping anyone")
}

// Messages use :shortcode: emoji that Slack must recognise, or they show up as literal text.
func TestEmojiShortcodesInMessagesAreSlackNames(t *testing.T) {
	loadStandardEmoji()
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_]):([a-z0-9_+\-]*[a-z][a-z0-9_+\-]*):`)
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	allowed := map[string]bool{"shortcode": true} // used in a comment
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			name := m[2]
			if allowed[name] {
				continue
			}
			_, ok := standardEmojiByName[name]
			assert.True(t, ok, "%s uses :%s:, which is not a Slack emoji name", f, name)
		}
	}
}
