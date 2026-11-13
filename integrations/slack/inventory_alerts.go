package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/permissions"
	"commons/store"
)

// Action IDs for the verification reminder DM (sent to the holder) and the alert DM (sent to inventory
// admins). Every value is the item ID.
const (
	ActionInventoryReminderYes   = "inventory_reminder_yes"
	ActionInventoryReminderNo    = "inventory_reminder_no"
	ActionInventoryAdminVerify   = "inventory_admin_verify"
	ActionInventoryAdminReassign = "inventory_admin_reassign"
	ActionInventoryAdminRevert   = "inventory_admin_revert"

	ModalIDInventoryReassign = "inventory_reassign_submit"

	inventoryReassignBlockID    = "inventory_reassign_block"
	inventoryReassignUserAction = "inventory_reassign_user"
)

// inventoryReassignMeta rides in the reassign modal's private_metadata so the submit can find the item and
// edit the alert message the admin clicked.
type inventoryReassignMeta struct {
	Item    string `json:"item"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// slackDate renders a time in each viewer's own time zone.
func slackDate(t time.Time) string {
	return fmt.Sprintf("<!date^%d^{date_short_pretty} at {time}|%s>", t.Unix(), t.UTC().Format("Jan 2, 3:04 PM UTC"))
}

func inventoryReminderBlocks(it store.InventoryItem) []slacklib.Block {
	text := fmt.Sprintf(":package: Do you still have *%s* (`%s`)?", escapeMrkdwn(it.Name), store.FormatInventoryCode(it.Code))
	if it.Location != nil && *it.Location != "" {
		text += "\n_" + escapeMrkdwn(*it.Location) + "_"
	}
	yes := slacklib.NewButtonBlockElement(ActionInventoryReminderYes, it.ID, invPlain("I still have it"))
	yes.Style = slacklib.StylePrimary
	no := slacklib.NewButtonBlockElement(ActionInventoryReminderNo, it.ID, invPlain("I don't have it"))
	return []slacklib.Block{
		slacklib.NewSectionBlock(invMD(text), nil, nil),
		slacklib.NewActionBlock("inventory_reminder_"+it.ID, yes, no),
	}
}

func inventoryAdminAlertBlocks(headline, itemID string) []slacklib.Block {
	verify := slacklib.NewButtonBlockElement(ActionInventoryAdminVerify, itemID, invPlain("Mark verified"))
	reassign := slacklib.NewButtonBlockElement(ActionInventoryAdminReassign, itemID, invPlain("Reassign"))
	revert := slacklib.NewButtonBlockElement(ActionInventoryAdminRevert, itemID, invPlain("Revert"))
	return []slacklib.Block{
		slacklib.NewSectionBlock(invMD(headline), nil, nil),
		slacklib.NewActionBlock("inventory_alert_"+itemID, verify, reassign, revert),
	}
}

func inventoryResolvedBlocks(text string) []slacklib.Block {
	return []slacklib.Block{slacklib.NewSectionBlock(invMD(text), nil, nil)}
}

// InventoryReassignModal asks an admin who should hold the item.
func InventoryReassignModal(item store.InventoryItem, privateMetadata string) slacklib.ModalViewRequest {
	picker := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeUser, invPlain("Select a person"), inventoryReassignUserAction)
	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           invPlain("Reassign"),
		Submit:          invPlain("Reassign"),
		Close:           invPlain("Cancel"),
		CallbackID:      ModalIDInventoryReassign,
		PrivateMetadata: privateMetadata,
		Blocks: slacklib.Blocks{BlockSet: []slacklib.Block{
			slacklib.NewSectionBlock(invMD("Give "+inventoryItemSummary(item)+" to someone. They take possession right away."), nil, nil),
			slacklib.NewInputBlock(inventoryReassignBlockID, invPlain("Reassign to"), nil, picker),
		}},
	}
}

// messageRef is where the clicked message lives, so the handler can edit it.
func messageRef(payload *slacklib.InteractionCallback) (channelID, ts string) {
	return payload.Container.ChannelID, payload.Container.MessageTs
}

// editMessage replaces a message's text and blocks. It does nothing without a message reference.
func editMessage(ctx context.Context, client *slacklib.Client, channelID, ts, text string, blocks []slacklib.Block) {
	if channelID == "" || ts == "" {
		return
	}
	if _, _, _, err := client.UpdateMessageContext(ctx, channelID, ts,
		slacklib.MsgOptionText(text, false), slacklib.MsgOptionBlocks(blocks...)); err != nil {
		log.Printf("slack/inventory: edit message: %s", slackErrDetail(err))
	}
}

// tellOnlyYou posts a message only the clicking user can see, leaving the clicked message as it is.
func tellOnlyYou(ctx context.Context, client *slacklib.Client, channelID, slackUserID, text string) {
	if channelID == "" {
		return
	}
	if _, err := client.PostEphemeralContext(ctx, channelID, slackUserID, slacklib.MsgOptionText(text, false)); err != nil {
		log.Printf("slack/inventory: ephemeral message: %s", slackErrDetail(err))
	}
}

// alertInventoryAdmins DMs every inventory admin who has a Slack account, with Mark verified, Reassign, and
// Revert buttons. It returns how many admins it tried to reach.
func alertInventoryAdmins(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, itemID, headline string) int {
	return sendToInventoryAdmins(ctx, pool, client, headline, inventoryAdminAlertBlocks(headline, itemID))
}

// sendToInventoryAdmins DMs the given blocks to every inventory admin who has a Slack account and returns
// how many it tried to reach.
func sendToInventoryAdmins(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, headline string, blocks []slacklib.Block) int {
	admins, err := store.ListUsersWithPermission(ctx, pool, permissions.InventoryAdmin)
	if err != nil {
		log.Printf("slack/inventory: list inventory admins: %v", err)
		return 0
	}
	reached := 0
	for _, a := range admins {
		slackID, err := store.GetUserExternalID(ctx, pool, a.ID, "slack")
		if err != nil || slackID == "" {
			continue
		}
		reached++
		if _, _, err := SendDM(ctx, client, slackID, headline, blocks...); err != nil {
			log.Printf("slack/inventory: alert admin %s: %v", a.ID, err)
		}
	}
	return reached
}

// inventoryAdminUser resolves the clicking user and re-checks inventory.admin, so a forwarded or stale
// alert can't be used by someone without the permission.
func inventoryAdminUser(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, slackID string) (*store.User, bool) {
	user, err := resolveSlackUser(ctx, pool, client, slackID)
	if err != nil {
		log.Printf("slack/inventory: resolve admin %s: %v", slackID, err)
		return nil, false
	}
	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil || !hasPermission(perms, permissions.InventoryAdmin) {
		log.Printf("slack/inventory: %s lacks inventory.admin", user.ID)
		return nil, false
	}
	return user, true
}

// ---- Holder's reminder buttons ----

func handleInventoryReminderYes(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	channelID, ts := messageRef(payload)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: reminder yes: resolve user %s: %v", payload.User.ID, err)
		return
	}
	item, ok := heldInventoryItem(ctx, pool, user.ID, itemID)
	if !ok {
		editMessage(ctx, client, channelID, ts, "This reminder is out of date.",
			inventoryResolvedBlocks(":information_source: This reminder is out of date. You no longer hold that item."))
		return
	}
	if _, err := store.RecordVerification(ctx, pool, item.ID, nil); err != nil {
		log.Printf("slack/inventory: reminder yes: verify item %s: %v", item.ID, err)
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: Something went wrong. Try again in a moment.")
		return
	}
	text := fmt.Sprintf(":white_check_mark: Thanks, verified *%s*.", escapeMrkdwn(item.Name))
	editMessage(ctx, client, channelID, ts, text, inventoryResolvedBlocks(text))
}

func handleInventoryReminderNo(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	channelID, ts := messageRef(payload)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: reminder no: resolve user %s: %v", payload.User.ID, err)
		return
	}
	item, ok := heldInventoryItem(ctx, pool, user.ID, itemID)
	if !ok {
		editMessage(ctx, client, channelID, ts, "This reminder is out of date.",
			inventoryResolvedBlocks(":information_source: This reminder is out of date. You no longer hold that item."))
		return
	}
	note := "Holder reported not having the item (verification reminder)"
	if _, err := store.RecordInventoryDispute(ctx, pool, item.ID, &note); err != nil {
		log.Printf("slack/inventory: reminder no: record dispute for item %s: %v", item.ID, err)
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: Something went wrong. Try again in a moment.")
		return
	}

	name, code := escapeMrkdwn(item.Name), store.FormatInventoryCode(item.Code)
	text := fmt.Sprintf(":warning: Thanks for letting us know. Please contact the Inventory Admins right away about *%s* (`%s`).", name, code)
	editMessage(ctx, client, channelID, ts, text, inventoryResolvedBlocks(text))

	headline := fmt.Sprintf(":rotating_light: <@%s> reports they no longer have *%s* (`%s`).\n_Reported %s._",
		payload.User.ID, name, code, slackDate(time.Now()))
	if alertInventoryAdmins(ctx, pool, client, item.ID, headline) == 0 {
		log.Printf("slack/inventory: no inventory admin with a Slack account to tell about item %s", item.ID)
	}
}

// ---- Admin alert buttons ----

func handleInventoryAdminVerify(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	if _, ok := inventoryAdminUser(ctx, pool, client, payload.User.ID); !ok {
		return
	}
	channelID, ts := messageRef(payload)
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: I couldn't find that item.")
		return
	}
	name := escapeMrkdwn(item.Name)
	_, err = store.RecordVerification(ctx, pool, item.ID, nil)
	switch {
	case errors.Is(err, store.ErrInventoryNoHolder):
		tellOnlyYou(ctx, client, channelID, payload.User.ID, fmt.Sprintf(":information_source: *%s* has no holder, so there's nothing to verify.", name))
		return
	case errors.Is(err, store.ErrInventoryItemArchived):
		tellOnlyYou(ctx, client, channelID, payload.User.ID, fmt.Sprintf(":information_source: *%s* is archived.", name))
		return
	case err != nil:
		log.Printf("slack/inventory: admin verify item %s: %v", item.ID, err)
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: Something went wrong. Try again in a moment.")
		return
	}
	text := fmt.Sprintf(":white_check_mark: *%s* (`%s`) marked verified by <@%s>.", name, store.FormatInventoryCode(item.Code), payload.User.ID)
	editMessage(ctx, client, channelID, ts, text, inventoryResolvedBlocks(text))
}

func handleInventoryAdminRevert(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	admin, ok := inventoryAdminUser(ctx, pool, client, payload.User.ID)
	if !ok {
		return
	}
	channelID, ts := messageRef(payload)
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: I couldn't find that item.")
		return
	}
	name := escapeMrkdwn(item.Name)
	note := "Reverted by " + admin.DisplayName
	ev, err := store.RevertCustody(ctx, pool, item.ID, &note)
	switch {
	case errors.Is(err, store.ErrInventoryNothingToRevert):
		tellOnlyYou(ctx, client, channelID, payload.User.ID, fmt.Sprintf(":information_source: There's no hand-off to revert for *%s*.", name))
		return
	case errors.Is(err, store.ErrInventoryItemArchived):
		tellOnlyYou(ctx, client, channelID, payload.User.ID, fmt.Sprintf(":information_source: *%s* is archived.", name))
		return
	case err != nil:
		log.Printf("slack/inventory: admin revert item %s: %v", item.ID, err)
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: Something went wrong. Try again in a moment.")
		return
	}
	holder := "nobody"
	if ev.UserID != nil {
		if u, err := store.GetUserByID(ctx, pool, *ev.UserID); err == nil {
			holder = escapeMrkdwn(u.DisplayName)
		}
	}
	text := fmt.Sprintf(":leftwards_arrow_with_hook: *%s* (`%s`) reverted by <@%s>. It is now held by %s.",
		name, store.FormatInventoryCode(item.Code), payload.User.ID, holder)
	editMessage(ctx, client, channelID, ts, text, inventoryResolvedBlocks(text))
}

func handleInventoryAdminReassign(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	if _, ok := inventoryAdminUser(ctx, pool, client, payload.User.ID); !ok {
		return
	}
	channelID, ts := messageRef(payload)
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil || item.ArchivedAt != nil {
		tellOnlyYou(ctx, client, channelID, payload.User.ID, ":warning: That item is no longer available.")
		return
	}
	meta, err := json.Marshal(inventoryReassignMeta{Item: item.ID, Channel: channelID, TS: ts})
	if err != nil {
		return
	}
	if _, err := client.OpenViewContext(ctx, payload.TriggerID, InventoryReassignModal(*item, string(meta))); err != nil {
		log.Printf("slack/inventory: open reassign modal: %s", slackErrDetail(err))
	}
}

// handleInventoryReassignSubmit gives the item to the chosen person right away and edits the alert. The admin
// permission is checked again here, not just on the button.
func handleInventoryReassignSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	defer w.WriteHeader(http.StatusOK)
	admin, ok := inventoryAdminUser(ctx, pool, client, payload.User.ID)
	if !ok {
		return
	}
	var meta inventoryReassignMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil || meta.Item == "" {
		log.Printf("slack/inventory: reassign submit: bad metadata: %v", err)
		return
	}
	var recipientSlackID string
	if payload.View.State != nil {
		recipientSlackID = payload.View.State.Values[inventoryReassignBlockID][inventoryReassignUserAction].SelectedUser
	}
	say := func(text string) { tellOnlyYou(ctx, client, meta.Channel, payload.User.ID, text) }

	if recipientSlackID == "" {
		say(":warning: Pick who to reassign it to.")
		return
	}
	item, err := store.GetInventoryItem(ctx, pool, meta.Item)
	if err != nil || item.ArchivedAt != nil {
		say(":warning: That item is no longer available.")
		return
	}
	name := escapeMrkdwn(item.Name)
	recipient, err := store.GetUserByExternalID(ctx, pool, "slack", recipientSlackID)
	if err != nil {
		say(fmt.Sprintf(":warning: I couldn't reassign *%s*: <@%s> hasn't opened this app yet, so I don't know them.", name, recipientSlackID))
		return
	}
	note := "Reassigned by " + admin.DisplayName
	_, err = store.ApplyCustodyChange(ctx, pool, store.CustodyChange{
		ItemID: item.ID, UserID: recipient.ID, Source: "admin", Note: &note,
	})
	switch {
	case errors.Is(err, store.ErrInventoryAlreadyHolder):
		say(fmt.Sprintf(":information_source: <@%s> already holds *%s*.", recipientSlackID, name))
		return
	case errors.Is(err, store.ErrInventoryItemArchived), errors.Is(err, store.ErrNotFound):
		say(":warning: That item is no longer available.")
		return
	case err != nil:
		log.Printf("slack/inventory: reassign item %s: %v", item.ID, err)
		say(":warning: Something went wrong. Try again in a moment.")
		return
	}
	text := fmt.Sprintf(":white_check_mark: *%s* (`%s`) reassigned to <@%s> by <@%s>.",
		name, store.FormatInventoryCode(item.Code), recipientSlackID, payload.User.ID)
	editMessage(ctx, client, meta.Channel, meta.TS, text, inventoryResolvedBlocks(text))
	if recipientSlackID != payload.User.ID {
		notifyReassigned(ctx, pool, client, item.ID, recipient.ID, payload.User.ID)
	}
}
