package slack

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/store"
)

func (e *invEnv) submitClaimLink(t *testing.T, slackUser, eventID, member string) {
	t.Helper()
	meta, err := json.Marshal(inventoryClaimLinkMeta{Event: eventID, Channel: "D_CH", TS: "111.222"})
	require.NoError(t, err)
	e.dispatch(t, map[string]any{
		"type": "view_submission",
		"user": map[string]any{"id": slackUser},
		"view": map[string]any{
			"id": "V2", "callback_id": ModalIDInventoryClaimLink, "private_metadata": string(meta),
			"state": map[string]any{"values": map[string]any{
				inventoryClaimLinkBlockID: map[string]any{
					inventoryClaimLinkUserAction: map[string]any{"type": "users_select", "selected_user": member},
				},
			}},
		},
	})
}

func TestNotifyUnmatchedInventoryClaim(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	e.user(t, "U_BOB", "Bob")
	e.admin(t, "U_ADAM", "Adam")
	e.admin(t, "U_ANNA", "Anna")
	it := e.item(t, "K7M4QX", "Projector")

	t.Run("admins are told who claimed it, which item, and who held it, with a link button", func(t *testing.T) {
		_, err := store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: it.ID, UserID: holly.ID, Source: "slack"})
		require.NoError(t, err)
		name, email := "Robert <@channel>", "robert@elsewhere.example"
		ev, err := store.RecordPendingClaim(e.ctx, e.pool, it.ID, name, email, nil, nil)
		require.NoError(t, err)

		NotifyUnmatchedInventoryClaim(e.ctx, e.pool, ev.ID)

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, 2, "both inventory admins, and nobody else")
		channels := []string{posts[0].Channel, posts[1].Channel}
		assert.ElementsMatch(t, []string{"DM_U_ADAM", "DM_U_ANNA"}, channels)
		for _, p := range posts {
			assert.Contains(t, p.Text, "Projector")
			assert.Contains(t, p.Text, "K7M-4QX")
			assert.Contains(t, p.Text, "robert@elsewhere.example")
			assert.Contains(t, p.Text, "*Previous holder:* <@U_HOLLY>")
			assert.Contains(t, p.Text, "doesn't match a member")
			assert.Contains(t, p.Text, "Robert &lt;@channel&gt;", "claimant input can't mention anyone")
			assert.NotContains(t, p.Text, "<@channel>")
			assert.Contains(t, p.Blocks, ActionInventoryClaimLink)
			assert.Contains(t, p.Blocks, ev.ID)
			assert.Contains(t, p.Blocks, "Link to member")
		}
	})

	t.Run("with no previous holder it says none", func(t *testing.T) {
		fresh := e.item(t, "FRS222", "Camera")
		ev, err := store.RecordPendingClaim(e.ctx, e.pool, fresh.ID, "Rita", "rita@elsewhere.example", nil, nil)
		require.NoError(t, err)
		before := len(e.fake.postsSnapshot())

		NotifyUnmatchedInventoryClaim(e.ctx, e.pool, ev.ID)

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, before+2)
		assert.Contains(t, posts[len(posts)-1].Text, "*Previous holder:* none")
	})

	t.Run("does nothing for an unknown claim", func(t *testing.T) {
		before := len(e.fake.postsSnapshot())
		NotifyUnmatchedInventoryClaim(e.ctx, e.pool, "00000000-0000-0000-0000-000000000000")
		assert.Len(t, e.fake.postsSnapshot(), before)
	})
}

func TestInventoryClaimLinkFlow(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	bob := e.user(t, "U_BOB", "Bob")
	e.admin(t, "U_ADAM", "Adam")

	newClaim := func(t *testing.T, code string) (*store.InventoryItem, *store.CustodyEvent) {
		t.Helper()
		it := e.item(t, code, "Item "+code)
		_, err := store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: it.ID, UserID: holly.ID, Source: "slack"})
		require.NoError(t, err)
		ev, err := store.RecordPendingClaim(e.ctx, e.pool, it.ID, "Robert", "robert@elsewhere.example", nil, nil)
		require.NoError(t, err)
		return it, ev
	}

	t.Run("the button opens a member picker for admins only", func(t *testing.T) {
		_, ev := newClaim(t, "OPN222")

		e.click(t, "U_HOLLY", ActionInventoryClaimLink, ev.ID)
		assert.Empty(t, e.fake.viewCalls("views.open"), "someone without inventory.admin gets nothing")

		e.click(t, "U_ADAM", ActionInventoryClaimLink, ev.ID)
		body := e.fake.lastView(t, "views.open")
		assert.Contains(t, body, "users_select")
		assert.Contains(t, body, inventoryClaimLinkUserAction)
		assert.Contains(t, body, ev.ID)
		assert.Contains(t, body, "111.222", "the alert to edit afterwards")
		assert.Contains(t, body, "Robert")
		assert.Contains(t, body, "robert@elsewhere.example")
	})

	t.Run("submitting links the claim, gives the member the item, and edits the alert", func(t *testing.T) {
		it, ev := newClaim(t, "LNK222")
		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_BOB")

		assert.Equal(t, bob.ID, e.holderOf(t, it.ID))
		got, err := store.GetCustodyEvent(e.ctx, e.pool, ev.ID)
		require.NoError(t, err)
		assert.Equal(t, "linked", got.Status)
		assert.Equal(t, bob.ID, *got.UserID)
		assert.Equal(t, holly.ID, *got.PreviousHolderUserID)

		upd := lastMessage(t, e.fake.updatesSnapshot())
		assert.Equal(t, "D_CH", upd.Channel)
		assert.Equal(t, "111.222", upd.TS)
		assert.Contains(t, upd.Text, "linked to <@U_BOB> by <@U_ADAM>")
		assert.Contains(t, upd.Text, "Item LNK222")
		assert.NotContains(t, upd.Blocks, ActionInventoryClaimLink, "the button is gone")
	})

	t.Run("an already resolved claim is reported and the alert's button removed", func(t *testing.T) {
		_, ev := newClaim(t, "RES222")
		_, err := store.LinkClaimToUser(e.ctx, e.pool, ev.ID, bob.ID)
		require.NoError(t, err)
		views := len(e.fake.viewCalls("views.open"))

		e.click(t, "U_ADAM", ActionInventoryClaimLink, ev.ID)
		assert.Len(t, e.fake.viewCalls("views.open"), views, "no picker for a resolved claim")
		assert.Contains(t, lastMessage(t, e.fake.updatesSnapshot()).Text, "already resolved")

		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_HOLLY")
		assert.Contains(t, lastMessage(t, e.fake.ephemeralsSnapshot()).Text, "already resolved")
		assert.Equal(t, bob.ID, e.holderOf(t, ev.ItemID), "the earlier link stands")
	})

	t.Run("someone without inventory.admin cannot submit", func(t *testing.T) {
		it, ev := newClaim(t, "NOP222")
		updates := len(e.fake.updatesSnapshot())

		e.submitClaimLink(t, "U_HOLLY", ev.ID, "U_BOB")

		assert.Equal(t, holly.ID, e.holderOf(t, it.ID))
		got, _ := store.GetCustodyEvent(e.ctx, e.pool, ev.ID)
		assert.Equal(t, "pending_unmatched", got.Status)
		assert.Len(t, e.fake.updatesSnapshot(), updates)
	})

	t.Run("a member the app has never seen changes nothing", func(t *testing.T) {
		it, ev := newClaim(t, "UNK222")
		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_NOBODY")

		assert.Contains(t, lastMessage(t, e.fake.ephemeralsSnapshot()).Text, "hasn't opened this app yet")
		assert.Equal(t, holly.ID, e.holderOf(t, it.ID))
		got, _ := store.GetCustodyEvent(e.ctx, e.pool, ev.ID)
		assert.Equal(t, "pending_unmatched", got.Status)
	})

	t.Run("an archived item can't be linked", func(t *testing.T) {
		it, ev := newClaim(t, "ARC222")
		require.NoError(t, store.ArchiveInventoryItem(e.ctx, e.pool, it.ID))

		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_BOB")
		assert.Contains(t, lastMessage(t, e.fake.ephemeralsSnapshot()).Text, "archived")
		got, _ := store.GetCustodyEvent(e.ctx, e.pool, ev.ID)
		assert.Equal(t, "pending_unmatched", got.Status)
	})
}
