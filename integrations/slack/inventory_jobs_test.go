package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/permissions"
	"commons/store"
)

// admin creates a member with a Slack account who holds inventory.admin.
func (e *invEnv) admin(t *testing.T, slackID, name string) *store.User {
	t.Helper()
	u := e.user(t, slackID, name)
	role, err := store.CreateRole(e.ctx, e.pool, "role-"+slackID, "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(e.ctx, e.pool, role.ID, permissions.InventoryAdmin))
	group, err := store.CreateRoleGroup(e.ctx, e.pool, "group-"+slackID, "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(e.ctx, e.pool, group.ID, role.ID))
	require.NoError(t, store.AssignGroupToUser(e.ctx, e.pool, u.ID, group.ID))
	return u
}

// heldItem creates an item with a 7 day cadence held by the user and last verified daysAgo days ago.
func (e *invEnv) heldItem(t *testing.T, code, name string, holder *store.User, verifiedDaysAgo int) *store.InventoryItem {
	t.Helper()
	days := 7
	it, err := store.CreateInventoryItem(e.ctx, e.pool, store.InventoryItemInput{Name: name, VerifyEveryDays: &days}, code)
	require.NoError(t, err)
	_, err = store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: it.ID, UserID: holder.ID, Source: "slack"})
	require.NoError(t, err)
	_, err = e.pool.Exec(e.ctx, `UPDATE inventory.items SET last_verified_at = NOW() - make_interval(days => $2) WHERE id = $1`, it.ID, verifiedDaysAgo)
	require.NoError(t, err)
	return it
}

func (e *invEnv) remindedDaysAgo(t *testing.T, itemID string, days int) {
	t.Helper()
	_, err := e.pool.Exec(e.ctx, `UPDATE inventory.items SET last_reminded_at = NOW() - make_interval(days => $2) WHERE id = $1`, itemID, days)
	require.NoError(t, err)
}

func (e *invEnv) reload(t *testing.T, itemID string) *store.InventoryItem {
	t.Helper()
	it, err := store.GetInventoryItem(e.ctx, e.pool, itemID)
	require.NoError(t, err)
	return it
}

func (e *invEnv) events(t *testing.T, itemID string) []store.CustodyEvent {
	t.Helper()
	ev, err := store.ListInventoryCustodyEvents(e.ctx, e.pool, itemID)
	require.NoError(t, err)
	return ev
}

func lastMessage(t *testing.T, msgs []invMessage) invMessage {
	t.Helper()
	require.NotEmpty(t, msgs)
	return msgs[len(msgs)-1]
}

// click delivers a button click on a message in channel D_CH (timestamp 111.222) through the real dispatcher.
func (e *invEnv) click(t *testing.T, slackUser, actionID, itemID string) {
	t.Helper()
	payload := map[string]any{
		"type":       "block_actions",
		"user":       map[string]any{"id": slackUser},
		"trigger_id": "trig",
		"container":  map[string]any{"type": "message", "message_ts": "111.222", "channel_id": "D_CH"},
		"actions":    []any{map[string]any{"type": "button", "block_id": "b1", "action_id": actionID, "value": itemID}},
	}
	e.dispatch(t, payload)
}

func (e *invEnv) submitReassign(t *testing.T, slackUser, itemID, recipient string) {
	t.Helper()
	meta, err := json.Marshal(inventoryReassignMeta{Item: itemID, Channel: "D_CH", TS: "111.222"})
	require.NoError(t, err)
	e.dispatch(t, map[string]any{
		"type": "view_submission",
		"user": map[string]any{"id": slackUser},
		"view": map[string]any{
			"id": "V2", "callback_id": ModalIDInventoryReassign, "private_metadata": string(meta),
			"state": map[string]any{"values": map[string]any{
				inventoryReassignBlockID: map[string]any{
					inventoryReassignUserAction: map[string]any{"type": "users_select", "selected_user": recipient},
				},
			}},
		},
	})
}

func (e *invEnv) dispatch(t *testing.T, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	dispatchInteractionBody(e.ctx, w, e.pool, e.encKey, nil, []byte(url.Values{"payload": {string(raw)}}.Encode()))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestSendInventoryVerificationReminders(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	e.admin(t, "U_ADAM", "Adam")

	due := e.heldItem(t, "DUE222", "Projector", holly, 8)
	fresh := e.heldItem(t, "FRSH22", "Camera", holly, 2)
	archived := e.heldItem(t, "ARCH22", "Old laptop", holly, 9)
	require.NoError(t, store.ArchiveInventoryItem(e.ctx, e.pool, archived.ID))
	noCadence := e.heldItem(t, "NOCD22", "Loaner", holly, 9)
	_, err := e.pool.Exec(e.ctx, `UPDATE inventory.items SET verify_every_days = NULL WHERE id = $1`, noCadence.ID)
	require.NoError(t, err)
	e.item(t, "LOOS22", "Nobody has this")

	t.Run("reminds the holder of an item whose cycle has elapsed", func(t *testing.T) {
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, 1, "only the due item is reminded")
		assert.Equal(t, "DM_U_HOLLY", posts[0].Channel)
		assert.Contains(t, posts[0].Text, "Projector")
		assert.Contains(t, posts[0].Blocks, ActionInventoryReminderYes)
		assert.Contains(t, posts[0].Blocks, ActionInventoryReminderNo)
		assert.Contains(t, posts[0].Blocks, due.ID)
		assert.NotNil(t, e.reload(t, due.ID).LastRemindedAt)
		assert.Nil(t, e.reload(t, fresh.ID).LastRemindedAt)
		assert.Nil(t, e.reload(t, noCadence.ID).LastRemindedAt)
	})

	t.Run("an item is reminded once per cycle", func(t *testing.T) {
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		assert.Len(t, e.fake.postsSnapshot(), 1)
	})

	t.Run("inside the grace period nothing goes to admins", func(t *testing.T) {
		e.remindedDaysAgo(t, due.ID, 4)
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		assert.Len(t, e.fake.postsSnapshot(), 1)
		assert.Nil(t, e.reload(t, due.ID).LastEscalatedAt)
	})

	t.Run("past the grace period every inventory admin is told once", func(t *testing.T) {
		e.remindedDaysAgo(t, due.ID, 6)
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, 2)
		alert := posts[1]
		assert.Equal(t, "DM_U_ADAM", alert.Channel)
		assert.Contains(t, alert.Text, "<@U_HOLLY>")
		assert.Contains(t, alert.Text, "Projector")
		assert.Contains(t, alert.Text, "6 days")
		assert.Contains(t, alert.Text, "unconfirmed")
		assert.NotContains(t, alert.Text, "overdue", "there is no overdue state")
		for _, id := range []string{ActionInventoryAdminVerify, ActionInventoryAdminReassign, ActionInventoryAdminRevert} {
			assert.Contains(t, alert.Blocks, id)
		}
		assert.NotNil(t, e.reload(t, due.ID).LastEscalatedAt)

		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		assert.Len(t, e.fake.postsSnapshot(), 2, "no second alert in the same cycle")
	})

	t.Run("a holder with no Slack account is escalated straight away", func(t *testing.T) {
		var noSlack string
		require.NoError(t, e.pool.QueryRow(e.ctx, `INSERT INTO users (display_name) VALUES ('Nora NoSlack') RETURNING id`).Scan(&noSlack))
		it := e.heldItem(t, "NOSL22", "Tripod", &store.User{ID: noSlack}, 8)
		before := len(e.fake.postsSnapshot())

		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, before+1, "one alert to the admin and no DM to the holder")
		alert := posts[len(posts)-1]
		assert.Equal(t, "DM_U_ADAM", alert.Channel)
		assert.Contains(t, alert.Text, "Nora NoSlack")
		assert.Contains(t, alert.Text, "no Slack account linked")
		got := e.reload(t, it.ID)
		assert.NotNil(t, got.LastRemindedAt)
		assert.NotNil(t, got.LastEscalatedAt)
	})
}

func TestInventoryReminderJobSwitchAndMissingAdmins(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	it := e.heldItem(t, "DUE222", "Projector", holly, 8)

	t.Run("jobs.inventory_verify_enabled=false switches it off", func(t *testing.T) {
		require.NoError(t, store.SetServiceConfig(e.ctx, e.pool, "jobs", "inventory_verify_enabled", "false", false, nil, e.encKey))
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		assert.Empty(t, e.fake.postsSnapshot())
		assert.Nil(t, e.reload(t, it.ID).LastRemindedAt)

		require.NoError(t, store.SetServiceConfig(e.ctx, e.pool, "jobs", "inventory_verify_enabled", "true", false, nil, e.encKey))
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		assert.Len(t, e.fake.postsSnapshot(), 1)
	})

	t.Run("with no admin reachable the item is not marked escalated, so it is retried", func(t *testing.T) {
		e.remindedDaysAgo(t, it.ID, 6)
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		assert.Len(t, e.fake.postsSnapshot(), 1, "nobody to alert")
		assert.Nil(t, e.reload(t, it.ID).LastEscalatedAt)

		e.admin(t, "U_ADAM", "Adam")
		SendInventoryVerificationReminders(e.ctx, e.pool, e.encKey)
		posts := e.fake.postsSnapshot()
		require.Len(t, posts, 2)
		assert.Equal(t, "DM_U_ADAM", posts[1].Channel)
		assert.NotNil(t, e.reload(t, it.ID).LastEscalatedAt)
	})
}

func TestInventoryReminderButtons(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	e.user(t, "U_BOB", "Bob")
	e.admin(t, "U_ADAM", "Adam")

	t.Run("I still have it verifies and resolves the message", func(t *testing.T) {
		it := e.heldItem(t, "YES222", "Projector", holly, 8)
		require.NoError(t, store.MarkInventoryItemReminded(e.ctx, e.pool, it.ID))

		e.click(t, "U_HOLLY", ActionInventoryReminderYes, it.ID)

		evs := e.events(t, it.ID)
		assert.Equal(t, "verify", evs[0].Source)
		assert.Nil(t, e.reload(t, it.ID).LastRemindedAt, "a new cycle starts")
		upd := lastMessage(t, e.fake.updatesSnapshot())
		assert.Equal(t, "D_CH", upd.Channel)
		assert.Equal(t, "111.222", upd.TS)
		assert.Contains(t, upd.Text, "verified")
		assert.NotContains(t, upd.Blocks, ActionInventoryReminderYes, "the buttons are gone")
	})

	t.Run("a stale reminder from a previous holder does nothing", func(t *testing.T) {
		it := e.heldItem(t, "STL222", "Camera", holly, 8)
		e.click(t, "U_BOB", ActionInventoryReminderYes, it.ID)
		assert.Contains(t, lastMessage(t, e.fake.updatesSnapshot()).Text, "out of date")
		assert.Len(t, e.events(t, it.ID), 1, "no verify event")

		e.click(t, "U_BOB", ActionInventoryReminderNo, it.ID)
		assert.Len(t, e.events(t, it.ID), 1, "no dispute either")
	})

	t.Run("I don't have it tells the holder to contact the admins and alerts them", func(t *testing.T) {
		it := e.heldItem(t, "NOO222", "Laptop", holly, 8)
		require.NoError(t, store.MarkInventoryItemReminded(e.ctx, e.pool, it.ID))
		postsBefore := len(e.fake.postsSnapshot())

		e.click(t, "U_HOLLY", ActionInventoryReminderNo, it.ID)

		upd := lastMessage(t, e.fake.updatesSnapshot())
		assert.Contains(t, upd.Text, "contact the Inventory Admins")
		assert.NotContains(t, upd.Blocks, ActionInventoryReminderNo)

		evs := e.events(t, it.ID)
		assert.Equal(t, "disputed", evs[0].Status)
		got := e.reload(t, it.ID)
		assert.Equal(t, holly.ID, *got.CurrentHolderUserID, "the holder does not change")
		assert.NotNil(t, got.LastEscalatedAt)

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, postsBefore+1)
		alert := posts[len(posts)-1]
		assert.Equal(t, "DM_U_ADAM", alert.Channel)
		assert.Contains(t, alert.Text, "<@U_HOLLY>")
		assert.Contains(t, alert.Text, "no longer have")
		assert.Contains(t, alert.Text, "Laptop")
		assert.Contains(t, alert.Text, "NOO-222")
		assert.Contains(t, alert.Blocks, ActionInventoryAdminReassign)
	})
}

func TestInventoryAdminAlertButtons(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	alice := e.user(t, "U_ALICE", "Alice")
	bob := e.user(t, "U_BOB", "Bob")
	adam := e.admin(t, "U_ADAM", "Adam")

	t.Run("Mark verified", func(t *testing.T) {
		it := e.heldItem(t, "VER222", "Projector", holly, 8)
		e.click(t, "U_ADAM", ActionInventoryAdminVerify, it.ID)

		assert.Equal(t, "verify", e.events(t, it.ID)[0].Source)
		upd := lastMessage(t, e.fake.updatesSnapshot())
		assert.Contains(t, upd.Text, "marked verified by <@U_ADAM>")
		assert.NotContains(t, upd.Blocks, ActionInventoryAdminVerify, "the alert's buttons are replaced")
	})

	t.Run("buttons are ignored for anyone without inventory.admin", func(t *testing.T) {
		it := e.heldItem(t, "NOP222", "Camera", holly, 8)
		updates := len(e.fake.updatesSnapshot())
		views := len(e.fake.viewCalls("views.open"))

		e.click(t, "U_HOLLY", ActionInventoryAdminVerify, it.ID)
		e.click(t, "U_HOLLY", ActionInventoryAdminRevert, it.ID)
		e.click(t, "U_HOLLY", ActionInventoryAdminReassign, it.ID)
		e.submitReassign(t, "U_HOLLY", it.ID, "U_BOB")

		assert.Len(t, e.events(t, it.ID), 1, "nothing was recorded")
		assert.Equal(t, holly.ID, *e.reload(t, it.ID).CurrentHolderUserID)
		assert.Len(t, e.fake.updatesSnapshot(), updates)
		assert.Len(t, e.fake.viewCalls("views.open"), views, "no reassign modal either")
	})

	t.Run("Revert restores the previous holder", func(t *testing.T) {
		it := e.heldItem(t, "REV222", "Laptop", alice, 1)
		_, err := store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: it.ID, UserID: holly.ID, Source: "slack"})
		require.NoError(t, err)

		e.click(t, "U_ADAM", ActionInventoryAdminRevert, it.ID)

		assert.Equal(t, alice.ID, *e.reload(t, it.ID).CurrentHolderUserID)
		evs := e.events(t, it.ID)
		assert.Equal(t, "revert", evs[0].Source)
		assert.Equal(t, "Reverted by Adam", *evs[0].Note)
		upd := lastMessage(t, e.fake.updatesSnapshot())
		assert.Contains(t, upd.Text, "reverted by <@U_ADAM>")
		assert.Contains(t, upd.Text, "Alice")
	})

	t.Run("a failed action leaves the alert alone and only tells the admin", func(t *testing.T) {
		loose := e.item(t, "LOS222", "Unclaimed")
		updates := len(e.fake.updatesSnapshot())

		e.click(t, "U_ADAM", ActionInventoryAdminRevert, loose.ID)
		eph := lastMessage(t, e.fake.ephemeralsSnapshot())
		assert.Equal(t, "U_ADAM", eph.User)
		assert.Equal(t, "D_CH", eph.Channel)
		assert.Contains(t, eph.Text, "no hand-off to revert")

		e.click(t, "U_ADAM", ActionInventoryAdminVerify, loose.ID)
		assert.Contains(t, lastMessage(t, e.fake.ephemeralsSnapshot()).Text, "no holder")
		assert.Len(t, e.fake.updatesSnapshot(), updates, "the alert keeps its buttons")
	})

	t.Run("Reassign opens a person picker carrying the item and the alert", func(t *testing.T) {
		it := e.heldItem(t, "REA222", "Tripod", holly, 8)
		e.click(t, "U_ADAM", ActionInventoryAdminReassign, it.ID)

		body := e.fake.lastView(t, "views.open")
		assert.Contains(t, body, "users_select")
		assert.Contains(t, body, inventoryReassignUserAction)
		assert.Contains(t, body, it.ID)
		assert.Contains(t, body, "111.222", "the alert message to edit afterwards")
	})

	t.Run("Reassign submit gives the item away right away and edits the alert", func(t *testing.T) {
		it := e.heldItem(t, "SUB222", "Speaker", holly, 8)
		e.submitReassign(t, "U_ADAM", it.ID, "U_BOB")

		assert.Equal(t, bob.ID, *e.reload(t, it.ID).CurrentHolderUserID)
		evs := e.events(t, it.ID)
		assert.Equal(t, "admin", evs[0].Source)
		assert.Equal(t, holly.ID, *evs[0].PreviousHolderUserID)
		assert.Equal(t, "Reassigned by "+adam.DisplayName, *evs[0].Note)
		upd := lastMessage(t, e.fake.updatesSnapshot())
		assert.Equal(t, "111.222", upd.TS)
		assert.Contains(t, upd.Text, "reassigned to <@U_BOB> by <@U_ADAM>")
	})

	t.Run("Reassign to someone unknown, or who already has it, changes nothing", func(t *testing.T) {
		it := e.heldItem(t, "UNK222", "Mic", holly, 8)
		updates := len(e.fake.updatesSnapshot())

		e.submitReassign(t, "U_ADAM", it.ID, "U_NOBODY")
		assert.Contains(t, lastMessage(t, e.fake.ephemeralsSnapshot()).Text, "hasn't opened this app yet")
		e.submitReassign(t, "U_ADAM", it.ID, "U_HOLLY")
		assert.Contains(t, lastMessage(t, e.fake.ephemeralsSnapshot()).Text, "already holds")

		assert.Equal(t, holly.ID, *e.reload(t, it.ID).CurrentHolderUserID)
		assert.Len(t, e.events(t, it.ID), 1)
		assert.Len(t, e.fake.updatesSnapshot(), updates)
	})
}

func TestInventoryReminderBlocksAndWording(t *testing.T) {
	loc := "Closet"
	it := store.InventoryItem{ID: "item-1", Name: "A <b> & B", Code: "K7M4QX", Location: &loc}

	body := modalJSON(t, InventoryReassignModal(it, `{"item":"item-1"}`))
	assert.Contains(t, body, "users_select")
	assert.Contains(t, body, "A &lt;b&gt; &amp; B", "names are escaped")

	var buf []byte
	for _, b := range inventoryReminderBlocks(it) {
		raw, err := json.Marshal(b)
		require.NoError(t, err)
		buf = append(buf, raw...)
	}
	reminder := string(buf)
	assert.Contains(t, reminder, "K7M-4QX")
	assert.Contains(t, reminder, "I still have it")
	assert.Contains(t, reminder, "I don")
	assert.Contains(t, reminder, `"value":"item-1"`)
}
