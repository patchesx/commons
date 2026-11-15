package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/permissions"
	"commons/store"
)

const (
	ActionBroadcastNew      = "broadcast_new"
	ActionBroadcastSend     = "broadcast_draft_send"
	ActionBroadcastSchedule = "broadcast_draft_schedule"
	ActionBroadcastDiscard  = "broadcast_draft_discard"

	ModalIDBroadcastSchedule = "broadcast_schedule_submit"

	broadcastScheduleBlock  = "bc_send_at"
	broadcastScheduleAction = "bc_send_at_input"

	// draftWindow is how long a draft waits for its next step before it lapses.
	draftWindow = 10 * time.Minute

	// maxPreviewLen keeps a long message inside Slack's limit for one block of text.
	maxPreviewLen = 2800
)

// broadcastScheduleMeta rides along with the schedule modal so its submission can find the draft and edit the
// preview message the button was on.
type broadcastScheduleMeta struct {
	Draft   string `json:"d"`
	Channel string `json:"c"`
	TS      string `json:"t"`
}

// slackBroadcastBody is what members receive: a line saying who it is from, then the message.
func slackBroadcastBody(authorSlackID, body string) string {
	return "The following is a broadcast message from <@" + authorSlackID + ">:\n" + body
}

// broadcastAuthor returns the member behind a Slack user if they may send broadcasts.
func broadcastAuthor(ctx context.Context, pool *pgxpool.Pool, slackID string) (*store.User, bool) {
	u, err := store.GetUserByExternalID(ctx, pool, "slack", slackID)
	if err != nil || u == nil {
		return nil, false
	}
	perms, err := store.GetUserPermissions(ctx, pool, u.ID)
	if err != nil {
		log.Printf("slack/broadcast: load permissions for %s: %v", u.ID, err)
		return nil, false
	}
	return u, hasPermission(perms, permissions.BroadcastSend)
}

func editSection(ctx context.Context, client *slacklib.Client, channelID, ts, text string) {
	editMessage(ctx, client, channelID, ts, text, []slacklib.Block{slacklib.NewSectionBlock(pollMD(text), nil, nil)})
}

// BroadcastPromptModal points the author at the Messages tab, where the prompt is waiting. The prompt is a DM,
// which is easy to miss on mobile when the button is pressed on the Home tab.
func BroadcastPromptModal() slacklib.ModalViewRequest {
	return messagesTabModal("Broadcast", ":mega: *Check the Messages tab to write your broadcast.*",
		"I've sent you a message there. Reply to it with what you want to say, and I'll show you a preview before anything goes out.")
}

// messagesTabModal is a notice, with nothing to fill in, that the app has sent a message and where to find it.
func messagesTabModal(title, headline, detail string) slacklib.ModalViewRequest {
	return slacklib.ModalViewRequest{
		Type:  slacklib.VTModal,
		Title: pollPlain(title),
		Close: pollPlain("Got it"),
		Blocks: slacklib.Blocks{BlockSet: []slacklib.Block{
			slacklib.NewSectionBlock(pollMD(headline), nil, nil),
			slacklib.NewSectionBlock(pollMD(detail+"\n\nThe Messages tab is next to Home at the top of this app."), nil, nil),
		}},
	}
}

// startBroadcastDraft answers the App Home Broadcast button: it begins a draft, tells the author where to look,
// and DMs the prompt. The modal is opened before the DM because Slack's trigger ID only works for three
// seconds, and sending can wait on rate limiting.
func startBroadcastDraft(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, slackID, triggerID string) {
	author, ok := broadcastAuthor(ctx, pool, slackID)
	if !ok {
		SendDM(ctx, client, slackID, "You don't have permission to send broadcasts.")
		return
	}
	DeletePollDraftBySlackID(ctx, pool, slackID) // one thing at a time in this conversation
	id, err := StartBroadcastDraft(ctx, pool, author.ID, slackID, author.DisplayName, time.Now().Add(draftWindow))
	if err != nil {
		log.Printf("slack/broadcast: start draft for %s: %v", slackID, err)
		SendDM(ctx, client, slackID, "I couldn't start a broadcast right now. Please try again.")
		return
	}
	if triggerID != "" {
		if _, err := client.OpenViewContext(ctx, triggerID, BroadcastPromptModal()); err != nil {
			log.Printf("slack/broadcast: open prompt modal: %v", err)
		}
	}
	plain := func(s string) *slacklib.TextBlockObject {
		return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, false, false)
	}
	text := fmt.Sprintf(":mega: *Write your broadcast.* Send me the message here and I'll show you a preview. "+
		"It goes to every active member, starting with “The following is a broadcast message from <@%s>:”. "+
		"You can use `{{member_name}}` to address each member by name. This prompt expires in %d minutes.", slackID, int(draftWindow.Minutes()))
	cancel := slacklib.NewButtonBlockElement(ActionBroadcastDiscard, id, plain("Cancel"))
	if _, _, err := SendDM(ctx, client, slackID, text, slacklib.NewSectionBlock(pollMD(text), nil, nil),
		slacklib.NewActionBlock("broadcast_prompt", cancel)); err != nil {
		log.Printf("slack/broadcast: send prompt to %s: %v", slackID, err)
	}
}

// handleBroadcastDraftMessage takes a DM as the author's draft if they are in the middle of one. It reports
// whether it handled the message, so ordinary DM commands are skipped.
func handleBroadcastDraftMessage(ctx context.Context, d dmContext, text string) bool {
	draft, err := GetBroadcastDraftBySlackID(ctx, d.pool, d.slackUserID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("slack/broadcast: look up draft for %s: %v", d.slackUserID, err)
		}
		return false
	}
	if time.Now().After(draft.ExpiresAt) {
		DeleteBroadcastDraft(ctx, d.pool, draft.ID)
		d.reply(ctx, "Your broadcast prompt expired, so that message wasn't used. Click *Broadcast* on the App Home to start again.")
		return true
	}
	if _, ok := broadcastAuthor(ctx, d.pool, d.slackUserID); !ok {
		DeleteBroadcastDraft(ctx, d.pool, draft.ID)
		d.reply(ctx, "You don't have permission to send broadcasts.")
		return true
	}
	body := strings.TrimSpace(text)
	if err := SetBroadcastDraftBody(ctx, d.pool, draft.ID, body, time.Now().Add(draftWindow)); err != nil {
		log.Printf("slack/broadcast: save draft for %s: %v", d.slackUserID, err)
		d.reply(ctx, "I couldn't save that message. Please send it again.")
		return true
	}
	sendBroadcastPreview(ctx, d.pool, d.client, draft, body)
	return true
}

// sendBroadcastPreview DMs the author how the message will look, to how many, with Send now, Schedule and Discard.
func sendBroadcastPreview(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, draft *BroadcastDraft, body string) {
	recipients, err := CountBroadcastRecipients(ctx, pool)
	if err != nil {
		log.Printf("slack/broadcast: count recipients: %v", err)
	}
	preview := renderBroadcast(slackBroadcastBody(draft.AuthorSlackID, body), draft.AuthorName)
	if r := []rune(preview); len(r) > maxPreviewLen {
		preview = string(r[:maxPreviewLen]) + "…"
	}
	plain := func(s string) *slacklib.TextBlockObject {
		return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, false, false)
	}
	sendBtn := slacklib.NewButtonBlockElement(ActionBroadcastSend, draft.ID, plain("Send now"))
	sendBtn.Style = slacklib.StylePrimary
	sendBtn.Confirm = slacklib.NewConfirmationBlockObject(plain("Send this broadcast?"),
		plain(fmt.Sprintf("It will be sent as a DM to %d members right away and can't be recalled.", recipients)), plain("Send now"), plain("Not yet"))
	scheduleBtn := slacklib.NewButtonBlockElement(ActionBroadcastSchedule, draft.ID, plain("Schedule"))
	discardBtn := slacklib.NewButtonBlockElement(ActionBroadcastDiscard, draft.ID, plain("Discard"))
	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(pollMD(fmt.Sprintf("*Preview.* This will go to *%d members*. Shown with your name where `{{member_name}}` appears. Send another message to replace it.", recipients)), nil, nil),
		slacklib.NewDividerBlock(),
		slacklib.NewSectionBlock(pollMD(preview), nil, nil),
		slacklib.NewDividerBlock(),
		slacklib.NewActionBlock("broadcast_preview", sendBtn, scheduleBtn, discardBtn),
	}
	if _, _, err := SendDM(ctx, client, draft.AuthorSlackID, "Broadcast preview", blocks...); err != nil {
		log.Printf("slack/broadcast: send preview to %s: %v", draft.AuthorSlackID, err)
	}
}

// BroadcastScheduleModal asks when to send a drafted broadcast.
func BroadcastScheduleModal(meta broadcastScheduleMeta) slacklib.ModalViewRequest {
	plain := func(s string) *slacklib.TextBlockObject {
		return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, false, false)
	}
	raw, _ := json.Marshal(meta)
	picker := slacklib.NewDateTimePickerBlockElement(broadcastScheduleAction)
	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		CallbackID:      ModalIDBroadcastSchedule,
		PrivateMetadata: string(raw),
		Title:           plain("Schedule broadcast"),
		Submit:          plain("Schedule message"),
		Close:           plain("Back"),
		Blocks: slacklib.Blocks{BlockSet: []slacklib.Block{
			slacklib.NewSectionBlock(pollMD("Pick when the broadcast goes out. The time is shown in your own timezone."), nil, nil),
			slacklib.NewInputBlock(broadcastScheduleBlock, plain("Send at"), nil, picker),
		}},
	}
}

// parseBroadcastSchedule reads the picked time and reports why it can't be used.
func parseBroadcastSchedule(values map[string]map[string]slacklib.BlockAction, now time.Time) (time.Time, map[string]string) {
	picked := values[broadcastScheduleBlock][broadcastScheduleAction].SelectedDateTime
	if picked == 0 {
		return time.Time{}, map[string]string{broadcastScheduleBlock: "Pick a date and time."}
	}
	at := time.Unix(picked, 0)
	if !at.After(now) {
		return time.Time{}, map[string]string{broadcastScheduleBlock: "The time must be in the future."}
	}
	return at, nil
}

// broadcastScheduleErrors validates a schedule modal submission, and returns nil for anything else or a valid
// one. Like pollClosesAtErrors, the interactions webhook calls it before answering Slack.
func broadcastScheduleErrors(body []byte) map[string]string {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil
	}
	var payload slacklib.InteractionCallback
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil {
		return nil
	}
	if payload.Type != slacklib.InteractionTypeViewSubmission || payload.View.CallbackID != ModalIDBroadcastSchedule {
		return nil
	}
	_, errs := parseBroadcastSchedule(payload.View.State.Values, time.Now())
	return errs
}

// loadOwnedDraft finds a draft for a button click, and tells the author if it can't be used: gone, someone
// else's, or expired.
func loadOwnedDraft(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, draftID string) *BroadcastDraft {
	channelID, ts := messageRef(payload)
	draft, err := GetBroadcastDraft(ctx, pool, draftID)
	if err != nil {
		editSection(ctx, client, channelID, ts, "This broadcast is no longer available. Click *Broadcast* on the App Home to start again.")
		return nil
	}
	if draft.AuthorSlackID != payload.User.ID {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "Only the author of this broadcast can do that.")
		return nil
	}
	if time.Now().After(draft.ExpiresAt) {
		DeleteBroadcastDraft(ctx, pool, draft.ID)
		editSection(ctx, client, channelID, ts, "This broadcast expired. Click *Broadcast* on the App Home to start again.")
		return nil
	}
	return draft
}

// handleBroadcastDraftAction runs the Send now, Schedule and Discard buttons, and Cancel on the prompt.
func handleBroadcastDraftAction(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, actionID, draftID string) {
	draft := loadOwnedDraft(ctx, pool, client, payload, draftID)
	if draft == nil {
		return
	}
	channelID, ts := messageRef(payload)

	if actionID == ActionBroadcastDiscard {
		DeleteBroadcastDraft(ctx, pool, draft.ID)
		editSection(ctx, client, channelID, ts, ":wastebasket: Broadcast discarded. Nothing was sent.")
		return
	}
	if draft.State != DraftReady {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "Send me the message you want to broadcast first.")
		return
	}
	if _, ok := broadcastAuthor(ctx, pool, draft.AuthorSlackID); !ok {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "You don't have permission to send broadcasts.")
		return
	}

	if actionID == ActionBroadcastSchedule {
		modal := BroadcastScheduleModal(broadcastScheduleMeta{Draft: draft.ID, Channel: channelID, TS: ts})
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/broadcast: open schedule modal: %v", err)
		}
		return
	}

	// Send now.
	if !BroadcastConfigured(ctx) {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "Slack isn't connected, so the broadcast can't be sent.")
		return
	}
	id, err := CreateBroadcast(ctx, pool, draft.AuthorUserID, draft.AuthorName, slackBroadcastBody(draft.AuthorSlackID, draft.Body), nil)
	if err != nil {
		log.Printf("slack/broadcast: create broadcast for draft %s: %v", draft.ID, err)
		tellOnlyYou(ctx, client, channelID, payload.User.ID, "The broadcast couldn't be created. Please try again.")
		return
	}
	StartBroadcast(pool, id)
	DeleteBroadcastDraft(ctx, pool, draft.ID)
	editSection(ctx, client, channelID, ts, ":white_check_mark: Broadcast is being sent."+broadcastHistoryLink(ctx, id))
}

// handleBroadcastScheduleSubmission schedules the drafted broadcast for the picked time.
func handleBroadcastScheduleSubmission(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	at, errs := parseBroadcastSchedule(payload.View.State.Values, time.Now())
	if errs != nil {
		modalErrors(w, errs)
		return
	}
	var meta broadcastScheduleMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil {
		modalErrors(w, map[string]string{broadcastScheduleBlock: "This draft can't be found. Start again from App Home."})
		return
	}
	draft, err := GetBroadcastDraft(ctx, pool, meta.Draft)
	switch {
	case err != nil, draft.AuthorSlackID != payload.User.ID, draft.State != DraftReady, time.Now().After(draft.ExpiresAt):
		modalErrors(w, map[string]string{broadcastScheduleBlock: "This draft expired. Start again from App Home."})
		return
	}
	if _, ok := broadcastAuthor(ctx, pool, draft.AuthorSlackID); !ok {
		modalErrors(w, map[string]string{broadcastScheduleBlock: "You don't have permission to send broadcasts."})
		return
	}
	if !BroadcastConfigured(ctx) {
		modalErrors(w, map[string]string{broadcastScheduleBlock: "Slack isn't connected, so the broadcast can't be scheduled."})
		return
	}
	id, err := CreateBroadcast(ctx, pool, draft.AuthorUserID, draft.AuthorName, slackBroadcastBody(draft.AuthorSlackID, draft.Body), &at)
	if err != nil {
		log.Printf("slack/broadcast: schedule broadcast for draft %s: %v", draft.ID, err)
		modalErrors(w, map[string]string{broadcastScheduleBlock: "The broadcast couldn't be scheduled. Please try again."})
		return
	}
	DeleteBroadcastDraft(ctx, pool, draft.ID)
	w.WriteHeader(http.StatusOK)
	editSection(ctx, client, meta.Channel, meta.TS, ":calendar: Broadcast scheduled for "+pollDateToken(at, pollLocation(ctx))+"."+broadcastHistoryLink(ctx, id))
}

// broadcastHistoryLink points at the broadcast's page in the admin UI, if the admin URL is configured.
func broadcastHistoryLink(ctx context.Context, broadcastID string) string {
	if pkgPool == nil {
		return ""
	}
	base, err := store.GetServiceConfig(ctx, pkgPool, "bot", "admin_url", pkgEncKey)
	if err != nil || strings.TrimSpace(base) == "" {
		return ""
	}
	return fmt.Sprintf(" <%s/admin/broadcasts/%s|View it>", strings.TrimRight(base, "/"), broadcastID)
}
