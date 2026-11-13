package slack

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// Inventory action IDs and modal callback IDs. Claiming and verifying need no permission: any Slack user
// the app can resolve to a member may use them.
const (
	ActionOpenInventory      = "open_inventory"
	ActionInventoryCodeInput = "inventory_code_input" // dispatch_action element; value is the typed code
	ActionInventoryClaim     = "inventory_claim"      // value: itemID
	ActionInventoryVerify    = "inventory_verify"     // value: itemID
	ActionInventoryHandOff   = "inventory_hand_off"   // value: itemID
	ActionInventoryHeldPrev  = "inventory_held_prev"  // value: page:N
	ActionInventoryHeldNext  = "inventory_held_next"  // value: page:N

	ModalIDInventory        = "inventory"
	ModalIDInventoryHandOff = "inventory_hand_off_submit"
)

// slackErrDetail adds the details Slack puts in response_metadata (which say what was wrong with a view) to
// an API error; the plain error code is often just "invalid_arguments".
func slackErrDetail(err error) string {
	var resp slacklib.SlackErrorResponse
	if errors.As(err, &resp) && len(resp.ResponseMetadata.Messages) > 0 {
		return fmt.Sprintf("%v (%s)", err, strings.Join(resp.ResponseMetadata.Messages, "; "))
	}
	return err.Error()
}

// resolveSlackUser maps a Slack user ID to a member, creating the member from the Slack profile if the app
// has not seen them yet.
func resolveSlackUser(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, slackID string) (*store.User, error) {
	u, err := store.GetUserByExternalID(ctx, pool, "slack", slackID)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	name, display := slackID, ""
	if info, ierr := client.GetUserInfoContext(ctx, slackID); ierr == nil {
		display = info.Profile.DisplayName
		name = info.Profile.RealName
		if name == "" {
			name = display
		}
		if name == "" {
			name = slackID
		}
	}
	return store.GetOrCreateUserByIdentity(ctx, pool, "slack", slackID, name, display)
}

// claimInventoryItem makes the Slack user the item's holder and returns the mrkdwn message to show them.
// Shared by the Inventory modal and the Checkout DM command.
func claimInventoryItem(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, slackID, itemID string) string {
	user, err := resolveSlackUser(ctx, pool, client, slackID)
	if err != nil {
		log.Printf("slack/inventory: resolve user %s: %v", slackID, err)
		return ":warning: I couldn't look you up. Try again in a moment."
	}
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil || item.ArchivedAt != nil {
		return ":warning: That item is no longer available."
	}
	name := escapeMrkdwn(item.Name)
	_, err = store.ApplyCustodyChange(ctx, pool, store.CustodyChange{ItemID: item.ID, UserID: user.ID, Source: "slack"})
	switch {
	case errors.Is(err, store.ErrInventoryAlreadyHolder):
		return fmt.Sprintf(":information_source: You already have *%s*.", name)
	case errors.Is(err, store.ErrInventoryItemArchived), errors.Is(err, store.ErrNotFound):
		return ":warning: That item is no longer available."
	case err != nil:
		log.Printf("slack/inventory: claim item %s for %s: %v", item.ID, user.ID, err)
		return ":warning: Something went wrong taking that item. Try again in a moment."
	}
	return fmt.Sprintf(":white_check_mark: You now have *%s* (`%s`).", name, store.FormatInventoryCode(item.Code))
}

// heldInventoryItem returns the item if the user currently holds it.
func heldInventoryItem(ctx context.Context, pool *pgxpool.Pool, userID, itemID string) (*store.InventoryItem, bool) {
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil || item.ArchivedAt != nil || item.CurrentHolderUserID == nil || *item.CurrentHolderUserID != userID {
		return nil, false
	}
	return item, true
}

// buildInventoryModal looks the typed code up (if any) and assembles the modal for this user.
func buildInventoryModal(ctx context.Context, pool *pgxpool.Pool, userID, code, notice string, page int) (slacklib.ModalViewRequest, error) {
	data := inventoryModalData{Code: strings.TrimSpace(code), Notice: notice, Page: page}
	if data.Code != "" {
		item, err := store.GetInventoryItemByCode(ctx, pool, data.Code)
		switch {
		case errors.Is(err, store.ErrNotFound):
			data.Notice = fmt.Sprintf(":warning: No active item has the code `%s`.", escapeMrkdwn(data.Code))
		case err != nil:
			return slacklib.ModalViewRequest{}, err
		default:
			data.Found = item
			if item.CurrentHolderUserID != nil {
				if *item.CurrentHolderUserID == userID {
					data.AlreadyHolder = true
				} else if holder, err := store.GetUserByID(ctx, pool, *item.CurrentHolderUserID); err == nil {
					data.FoundHolder = holder.DisplayName
				}
			}
		}
	}
	held, err := store.ListInventoryItemsHeldBy(ctx, pool, userID)
	if err != nil {
		return slacklib.ModalViewRequest{}, err
	}
	data.Held = held
	return InventoryModal(data), nil
}

// refreshInventoryModal re-renders the Inventory modal in place with a status line.
func refreshInventoryModal(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, userID, viewID, hash, code, notice string, page int) {
	modal, err := buildInventoryModal(ctx, pool, userID, code, notice, page)
	if err != nil {
		log.Printf("slack/inventory: build modal: %v", err)
		return
	}
	if _, err := client.UpdateViewContext(ctx, modal, "", hash, viewID); err != nil {
		log.Printf("slack/inventory: update modal: %s", slackErrDetail(err))
	}
}

func handleOpenInventory(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	defer w.WriteHeader(http.StatusOK)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: open: resolve user %s: %v", payload.User.ID, err)
		return
	}
	modal, err := buildInventoryModal(ctx, pool, user.ID, "", "", 1)
	if err != nil {
		log.Printf("slack/inventory: open: build modal: %v", err)
		return
	}
	if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
		log.Printf("slack/inventory: open modal: %s", slackErrDetail(err))
	}
}

// handleInventoryCodeInput runs when the user presses Enter in the code box.
func handleInventoryCodeInput(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, code string) {
	defer w.WriteHeader(http.StatusOK)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: lookup: resolve user %s: %v", payload.User.ID, err)
		return
	}
	refreshInventoryModal(ctx, pool, client, user.ID, payload.View.ID, payload.View.Hash, code, "", inventoryHeldPageOf(payload.View))
}

func handleInventoryClaim(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	notice := claimInventoryItem(ctx, pool, client, payload.User.ID, itemID)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		return
	}
	refreshInventoryModal(ctx, pool, client, user.ID, payload.View.ID, payload.View.Hash, "", notice, inventoryHeldPageOf(payload.View))
}

func handleInventoryVerify(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: verify: resolve user %s: %v", payload.User.ID, err)
		return
	}
	notice := ":warning: You no longer hold that item."
	if item, ok := heldInventoryItem(ctx, pool, user.ID, itemID); ok {
		if _, err := store.RecordVerification(ctx, pool, item.ID, nil); err != nil {
			log.Printf("slack/inventory: verify item %s: %v", item.ID, err)
			notice = ":warning: Something went wrong verifying that item. Try again in a moment."
		} else {
			notice = fmt.Sprintf(":white_check_mark: Thanks, verified *%s*.", escapeMrkdwn(item.Name))
		}
	}
	refreshInventoryModal(ctx, pool, client, user.ID, payload.View.ID, payload.View.Hash, "", notice, inventoryHeldPageOf(payload.View))
}

// handleInventoryHandOff pushes the hand-off view on top of the Inventory modal.
func handleInventoryHandOff(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, itemID string) {
	defer w.WriteHeader(http.StatusOK)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: hand off: resolve user %s: %v", payload.User.ID, err)
		return
	}
	item, ok := heldInventoryItem(ctx, pool, user.ID, itemID)
	if !ok {
		refreshInventoryModal(ctx, pool, client, user.ID, payload.View.ID, payload.View.Hash, "", ":warning: You no longer hold that item.", inventoryHeldPageOf(payload.View))
		return
	}
	if _, err := client.PushViewContext(ctx, payload.TriggerID, InventoryHandOffModal(*item)); err != nil {
		log.Printf("slack/inventory: push hand-off view: %s", slackErrDetail(err))
	}
}

// handleInventoryHandOffSubmit gives the item to the chosen person right away and messages them. The holder
// gets the result in the Inventory modal underneath; the previous holder is not messaged separately.
func handleInventoryHandOffSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	defer w.WriteHeader(http.StatusOK)
	itemID := payload.View.PrivateMetadata
	var recipientSlackID string
	if payload.View.State != nil {
		recipientSlackID = payload.View.State.Values[inventoryHandOffBlockID][inventoryHandOffUserAction].SelectedUser
	}

	holder, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: hand off submit: resolve user %s: %v", payload.User.ID, err)
		return
	}

	notice := handOffInventoryItem(ctx, pool, client, holder, payload.User.ID, itemID, recipientSlackID)
	if payload.View.RootViewID != "" {
		refreshInventoryModal(ctx, pool, client, holder.ID, payload.View.RootViewID, "", "", notice, 1)
	}
}

// handOffInventoryItem gives the item to the recipient if the holder still holds it, messages the recipient,
// and returns the mrkdwn result to show the holder.
func handOffInventoryItem(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, holder *store.User, holderSlackID, itemID, recipientSlackID string) string {
	item, err := store.GetInventoryItem(ctx, pool, itemID)
	if err != nil || item.ArchivedAt != nil {
		return ":warning: That item is no longer available."
	}
	name := escapeMrkdwn(item.Name)
	if recipientSlackID == "" {
		return ":warning: Pick who to hand it off to."
	}
	recipient, err := store.GetUserByExternalID(ctx, pool, "slack", recipientSlackID)
	if err != nil {
		return fmt.Sprintf(":warning: I couldn't hand off *%s*: <@%s> hasn't opened this app yet, so I don't know them.", name, recipientSlackID)
	}
	note := "Handed off by " + holder.DisplayName
	_, err = store.ApplyCustodyChange(ctx, pool, store.CustodyChange{
		ItemID: item.ID, UserID: recipient.ID, FromUserID: holder.ID, Source: "slack", Note: &note,
	})
	switch {
	case errors.Is(err, store.ErrInventoryNotHolder):
		return fmt.Sprintf(":warning: You no longer hold *%s*.", name)
	case errors.Is(err, store.ErrInventoryAlreadyHolder):
		return fmt.Sprintf(":information_source: You already have *%s*.", name)
	case errors.Is(err, store.ErrInventoryItemArchived), errors.Is(err, store.ErrNotFound):
		return ":warning: That item is no longer available."
	case err != nil:
		log.Printf("slack/inventory: hand off item %s: %v", item.ID, err)
		return ":warning: Something went wrong handing that item off. Try again in a moment."
	}
	notifyHandedOff(ctx, pool, client, item.ID, recipient.ID, holderSlackID)
	return fmt.Sprintf(":white_check_mark: Handed off *%s* to <@%s>.", name, recipientSlackID)
}

// handleInventoryHeldPage moves the "Items I hold" list to the page in the button's "page:N" value.
func handleInventoryHeldPage(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, value string) {
	defer w.WriteHeader(http.StatusOK)
	user, err := resolveSlackUser(ctx, pool, client, payload.User.ID)
	if err != nil {
		log.Printf("slack/inventory: held page: resolve user %s: %v", payload.User.ID, err)
		return
	}
	page, err := strconv.Atoi(strings.TrimPrefix(value, "page:"))
	if err != nil {
		page = 1
	}
	refreshInventoryModal(ctx, pool, client, user.ID, payload.View.ID, payload.View.Hash, "", "", page)
}
