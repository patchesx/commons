package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// A claim made on the public web form with an email that matches no member waits as a pending custody event.
// Inventory admins get a DM about it with a "Link to member" button; the web admin has the same queue.

const (
	ActionInventoryClaimLink  = "inventory_claim_link" // value: the pending custody event ID
	ModalIDInventoryClaimLink = "inventory_claim_link_submit"

	inventoryClaimLinkBlockID    = "inventory_claim_link_block"
	inventoryClaimLinkUserAction = "inventory_claim_link_user"
)

// inventoryClaimLinkMeta rides in the link modal's private_metadata so the submit can find the claim and edit
// the alert the admin clicked.
type inventoryClaimLinkMeta struct {
	Event   string `json:"event"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// claimantLabel is what the claimant typed, escaped for mrkdwn.
func claimantLabel(ev *store.CustodyEvent) string {
	return fmt.Sprintf("*%s* (%s)", escapeMrkdwn(derefString(ev.RawName)), escapeMrkdwn(derefString(ev.RawEmail)))
}

// NotifyUnmatchedInventoryClaim DMs every inventory admin about a web-form claim that matched no member, with
// the claimant's name and email, the item, and the previous holder, and a button to link it to a member.
func NotifyUnmatchedInventoryClaim(ctx context.Context, pool *pgxpool.Pool, eventID string) {
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/inventory: bot_token not configured; not telling admins about claim %s", eventID)
		return
	}
	ev, err := store.GetCustodyEvent(ctx, pool, eventID)
	if err != nil {
		log.Printf("slack/inventory: load claim %s: %v", eventID, err)
		return
	}
	item, err := store.GetInventoryItem(ctx, pool, ev.ItemID)
	if err != nil {
		log.Printf("slack/inventory: load item for claim %s: %v", eventID, err)
		return
	}
	previous := "none"
	if ev.PreviousHolderUserID != nil {
		previous = inventoryHolderRef(ctx, pool, *ev.PreviousHolderUserID)
	}
	headline := fmt.Sprintf(":inbox_tray: *%s* (`%s`) was claimed on the web form by someone whose email doesn't match a member.\n*Name:* %s\n*Email:* %s\n*Previous holder:* %s\n_%s_",
		escapeMrkdwn(item.Name), store.FormatInventoryCode(item.Code),
		escapeMrkdwn(derefString(ev.RawName)), escapeMrkdwn(derefString(ev.RawEmail)), previous, slackDate(ev.CreatedAt))
	link := slacklib.NewButtonBlockElement(ActionInventoryClaimLink, ev.ID, invPlain("Link to member"))
	link.Style = slacklib.StylePrimary
	blocks := []slacklib.Block{
		slacklib.NewSectionBlock(invMD(headline), nil, nil),
		slacklib.NewActionBlock("inventory_claim_"+ev.ID, link),
	}
	if sendToInventoryAdmins(ctx, pool, client, headline, blocks) == 0 {
		log.Printf("slack/inventory: no inventory admin with a Slack account to tell about claim %s", eventID)
	}
}

// InventoryClaimLinkModal asks an admin which member a pending claim belongs to.
func InventoryClaimLinkModal(item store.InventoryItem, ev *store.CustodyEvent, privateMetadata string) slacklib.ModalViewRequest {
	picker := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeUser, invPlain("Select a member"), inventoryClaimLinkUserAction)
	text := fmt.Sprintf("Link the claim by %s on %s to a member. They take possession right away.",
		claimantLabel(ev), inventoryItemSummary(item))
	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           invPlain("Link claim"),
		Submit:          invPlain("Link"),
		Close:           invPlain("Cancel"),
		CallbackID:      ModalIDInventoryClaimLink,
		PrivateMetadata: privateMetadata,
		Blocks: slacklib.Blocks{BlockSet: []slacklib.Block{
			slacklib.NewSectionBlock(invMD(text), nil, nil),
			slacklib.NewInputBlock(inventoryClaimLinkBlockID, invPlain("Member"), nil, picker),
		}},
	}
}

func handleInventoryClaimLinkOpen(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, eventID string) {
	defer w.WriteHeader(http.StatusOK)
	if _, ok := inventoryAdminUser(ctx, pool, client, payload.User.ID); !ok {
		return
	}
	channelID, ts := messageRef(payload)
	ev, err := store.GetCustodyEvent(ctx, pool, eventID)
	if err != nil {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: I couldn't find that claim.")
		return
	}
	if ev.Status != "pending_unmatched" {
		text := ":information_source: That claim was already resolved."
		editMessage(ctx, client, channelID, ts, text, inventoryResolvedBlocks(text))
		return
	}
	item, err := store.GetInventoryItem(ctx, pool, ev.ItemID)
	if err != nil || item.ArchivedAt != nil {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: That item is archived. Restore it first.")
		return
	}
	meta, err := json.Marshal(inventoryClaimLinkMeta{Event: ev.ID, Channel: channelID, TS: ts})
	if err != nil {
		return
	}
	if _, err := client.OpenViewContext(ctx, payload.TriggerID, InventoryClaimLinkModal(*item, ev, string(meta))); err != nil {
		log.Printf("slack/inventory: open claim link modal: %s", slackErrDetail(err))
	}
}

// handleInventoryClaimLinkSubmit links the claim to the chosen member and edits the alert. The admin permission
// is checked again here, not just on the button.
func handleInventoryClaimLinkSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	defer w.WriteHeader(http.StatusOK)
	if _, ok := inventoryAdminUser(ctx, pool, client, payload.User.ID); !ok {
		return
	}
	var meta inventoryClaimLinkMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil || meta.Event == "" {
		log.Printf("slack/inventory: claim link submit: bad metadata: %v", err)
		return
	}
	var memberSlackID string
	if payload.View.State != nil {
		memberSlackID = payload.View.State.Values[inventoryClaimLinkBlockID][inventoryClaimLinkUserAction].SelectedUser
	}
	say := func(text string) { tellOnlyYou(ctx, client, meta.Channel, payload.User.ID, text) }

	if memberSlackID == "" {
		say(":warning: Pick which member this claim belongs to.")
		return
	}
	ev, err := store.GetCustodyEvent(ctx, pool, meta.Event)
	if err != nil {
		say(":warning: I couldn't find that claim.")
		return
	}
	member, err := store.GetUserByExternalID(ctx, pool, "slack", memberSlackID)
	if err != nil {
		say(fmt.Sprintf(":warning: I couldn't link the claim: <@%s> hasn't opened this app yet, so I don't know them.", memberSlackID))
		return
	}
	linked, err := store.LinkClaimToUser(ctx, pool, ev.ID, member.ID)
	switch {
	case errors.Is(err, store.ErrInventoryClaimNotPending):
		text := ":information_source: That claim was already resolved."
		editMessage(ctx, client, meta.Channel, meta.TS, text, inventoryResolvedBlocks(text))
		say(text)
		return
	case errors.Is(err, store.ErrInventoryItemArchived):
		say(":warning: That item is archived. Restore it first.")
		return
	case errors.Is(err, store.ErrNotFound):
		say(":warning: I couldn't find that claim.")
		return
	case err != nil:
		log.Printf("slack/inventory: link claim %s: %v", ev.ID, err)
		say(":warning: Something went wrong. Try again in a moment.")
		return
	}

	itemName := "the item"
	if item, err := store.GetInventoryItem(ctx, pool, ev.ItemID); err == nil {
		itemName = fmt.Sprintf("*%s* (`%s`)", escapeMrkdwn(item.Name), store.FormatInventoryCode(item.Code))
	}
	text := fmt.Sprintf(":white_check_mark: The web-form claim of %s by %s was linked to <@%s> by <@%s>.",
		itemName, claimantLabel(ev), memberSlackID, payload.User.ID)
	editMessage(ctx, client, meta.Channel, meta.TS, text, inventoryResolvedBlocks(text))
	notifyClaimLinked(ctx, pool, client, linked.ItemID, member.ID)
}
