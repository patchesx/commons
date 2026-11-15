package slack

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// A poll is written in a DM conversation, one step at a time: the question, the emoji options, who receives it,
// how long it stays open, then a preview. The modal this replaced had no emoji picker, so members had to know
// emoji names. In a message they can use Slack's own picker.
const (
	ActionPollDraftCancel      = "poll_draft_cancel"
	ActionPollOptionsDone      = "poll_options_done"
	ActionPollOptionsReset     = "poll_options_reset"
	ActionPollAudienceEveryone = "poll_audience_everyone"
	ActionPollAudienceChannel  = "poll_audience_channel"
	ActionPollPickTime         = "poll_pick_time"
	ActionPollStart            = "poll_draft_start"

	// pollDraftWindow is how long a draft waits for its next step before it lapses.
	pollDraftWindow = 30 * time.Minute

	// pollAudienceBlockPrefix starts the block ID of the audience step's buttons and picker, followed by the draft
	// ID, because a picker's value is the channel and has no room for it.
	pollAudienceBlockPrefix = "poll_audience:"
)

// interactiveSendInterval spaces replies in a conversation, where the usual 5 seconds between DMs to one
// member would feel broken. Override in tests.
var interactiveSendInterval = time.Second

var errPollStepChanged = errors.New("poll draft is no longer at this step")

// replyDM sends a conversation message to the author.
func replyDM(ctx context.Context, client *slacklib.Client, slackID, text string, blocks ...slacklib.Block) {
	if _, _, err := sendDMEvery(ctx, client, slackID, interactiveSendInterval, text, blocks...); err != nil {
		log.Printf("slack/poll: message %s: %v", slackID, err)
	}
}

func pollPlain(s string) *slacklib.TextBlockObject {
	return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, false, false)
}

func pollButton(action, draftID, label string) *slacklib.ButtonBlockElement {
	return slacklib.NewButtonBlockElement(action, draftID, pollPlain(label))
}

// stepBlocks is a message with buttons under it.
func stepBlocks(text, blockID string, elements ...slacklib.BlockElement) []slacklib.Block {
	return []slacklib.Block{
		slacklib.NewSectionBlock(pollMD(text), nil, nil),
		slacklib.NewActionBlock(blockID, elements...),
	}
}

// pollAuthor returns the member behind a Slack user if they may create polls.
func pollAuthor(ctx context.Context, pool *pgxpool.Pool, slackID string) (*store.User, bool) {
	u, err := store.GetUserByExternalID(ctx, pool, "slack", slackID)
	if err != nil || u == nil {
		return nil, false
	}
	return u, canCreatePoll(ctx, pool, u.ID)
}

// startPollDraft answers the App Home button: it begins a draft, tells the author where to look, and sends the
// first question. The pop-up opens before the DM because Slack's trigger ID only works for three seconds.
func startPollDraft(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, slackID, triggerID string) {
	author, ok := pollAuthor(ctx, pool, slackID)
	if !ok {
		SendDM(ctx, client, slackID, "You don't have permission to create polls.")
		return
	}
	DeleteBroadcastDraftBySlackID(ctx, pool, slackID) // one thing at a time in this conversation
	id, err := StartPollDraft(ctx, pool, author.ID, slackID, author.DisplayName, time.Now().Add(pollDraftWindow))
	if err != nil {
		log.Printf("slack/poll: start draft for %s: %v", slackID, err)
		SendDM(ctx, client, slackID, "I couldn't start a poll right now. Please try again.")
		return
	}
	if triggerID != "" {
		if _, err := client.OpenViewContext(ctx, triggerID, pollPromptModal()); err != nil {
			log.Printf("slack/poll: open prompt modal: %v", err)
		}
	}
	text := fmt.Sprintf(":bar_chart: *What's your question?* Send it as a message here. Then I'll ask for the emoji options, "+
		"who should get the poll, and how long it stays open. Each step waits %d minutes.", int(pollDraftWindow.Minutes()))
	replyDM(ctx, client, slackID, text, stepBlocks(text, "poll_step", pollButton(ActionPollDraftCancel, id, "Cancel"))...)
}

// pollPromptModal points the author at the Messages tab, where the conversation happens.
func pollPromptModal() slacklib.ModalViewRequest {
	return messagesTabModal("Emoji straw poll", ":bar_chart: *Check the Messages tab to write your poll.*",
		"I've sent you a message there. Reply to it with your question, and I'll take you through the rest.")
}

// handlePollDraftMessage takes a DM as the author's answer to the current step if they are in the middle of a
// draft. It reports whether it handled the message, so ordinary DM commands are skipped.
func handlePollDraftMessage(ctx context.Context, d dmContext, text string) bool {
	draft, err := GetPollDraftBySlackID(ctx, d.pool, d.slackUserID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("slack/poll: look up draft for %s: %v", d.slackUserID, err)
		}
		return false
	}
	if time.Now().After(draft.ExpiresAt) {
		DeletePollDraft(ctx, d.pool, draft.ID)
		d.reply(ctx, "Your poll expired, so that message wasn't used. Click *Emoji Straw Poll* on the App Home to start again.")
		return true
	}
	if _, ok := pollAuthor(ctx, d.pool, d.slackUserID); !ok {
		DeletePollDraft(ctx, d.pool, draft.ID)
		d.reply(ctx, "You don't have permission to create polls.")
		return true
	}
	text = strings.TrimSpace(text)

	switch draft.Step {
	case PollStepQuestion:
		answerQuestion(ctx, d, draft, text)
	case PollStepOptions:
		answerOptions(ctx, d, draft, text)
	case PollStepDuration:
		answerDuration(ctx, d, draft, text)
	case PollStepAudience:
		replyDM(ctx, d.client, d.slackUserID, "Use the buttons above to choose who gets the poll.")
	default:
		replyDM(ctx, d.client, d.slackUserID, "Press *Start poll* or *Cancel* on the preview above.")
	}
	return true
}

func answerQuestion(ctx context.Context, d dmContext, draft *PollDraft, text string) {
	if n := len([]rune(text)); n > maxPollQuestionLen {
		replyDM(ctx, d.client, d.slackUserID, fmt.Sprintf("Keep the question under %d characters. That one is %d. Send a shorter one.", maxPollQuestionLen, n))
		return
	}
	updated, err := UpdatePollDraft(ctx, d.pool, draft.ID, func(dr *PollDraft) error {
		if dr.Step != PollStepQuestion {
			return errPollStepChanged
		}
		dr.Question, dr.Step, dr.ExpiresAt = text, PollStepOptions, time.Now().Add(pollDraftWindow)
		return nil
	})
	if err != nil {
		log.Printf("slack/poll: save question for %s: %v", d.slackUserID, err)
		return
	}
	msg := fmt.Sprintf(":ballot_box_with_ballot: *Now add the options* for “%s”. Send each as an emoji, a dash, then what it means, like :pizza: - Pizza. "+
		"Put one per line in a single message, or send several messages. Use Slack's emoji picker to find emoji, including your custom ones. "+
		"You can add %d to %d.", escapeMrkdwn(updated.Question), minPollOptions, maxPollOptions)
	replyDM(ctx, d.client, d.slackUserID, msg, stepBlocks(msg, "poll_step", pollButton(ActionPollDraftCancel, updated.ID, "Cancel"))...)
}

// optionLines lists a draft's options.
func optionLines(options []PollOptionInput) string {
	var b strings.Builder
	for i, o := range options {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, ":%s:  %s", o.Emoji, escapeMrkdwn(o.Label))
	}
	return b.String()
}

func answerOptions(ctx context.Context, d dmContext, draft *PollDraft, text string) {
	options, problems := parsePollOptionLines(text, pollEmojiKnown(ctx))
	var duplicates []string
	overflow := 0
	updated, err := UpdatePollDraft(ctx, d.pool, draft.ID, func(dr *PollDraft) error {
		if dr.Step != PollStepOptions {
			return errPollStepChanged
		}
		have := map[string]bool{}
		for _, o := range dr.Options {
			have[emojiKey(o.Emoji)] = true
		}
		for _, o := range options {
			switch k := emojiKey(o.Emoji); {
			case len(dr.Options) >= maxPollOptions:
				overflow++
			case have[k]:
				duplicates = append(duplicates, ":"+o.Emoji+":")
			default:
				have[k] = true
				dr.Options = append(dr.Options, o)
			}
		}
		dr.ExpiresAt = time.Now().Add(pollDraftWindow)
		if len(dr.Options) >= maxPollOptions {
			dr.Step = PollStepAudience
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, errPollStepChanged) {
			log.Printf("slack/poll: save options for %s: %v", d.slackUserID, err)
		}
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, ":ballot_box_with_ballot: *Options so far (%d):*\n%s", len(updated.Options), optionLines(updated.Options))
	if len(updated.Options) == 0 {
		b.Reset()
		b.WriteString(":ballot_box_with_ballot: *No options yet.*")
	}
	if len(problems) > 0 {
		b.WriteString("\n:warning: I couldn't use:\n• " + strings.Join(problems, "\n• "))
	}
	if len(duplicates) > 0 {
		b.WriteString("\n:warning: Already added: " + strings.Join(duplicates, " "))
	}
	if overflow > 0 {
		fmt.Fprintf(&b, "\n:warning: A poll can have at most %d options, so %d weren't added.", maxPollOptions, overflow)
	}

	if len(updated.Options) >= maxPollOptions {
		b.WriteString("\nThat's the maximum, so on to the next step.")
		replyDM(ctx, d.client, d.slackUserID, b.String(), slacklib.NewSectionBlock(pollMD(b.String()), nil, nil))
		sendAudienceStep(ctx, d.client, updated)
		return
	}
	var buttons []slacklib.BlockElement
	if len(updated.Options) >= minPollOptions {
		b.WriteString("\nSend more, or press *Done* when you have them all.")
		done := pollButton(ActionPollOptionsDone, updated.ID, "Done")
		done.Style = slacklib.StylePrimary
		buttons = append(buttons, done)
	} else {
		fmt.Fprintf(&b, "\nAdd at least %d options.", minPollOptions)
	}
	if len(updated.Options) > 0 {
		buttons = append(buttons, pollButton(ActionPollOptionsReset, updated.ID, "Start over"))
	}
	buttons = append(buttons, pollButton(ActionPollDraftCancel, updated.ID, "Cancel"))
	replyDM(ctx, d.client, d.slackUserID, b.String(), stepBlocks(b.String(), "poll_step", buttons...)...)
}

// sendAudienceStep asks who receives the poll, with a button for everyone and a picker for a channel.
func sendAudienceStep(ctx context.Context, client *slacklib.Client, draft *PollDraft) {
	text := ":mailbox: *Who should get this poll?* Send it to every active member as a DM, or post it in a channel."
	picker := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeConversations, pollPlain("Or choose a channel"), ActionPollAudienceChannel)
	picker.Filter = &slacklib.SelectBlockElementFilter{
		Include:                       []string{"public", "private"},
		ExcludeBotUsers:               true,
		ExcludeExternalSharedChannels: true,
	}
	everyone := pollButton(ActionPollAudienceEveryone, draft.ID, "Everyone")
	cancel := pollButton(ActionPollDraftCancel, draft.ID, "Cancel")
	replyDM(ctx, client, draft.AuthorSlackID, text, stepBlocks(text, pollAudienceBlockPrefix+draft.ID, picker, everyone, cancel)...)
}

// sendDurationStep asks how long the poll stays open.
func sendDurationStep(ctx context.Context, client *slacklib.Client, draft *PollDraft) {
	text := fmt.Sprintf(":hourglass: *How long should it stay open?* Reply with a duration like `90m`, `2h` or `3d` "+
		"(%s to %s), or pick an exact time.", "30 minutes", "30 days")
	replyDM(ctx, client, draft.AuthorSlackID, text, stepBlocks(text, "poll_step",
		pollButton(ActionPollPickTime, draft.ID, "Pick a date and time"), pollButton(ActionPollDraftCancel, draft.ID, "Cancel"))...)
}

func answerDuration(ctx context.Context, d dmContext, draft *PollDraft, text string) {
	dur, err := parsePollDuration(text)
	if err != nil {
		replyDM(ctx, d.client, d.slackUserID, "I couldn't read that as a duration. Try `90m`, `2h` or `3d`, or press *Pick a date and time*.")
		return
	}
	if msg := pollWindowError(time.Now().Add(dur), time.Now()); msg != "" {
		replyDM(ctx, d.client, d.slackUserID, msg+" Try another duration.")
		return
	}
	updated, err := UpdatePollDraft(ctx, d.pool, draft.ID, func(dr *PollDraft) error {
		if dr.Step != PollStepDuration {
			return errPollStepChanged
		}
		dr.DurationSecs, dr.ClosesAt, dr.Step, dr.ExpiresAt = int(dur.Seconds()), nil, PollStepConfirm, time.Now().Add(pollDraftWindow)
		return nil
	})
	if err != nil {
		if !errors.Is(err, errPollStepChanged) {
			log.Printf("slack/poll: save duration for %s: %v", d.slackUserID, err)
		}
		return
	}
	sendPollPreview(ctx, d.pool, d.client, updated)
}

// draftClosesAt is when the poll would close if started at now: the exact time the author picked, or the
// duration they gave counted from now.
func draftClosesAt(d *PollDraft, now time.Time) time.Time {
	if d.ClosesAt != nil {
		return *d.ClosesAt
	}
	return now.Add(time.Duration(d.DurationSecs) * time.Second)
}

// sendPollPreview shows the author the poll as members will see it, with Start poll and Cancel.
func sendPollPreview(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, draft *PollDraft) {
	now := time.Now()
	closes := draftClosesAt(draft, now)
	p := &Poll{Question: draft.Question, CreatorSlackID: draft.AuthorSlackID, ClosesAt: closes}
	for i, o := range draft.Options {
		p.Options = append(p.Options, PollOption{Position: i, Emoji: o.Emoji, Label: o.Label})
	}
	loc := pollLocation(ctx)
	_, pollBlocks := pollOpenMessage(p, loc)

	where := "<#" + draft.ChannelID + ">"
	if draft.Audience == PollAudienceEveryone {
		n, err := CountBroadcastRecipients(ctx, pool)
		if err != nil {
			log.Printf("slack/poll: count recipients: %v", err)
		}
		where = fmt.Sprintf("every active member (%d)", n)
	}
	closing := "Closes " + pollDateToken(closes, loc)
	if draft.ClosesAt == nil {
		closing = fmt.Sprintf("Closes %s after you press Start poll", durationText(time.Duration(draft.DurationSecs)*time.Second))
	}

	start := pollButton(ActionPollStart, draft.ID, "Start poll")
	start.Style = slacklib.StylePrimary
	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(pollMD(":eyes: *Here's your poll.* Check it, then press *Start poll*."), nil, nil),
		slacklib.NewDividerBlock(),
	}
	blocks = append(blocks, pollBlocks...)
	blocks = append(blocks,
		slacklib.NewDividerBlock(),
		slacklib.NewContextBlock("", pollMD("Goes to: "+where+" · "+closing)),
		slacklib.NewActionBlock("poll_preview", start, pollButton(ActionPollDraftCancel, draft.ID, "Cancel")),
	)
	replyDM(ctx, client, draft.AuthorSlackID, "Poll preview", blocks...)
}

// durationText says a duration in words, like "2 hours" or "1 day 12 hours".
func durationText(d time.Duration) string {
	units := []struct {
		name string
		dur  time.Duration
	}{{"day", 24 * time.Hour}, {"hour", time.Hour}, {"minute", time.Minute}}
	var parts []string
	for _, u := range units {
		if n := int(d / u.dur); n > 0 {
			d -= time.Duration(n) * u.dur
			s := fmt.Sprintf("%d %s", n, u.name)
			if n != 1 {
				s += "s"
			}
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// loadOwnedPollDraft finds a draft for a button click, and tells the author if it can't be used: gone, someone
// else's, or expired.
func loadOwnedPollDraft(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, draftID string) *PollDraft {
	channelID, ts := messageRef(payload)
	draft, err := GetPollDraft(ctx, pool, draftID)
	if err != nil {
		editSection(ctx, client, channelID, ts, "This poll is no longer available. Click *Emoji Straw Poll* on the App Home to start again.")
		return nil
	}
	if draft.AuthorSlackID != payload.User.ID {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "Only the author of this poll can do that.")
		return nil
	}
	if time.Now().After(draft.ExpiresAt) {
		DeletePollDraft(ctx, pool, draft.ID)
		editSection(ctx, client, channelID, ts, "This poll expired. Click *Emoji Straw Poll* on the App Home to start again.")
		return nil
	}
	return draft
}

// handlePollDraftAction runs the buttons and the channel picker in the poll conversation.
func handlePollDraftAction(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, action slacklib.BlockAction) {
	draftID := action.Value
	if action.ActionID == ActionPollAudienceChannel {
		draftID = strings.TrimPrefix(action.BlockID, pollAudienceBlockPrefix)
	}
	draft := loadOwnedPollDraft(ctx, pool, client, payload, draftID)
	if draft == nil {
		return
	}
	channelID, ts := messageRef(payload)
	slackID := draft.AuthorSlackID
	wrongStep := func() {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "You've already moved past this step.")
	}

	switch action.ActionID {
	case ActionPollDraftCancel:
		DeletePollDraft(ctx, pool, draft.ID)
		editSection(ctx, client, channelID, ts, ":wastebasket: Poll cancelled. Nothing was sent.")

	case ActionPollOptionsReset:
		if _, err := UpdatePollDraft(ctx, pool, draft.ID, func(dr *PollDraft) error {
			if dr.Step != PollStepOptions {
				return errPollStepChanged
			}
			dr.Options, dr.ExpiresAt = nil, time.Now().Add(pollDraftWindow)
			return nil
		}); err != nil {
			wrongStep()
			return
		}
		replyDM(ctx, client, slackID, ":wastebasket: Options cleared. Send the options again, like :pizza: - Pizza.")

	case ActionPollOptionsDone:
		updated, err := UpdatePollDraft(ctx, pool, draft.ID, func(dr *PollDraft) error {
			if dr.Step != PollStepOptions {
				return errPollStepChanged
			}
			if len(dr.Options) < minPollOptions {
				return errTooFewOptions
			}
			dr.Step, dr.ExpiresAt = PollStepAudience, time.Now().Add(pollDraftWindow)
			return nil
		})
		switch {
		case errors.Is(err, errTooFewOptions):
			tellOnlyYou(ctx, client, channelID, payload.User.ID, fmt.Sprintf("Add at least %d options first.", minPollOptions))
		case err != nil:
			wrongStep()
		default:
			sendAudienceStep(ctx, client, updated)
		}

	case ActionPollAudienceEveryone, ActionPollAudienceChannel:
		audience, channel := PollAudienceEveryone, ""
		summary := "every active member"
		if action.ActionID == ActionPollAudienceChannel {
			audience, channel = PollAudienceChannel, action.SelectedConversation
			summary = "<#" + channel + ">"
		}
		updated, err := UpdatePollDraft(ctx, pool, draft.ID, func(dr *PollDraft) error {
			if dr.Step != PollStepAudience {
				return errPollStepChanged
			}
			dr.Audience, dr.ChannelID, dr.Step, dr.ExpiresAt = audience, channel, PollStepDuration, time.Now().Add(pollDraftWindow)
			return nil
		})
		if err != nil {
			wrongStep()
			return
		}
		editSection(ctx, client, channelID, ts, ":mailbox: Sending to "+summary+".")
		sendDurationStep(ctx, client, updated)

	case ActionPollPickTime:
		if draft.Step != PollStepDuration {
			wrongStep()
			return
		}
		modal := PollClosesAtModal(pollClosesAtMeta{Draft: draft.ID, Channel: channelID, TS: ts})
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/poll: open time picker: %v", err)
		}

	case ActionPollStart:
		startDraftedPoll(ctx, pool, client, payload, draft)
	}
}

var errTooFewOptions = errors.New("too few options")

// startDraftedPoll creates the poll from a confirmed draft and starts posting it.
func startDraftedPoll(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, draft *PollDraft) {
	channelID, ts := messageRef(payload)
	if draft.Step != PollStepConfirm {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "You've already moved past this step.")
		return
	}
	author, ok := pollAuthor(ctx, pool, draft.AuthorSlackID)
	if !ok {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "You don't have permission to create polls.")
		return
	}
	if !BroadcastConfigured(ctx) {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "Slack isn't connected, so the poll can't be started.")
		return
	}
	now := time.Now()
	closes := draftClosesAt(draft, now)
	if msg := pollWindowError(closes, now); msg != "" {
		// An exact time that has since gone by, or got too close, needs choosing again.
		if _, err := UpdatePollDraft(ctx, pool, draft.ID, func(dr *PollDraft) error {
			dr.Step, dr.ExpiresAt = PollStepDuration, now.Add(pollDraftWindow)
			return nil
		}); err == nil {
			editSection(ctx, client, channelID, ts, ":warning: "+msg)
			sendDurationStep(ctx, client, draft)
		}
		return
	}
	id, err := CreatePoll(ctx, pool, CreatePollInput{
		CreatorUserID:  author.ID,
		CreatorName:    author.DisplayName,
		CreatorSlackID: draft.AuthorSlackID,
		Question:       draft.Question,
		Audience:       draft.Audience,
		ChannelID:      draft.ChannelID,
		ClosesAt:       closes,
		Options:        draft.Options,
	})
	if err != nil {
		log.Printf("slack/poll: create poll from draft %s: %v", draft.ID, err)
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "The poll couldn't be created. Please try again.")
		return
	}
	StartPollPosting(pool, id)
	DeletePollDraft(ctx, pool, draft.ID)
	editSection(ctx, client, channelID, ts, ":white_check_mark: Starting your poll…")
}
