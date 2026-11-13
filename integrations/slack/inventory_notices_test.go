package slack

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/store"
)

func dmsTo(msgs []invMessage, channel string) []invMessage {
	var out []invMessage
	for _, m := range msgs {
		if m.Channel == channel {
			out = append(out, m)
		}
	}
	return out
}

func TestNotifyWebClaimApplied(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	bob := e.user(t, "U_BOB", "Bob")
	it := e.item(t, "K7M4QX", "Projector")

	t.Run("tells the member they now have it, and what to do if it wasn't them", func(t *testing.T) {
		NotifyWebClaimApplied(e.ctx, e.pool, it.ID, bob.ID)

		dms := dmsTo(e.fake.postsSnapshot(), "DM_U_BOB")
		require.Len(t, dms, 1)
		assert.Contains(t, dms[0].Text, "You now have *Projector* (`K7M-4QX`)")
		assert.Contains(t, dms[0].Text, "web form")
		assert.Contains(t, dms[0].Text, "If this wasn't you, please contact the Inventory Admins")
	})

	t.Run("a member with no Slack account is skipped quietly", func(t *testing.T) {
		before := len(e.fake.postsSnapshot())
		var noSlack string
		require.NoError(t, e.pool.QueryRow(e.ctx, `INSERT INTO users (display_name) VALUES ('Nora') RETURNING id`).Scan(&noSlack))
		NotifyWebClaimApplied(e.ctx, e.pool, it.ID, noSlack)
		assert.Len(t, e.fake.postsSnapshot(), before)
	})

	t.Run("an unknown item sends nothing", func(t *testing.T) {
		before := len(e.fake.postsSnapshot())
		NotifyWebClaimApplied(e.ctx, e.pool, "00000000-0000-0000-0000-000000000000", bob.ID)
		assert.Len(t, e.fake.postsSnapshot(), before)
	})

	t.Run("item names are escaped", func(t *testing.T) {
		odd := e.item(t, "ESC222", "A <@channel> & B")
		NotifyWebClaimApplied(e.ctx, e.pool, odd.ID, bob.ID)
		dms := dmsTo(e.fake.postsSnapshot(), "DM_U_BOB")
		last := dms[len(dms)-1]
		assert.Contains(t, last.Text, "A &lt;@channel&gt; &amp; B")
		assert.NotContains(t, last.Text, "<@channel>")
	})
}

func TestNotifyClaimLinked(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	bob := e.user(t, "U_BOB", "Bob")
	it := e.item(t, "K7M4QX", "Projector")

	NotifyClaimLinked(e.ctx, e.pool, it.ID, bob.ID)

	dms := dmsTo(e.fake.postsSnapshot(), "DM_U_BOB")
	require.Len(t, dms, 1)
	assert.Contains(t, dms[0].Text, "An inventory admin matched a web-form check-in to you")
	assert.Contains(t, dms[0].Text, "you now have *Projector* (`K7M-4QX`)")
	assert.Contains(t, dms[0].Text, "contact the Inventory Admins")
}

func TestMemberHandOffMessagesTheRecipient(t *testing.T) {
	e := newInvEnv(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	e.user(t, "U_BOB", "Bob")
	odd := e.heldItem(t, "ODD222", "A <@channel> & B", holly, 1)

	// Handing an item to yourself changes nothing and sends nothing.
	handleInventoryHandOffSubmit(e.ctx, httptest.NewRecorder(), e.pool, e.client, decodePayload(t, handOffSubmitJSON("U_HOLLY", odd.ID, "U_HOLLY")))
	assert.Empty(t, e.fake.postsSnapshot())
	assert.Equal(t, holly.ID, e.holderOf(t, odd.ID))

	// The recipient is told, with the item name escaped and the holder mentioned, and nobody else is.
	handleInventoryHandOffSubmit(e.ctx, httptest.NewRecorder(), e.pool, e.client, decodePayload(t, handOffSubmitJSON("U_HOLLY", odd.ID, "U_BOB")))
	posts := e.fake.postsSnapshot()
	require.Len(t, posts, 1)
	assert.Equal(t, "DM_U_BOB", posts[0].Channel)
	assert.Contains(t, posts[0].Text, "<@U_HOLLY> handed")
	assert.Contains(t, posts[0].Text, "A &lt;@channel&gt; &amp; B")
	assert.NotContains(t, posts[0].Text, "<@channel>")
}

func TestRecipientIsToldWhenAnAdminGivesThemAnItem(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	holly := e.user(t, "U_HOLLY", "Holly")
	bob := e.user(t, "U_BOB", "Bob")
	e.admin(t, "U_ADAM", "Adam")

	t.Run("a Slack reassign messages the new holder, and only them", func(t *testing.T) {
		it := e.heldItem(t, "REA222", "Projector", holly, 8)
		e.submitReassign(t, "U_ADAM", it.ID, "U_BOB")
		require.Equal(t, bob.ID, e.holderOf(t, it.ID))

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, 1)
		assert.Equal(t, "DM_U_BOB", posts[0].Channel)
		assert.Contains(t, posts[0].Text, "<@U_ADAM> assigned *Projector* (`REA-222`) to you")
		assert.Empty(t, dmsTo(posts, "DM_U_HOLLY"), "the previous holder is not messaged")
	})

	t.Run("reassigning to yourself sends nothing", func(t *testing.T) {
		it := e.heldItem(t, "SLF222", "Camera", holly, 8)
		before := len(e.fake.postsSnapshot())
		e.submitReassign(t, "U_ADAM", it.ID, "U_ADAM")
		assert.Len(t, e.fake.postsSnapshot(), before)
	})

	t.Run("a reassign that fails sends nothing", func(t *testing.T) {
		it := e.heldItem(t, "FAL222", "Laptop", holly, 8)
		before := len(e.fake.postsSnapshot())
		e.submitReassign(t, "U_ADAM", it.ID, "U_NOBODY") // never seen by the app
		e.submitReassign(t, "U_ADAM", it.ID, "U_HOLLY")  // already holds it
		e.submitReassign(t, "U_HOLLY", it.ID, "U_BOB")   // not an admin
		assert.Len(t, e.fake.postsSnapshot(), before)
		assert.Equal(t, holly.ID, e.holderOf(t, it.ID))
	})

	t.Run("linking a claim in Slack messages the member it was linked to", func(t *testing.T) {
		it := e.item(t, "LNK222", "Speaker")
		ev, err := store.RecordPendingClaim(e.ctx, e.pool, it.ID, "Robert", "robert@elsewhere.example", nil, nil)
		require.NoError(t, err)
		before := len(e.fake.postsSnapshot())

		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_BOB")

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, before+1)
		last := posts[len(posts)-1]
		assert.Equal(t, "DM_U_BOB", last.Channel)
		assert.Contains(t, last.Text, "matched a web-form check-in to you")
		assert.Contains(t, last.Text, "*Speaker* (`LNK-222`)")
	})

	t.Run("a link that fails sends nothing", func(t *testing.T) {
		it := e.item(t, "LFL222", "Mic")
		ev, err := store.RecordPendingClaim(e.ctx, e.pool, it.ID, "Rita", "rita@elsewhere.example", nil, nil)
		require.NoError(t, err)
		before := len(e.fake.postsSnapshot())

		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_NOBODY") // never seen by the app
		e.submitClaimLink(t, "U_HOLLY", ev.ID, "U_BOB")   // not an admin
		assert.Len(t, e.fake.postsSnapshot(), before)

		_, err = store.LinkClaimToUser(e.ctx, e.pool, ev.ID, holly.ID)
		require.NoError(t, err)
		e.submitClaimLink(t, "U_ADAM", ev.ID, "U_BOB") // already resolved
		assert.Len(t, e.fake.postsSnapshot(), before, "no message for a claim that was already resolved")
	})
}
