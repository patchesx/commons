package slack

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
)

const (
	ModalIDPollClosesAt = "poll_closes_at_submit"

	pollClosesAtBlock  = "poll_closes_at"
	pollClosesAtAction = "poll_closes_at_input"
)

// pollClosesAtMeta rides along with the time picker so its submission can find the draft and edit the message
// the button was on.
type pollClosesAtMeta struct {
	Draft   string `json:"d"`
	Channel string `json:"c"`
	TS      string `json:"t"`
}

// PollClosesAtModal asks for an exact closing time, for an author who would rather pick one than type a duration.
func PollClosesAtModal(meta pollClosesAtMeta) slacklib.ModalViewRequest {
	plain := func(s string) *slacklib.TextBlockObject {
		return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, false, false)
	}
	raw, _ := json.Marshal(meta)
	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		CallbackID:      ModalIDPollClosesAt,
		PrivateMetadata: string(raw),
		Title:           plain("When should it close?"),
		Submit:          plain("Set time"),
		Close:           plain("Back"),
		Blocks: slacklib.Blocks{BlockSet: []slacklib.Block{
			slacklib.NewSectionBlock(pollMD("Pick when the poll closes, from 30 minutes to 30 days from now. The time is shown in your own timezone."), nil, nil),
			slacklib.NewInputBlock(pollClosesAtBlock, plain("Closes at"), nil, slacklib.NewDateTimePickerBlockElement(pollClosesAtAction)),
		}},
	}
}

// parsePollClosesAt reads the picked time and reports why it can't be used.
func parsePollClosesAt(values map[string]map[string]slacklib.BlockAction, now time.Time) (time.Time, map[string]string) {
	picked := values[pollClosesAtBlock][pollClosesAtAction].SelectedDateTime
	if picked == 0 {
		return time.Time{}, map[string]string{pollClosesAtBlock: "Pick a date and time."}
	}
	at := time.Unix(picked, 0)
	if msg := pollWindowError(at, now); msg != "" {
		return time.Time{}, map[string]string{pollClosesAtBlock: msg}
	}
	return at, nil
}

// pollClosesAtErrors validates a time picker submission, and returns nil for anything else or a valid time.
// The interactions webhook calls it before answering Slack, so the error shows on the modal.
func pollClosesAtErrors(body []byte) map[string]string {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil
	}
	var payload slacklib.InteractionCallback
	if err := json.Unmarshal([]byte(form.Get("payload")), &payload); err != nil {
		return nil
	}
	if payload.Type != slacklib.InteractionTypeViewSubmission || payload.View.CallbackID != ModalIDPollClosesAt {
		return nil
	}
	_, errs := parsePollClosesAt(payload.View.State.Values, time.Now())
	return errs
}

// handlePollClosesAtSubmission records the picked time on the draft and shows the preview.
func handlePollClosesAtSubmission(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	at, errs := parsePollClosesAt(payload.View.State.Values, time.Now())
	if errs != nil {
		modalErrors(w, errs)
		return
	}
	var meta pollClosesAtMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil {
		modalErrors(w, map[string]string{pollClosesAtBlock: "This poll draft can't be found. Start again from App Home."})
		return
	}
	draft, err := GetPollDraft(ctx, pool, meta.Draft)
	if err != nil || draft.AuthorSlackID != payload.User.ID || time.Now().After(draft.ExpiresAt) || draft.Step != PollStepDuration {
		modalErrors(w, map[string]string{pollClosesAtBlock: "This poll draft expired. Start again from App Home."})
		return
	}
	updated, err := UpdatePollDraft(ctx, pool, draft.ID, func(d *PollDraft) error {
		d.ClosesAt, d.DurationSecs, d.Step, d.ExpiresAt = &at, 0, PollStepConfirm, time.Now().Add(pollDraftWindow)
		return nil
	})
	if err != nil {
		log.Printf("slack/poll: save closing time: %v", err)
		modalErrors(w, map[string]string{pollClosesAtBlock: "The time couldn't be saved. Please try again."})
		return
	}
	w.WriteHeader(http.StatusOK)
	sendPollPreview(ctx, pool, client, updated)
}
