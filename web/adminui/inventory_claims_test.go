package adminui

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

func TestInventoryClaimsQueue(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	linked := make(chan [2]string, 8)
	d := Deps{
		Pool:   pool,
		EncKey: testhelpers.EncKey(),
		NotifyClaimLinked: func(_ context.Context, itemID, userID string) {
			linked <- [2]string{itemID, userID}
		},
	}
	ctx := t.Context()

	admin := invGrant(t, pool, "admin", permissions.InventoryAdmin)
	manager := invGrant(t, pool, "manager", permissions.InventoryManage)
	viewer := invGrant(t, pool, "viewer", permissions.InventoryView)

	alice := invUser(t, pool, "Alice Anderson")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, alice)
	invUser(t, pool, "Bob Brown")
	invExec(t, pool, `INSERT INTO users (display_name, bot) VALUES ('Helpful Bot', TRUE)`)

	item, err := store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "Projector"}, "K7M4QX")
	require.NoError(t, err)
	_, err = store.ApplyCustodyChange(ctx, pool, store.CustodyChange{ItemID: item.ID, UserID: alice, Source: "slack"})
	require.NoError(t, err)
	claim, err := store.RecordPendingClaim(ctx, pool, item.ID, "Robert <R>", "robert@elsewhere.example", nil, nil)
	require.NoError(t, err)

	page := func(user string) (int, string) {
		w := invDo(t, d.InventoryClaimsPage(), invReq{userID: user, method: "GET", target: "/admin/inventory/claims"})
		return w.Code, w.Body.String()
	}
	link := func(user, claimID string, form url.Values) (int, string) {
		w := invDo(t, d.InventoryClaimLink(), invReq{userID: user, method: "POST", target: "/x", path: map[string]string{"id": claimID}, form: form})
		return w.Code, w.Body.String()
	}
	count := func(user string) string {
		return invDo(t, d.InventoryClaimsCount(), invReq{userID: user, method: "GET", target: "/x"}).Body.String()
	}

	t.Run("only inventory admins may see or act on claims", func(t *testing.T) {
		for _, user := range []string{manager, viewer} {
			code, _ := page(user)
			assert.Equal(t, http.StatusForbidden, code)
			code, _ = link(user, claim.ID, url.Values{"user_id": {alice}})
			assert.Equal(t, http.StatusForbidden, code)
			assert.Empty(t, count(user), "no badge for people who can't act on claims")
		}
		got, _ := store.GetCustodyEvent(ctx, pool, claim.ID)
		assert.Equal(t, "pending_unmatched", got.Status)
	})

	t.Run("the page lists each claim with what the claimant typed and the previous holder", func(t *testing.T) {
		code, body := page(admin)
		require.Equal(t, http.StatusOK, code)
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "K7M-4QX")
		assert.Contains(t, body, "Robert &lt;R&gt;", "claimant input is escaped")
		assert.Contains(t, body, "robert@elsewhere.example")
		assert.Contains(t, body, "Alice Anderson", "the previous holder")
		assert.Contains(t, body, "/admin/fragments/inventory/claims/"+claim.ID+"/link")
		assert.Contains(t, body, "Alice Anderson (alice@example.com)", "members are offered with their email")
		assert.Contains(t, body, "Bob Brown")
		assert.NotContains(t, body, "Helpful Bot", "bots are not offered")
		assert.Contains(t, body, "/admin/fragments/inventory/claims-count", "the nav badge is wired")
	})

	t.Run("the badge shows the pending count", func(t *testing.T) {
		assert.Contains(t, count(admin), `>1</span>`)
	})

	t.Run("linking needs a real member", func(t *testing.T) {
		_, body := link(admin, claim.ID, url.Values{})
		assert.Contains(t, body, "Choose a member.")
		_, body = link(admin, claim.ID, url.Values{"user_id": {"00000000-0000-0000-0000-000000000000"}})
		assert.Contains(t, body, "could not be found")
		_, body = link(admin, "00000000-0000-0000-0000-000000000000", url.Values{"user_id": {alice}})
		assert.Contains(t, body, "Claim not found.")
	})

	t.Run("linking gives the member the item and clears the claim", func(t *testing.T) {
		var bobID string
		require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM users WHERE display_name = 'Bob Brown'`).Scan(&bobID))

		code, body := link(admin, claim.ID, url.Values{"user_id": {bobID}})
		require.Equal(t, http.StatusNoContent, code, body)
		expectCall(t, linked, [2]string{item.ID, bobID})

		got, _ := store.GetInventoryItem(ctx, pool, item.ID)
		assert.Equal(t, bobID, *got.CurrentHolderUserID)
		ev, _ := store.GetCustodyEvent(ctx, pool, claim.ID)
		assert.Equal(t, "linked", ev.Status)
		assert.Equal(t, alice, *ev.PreviousHolderUserID)

		_, body = page(admin)
		assert.NotContains(t, body, "robert@elsewhere.example")
		assert.Empty(t, count(admin), "the badge disappears with the last claim")

		_, body = link(admin, claim.ID, url.Values{"user_id": {bobID}})
		assert.Contains(t, body, "already resolved")
	})

	t.Run("an archived item can't be linked", func(t *testing.T) {
		gone, err := store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "Retired"}, "GON222")
		require.NoError(t, err)
		pending, err := store.RecordPendingClaim(ctx, pool, gone.ID, "Someone", "someone@elsewhere.example", nil, nil)
		require.NoError(t, err)
		require.NoError(t, store.ArchiveInventoryItem(ctx, pool, gone.ID))

		_, body := link(admin, pending.ID, url.Values{"user_id": {alice}})
		assert.Contains(t, body, "archived")
		ev, _ := store.GetCustodyEvent(ctx, pool, pending.ID)
		assert.Equal(t, "pending_unmatched", ev.Status)
	})

	t.Run("only a successful link notifies the member", func(t *testing.T) {
		// The forbidden, invalid, already-resolved, and archived attempts above must not have produced a call.
		assertNoCall(t, linked)
	})
}
