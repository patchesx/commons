package slack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/permissions"
	"commons/store"
)

// browseAction delivers a browse modal interaction: the clicked action plus what is currently typed in the
// search box and chosen in the filter, as Slack sends them in the view state.
func (e *invEnv) browseAction(t *testing.T, slackUser, actionID, value, search, filter string) {
	t.Helper()
	e.dispatch(t, map[string]any{
		"type":       "block_actions",
		"user":       map[string]any{"id": slackUser},
		"trigger_id": "trig",
		"view": map[string]any{
			"id": "V1", "hash": "h1",
			"state": map[string]any{"values": map[string]any{
				inventoryBrowseSearchBlockID: map[string]any{
					ActionInventoryBrowseSearch: map[string]any{"type": "plain_text_input", "value": search},
				},
				inventoryBrowseFilterBlockID: map[string]any{
					ActionInventoryBrowseFilter: map[string]any{"type": "static_select", "selected_option": map[string]any{"value": filter}},
				},
			}},
		},
		"actions": []any{map[string]any{"type": "button", "block_id": "b1", "action_id": actionID, "value": value}},
	})
}

func TestInventoryBrowseModalLayout(t *testing.T) {
	loc := "Closet"
	rows := []store.InventoryGridRow{
		{InventoryItem: store.InventoryItem{ID: "1", Name: "A <b> & B", Code: "K7M4QX", Location: &loc}, HolderName: "Alice <A>", ClaimPending: true},
		{InventoryItem: store.InventoryItem{ID: "2", Name: "Camera", Code: "CAM222"}},
	}
	holder := "u1"
	rows[0].CurrentHolderUserID = &holder

	t.Run("each row shows name, code, holder, last verified, and the claim marker", func(t *testing.T) {
		body := modalJSON(t, InventoryBrowseModal(inventoryBrowseData{Filter: browseFilterAll, Page: 1, Pages: 1, Total: 2, Rows: rows}))
		assert.Contains(t, body, "*All items* — 2 items")
		assert.Contains(t, body, "A &lt;b&gt; &amp; B", "names are escaped")
		assert.Contains(t, body, "Alice &lt;A&gt;", "so are holder names")
		assert.Contains(t, body, "K7M-4QX")
		assert.Equal(t, 1, strings.Count(body, ":warning: Claim pending"), "the marker shows on the one item with a claim waiting")
		assert.Contains(t, body, "Holder: nobody")
		assert.Contains(t, body, "Last verified: never")
		assert.NotContains(t, body, "Page 1 of", "no pager for a single page")
	})

	t.Run("it is read-only", func(t *testing.T) {
		m := InventoryBrowseModal(inventoryBrowseData{Filter: browseFilterAll, Page: 1, Pages: 3, Total: 25, Rows: rows})
		body := modalJSON(t, m)
		for _, forbidden := range []string{ActionInventoryClaim, ActionInventoryVerify, ActionInventoryHandOff, ActionInventoryAdminRevert, "Confirm possession"} {
			assert.NotContains(t, body, forbidden)
		}
		assert.Nil(t, m.Submit)
		assert.Equal(t, ModalIDInventoryBrowse, m.CallbackID)
	})

	t.Run("search box and filter keep their values", func(t *testing.T) {
		body := modalJSON(t, InventoryBrowseModal(inventoryBrowseData{Search: "proj", Filter: browseFilterAwaiting, Page: 1, Pages: 1}))
		assert.Contains(t, body, `"initial_value":"proj"`)
		assert.Contains(t, body, `"dispatch_action":true`)
		assert.Contains(t, body, "*Awaiting verification* — 0 items")
		assert.Contains(t, body, "_No items found._")
		for _, f := range inventoryBrowseFilters {
			assert.Contains(t, body, `"value":"`+f.value+`"`)
		}
		assert.Regexp(t, `"initial_option":\{"text":\{[^}]*Awaiting verification[^}]*\},"value":"awaiting"`, body)
	})

	t.Run("paging buttons depend on the page", func(t *testing.T) {
		first := modalJSON(t, InventoryBrowseModal(inventoryBrowseData{Filter: browseFilterAll, Page: 1, Pages: 3, Total: 25}))
		assert.Contains(t, first, "Page 1 of 3")
		assert.Contains(t, first, `"value":"page:2"`)
		assert.NotContains(t, first, ActionInventoryBrowsePrev)

		middle := modalJSON(t, InventoryBrowseModal(inventoryBrowseData{Filter: browseFilterAll, Page: 2, Pages: 3, Total: 25}))
		assert.Contains(t, middle, `"value":"page:1"`)
		assert.Contains(t, middle, `"value":"page:3"`)

		last := modalJSON(t, InventoryBrowseModal(inventoryBrowseData{Filter: browseFilterAll, Page: 3, Pages: 3, Total: 25}))
		assert.Contains(t, last, ActionInventoryBrowsePrev)
		assert.NotContains(t, last, ActionInventoryBrowseNext)
	})

	t.Run("a full page stays under Slack's block limit", func(t *testing.T) {
		var many []store.InventoryGridRow
		for i := range inventoryBrowsePageSize {
			many = append(many, store.InventoryGridRow{InventoryItem: store.InventoryItem{ID: fmt.Sprint(i), Name: fmt.Sprintf("Thing %d", i), Code: "ABC234"}})
		}
		m := InventoryBrowseModal(inventoryBrowseData{Filter: browseFilterAll, Page: 2, Pages: 3, Total: 25, Rows: many})
		assert.LessOrEqual(t, len(m.Blocks.BlockSet), 100)
	})
}

func TestInventoryBrowseFlow(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	viewer := e.user(t, "U_VIEW", "Vera Member") // an ordinary member: browsing needs no permission
	alice := e.user(t, "U_ALICE", "Alice Anderson")

	for i := 0; i < 25; i++ {
		e.item(t, fmt.Sprintf("THG%03d", i+200), fmt.Sprintf("Thing %02d", i))
	}
	mine := e.item(t, "MIN222", "Vera's drill")
	_, err := store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: mine.ID, UserID: viewer.ID, Source: "slack"})
	require.NoError(t, err)
	theirs := e.item(t, "THR222", "Alice's saw")
	_, err = store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: theirs.ID, UserID: alice.ID, Source: "slack"})
	require.NoError(t, err)

	lastUpdate := func() string { return e.fake.lastView(t, "views.update") }

	t.Run("opening shows the first page of everything", func(t *testing.T) {
		e.click(t, "U_VIEW", ActionInventoryBrowseOpen, "open")
		body := e.fake.lastView(t, "views.open")
		assert.Contains(t, body, "*All items* — 27 items")
		assert.Contains(t, body, "Alice's saw", "name order: Alice's saw is first")
		assert.Contains(t, body, "Thing 07")
		assert.NotContains(t, body, "Thing 10", "ten to a page")
		assert.Contains(t, body, "Page 1 of 3")
		assert.Contains(t, body, "Holder: Alice Anderson")
	})

	t.Run("next and previous move through the pages and keep the search and filter", func(t *testing.T) {
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseNext, "page:2", "", browseFilterAll)
		body := lastUpdate()
		assert.Contains(t, body, "Page 2 of 3")
		assert.Contains(t, body, "Thing 09", "page one ends at Thing 08")
		assert.Contains(t, body, "Thing 18")
		assert.NotContains(t, body, "Thing 08")
		assert.NotContains(t, body, "Alice's saw")
		assert.Contains(t, body, `"value":"page:1"`)
		assert.Contains(t, body, `"value":"page:3"`)

		e.browseAction(t, "U_VIEW", ActionInventoryBrowseNext, "page:3", "", browseFilterAll)
		assert.Contains(t, lastUpdate(), "Page 3 of 3")

		e.browseAction(t, "U_VIEW", ActionInventoryBrowsePrev, "page:2", "thing", browseFilterAll)
		body = lastUpdate()
		assert.Contains(t, body, `"initial_value":"thing"`, "paging keeps the search term")
		assert.Contains(t, body, "25 items")
	})

	t.Run("searching goes back to page one", func(t *testing.T) {
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseSearch, "Thing 2", "Thing 2", browseFilterAll)
		body := lastUpdate()
		assert.Contains(t, body, "5 items", "Thing 20 to Thing 24")
		assert.NotContains(t, body, "Page 1 of", "one page")
		assert.Contains(t, body, "Thing 24")
		assert.NotContains(t, body, "Thing 19")

		e.browseAction(t, "U_VIEW", ActionInventoryBrowseSearch, "alice", "alice", browseFilterAll)
		body = lastUpdate()
		assert.Contains(t, body, "Alice's saw")
		assert.NotContains(t, body, "Thing 00")

		e.browseAction(t, "U_VIEW", ActionInventoryBrowseSearch, "k7m", "THG200", browseFilterAll)
		assert.Contains(t, lastUpdate(), "THG-200", "codes are searchable")
	})

	t.Run("the filters", func(t *testing.T) {
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseFilter, "", "", browseFilterMine)
		body := lastUpdate()
		assert.Contains(t, body, "*Held by me* — 1 item")
		assert.Contains(t, body, "Vera's drill")
		assert.NotContains(t, body, "Alice's saw")

		e.browseAction(t, "U_VIEW", ActionInventoryBrowseFilter, "", "", browseFilterAwaiting)
		assert.Contains(t, lastUpdate(), "_No items found._", "nobody has been reminded yet")
		require.NoError(t, store.MarkInventoryItemReminded(e.ctx, e.pool, theirs.ID))
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseFilter, "", "", browseFilterAwaiting)
		body = lastUpdate()
		assert.Contains(t, body, "*Awaiting verification* — 1 item")
		assert.Contains(t, body, "Alice's saw")

		pending := e.item(t, "PND222", "Pending thing")
		_, err := store.RecordPendingClaim(e.ctx, e.pool, pending.ID, "Robert", "robert@elsewhere.example", nil, nil)
		require.NoError(t, err)
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseFilter, "", "", browseFilterPending)
		body = lastUpdate()
		assert.Contains(t, body, "Pending thing")
		assert.Contains(t, body, "Claim pending")
		assert.NotContains(t, body, "Alice's saw")

		e.browseAction(t, "U_VIEW", ActionInventoryBrowseFilter, "", "", browseFilterAll)
		assert.Contains(t, lastUpdate(), "Claim pending", "the marker shows in the full list too")
	})

	t.Run("archived items are left out", func(t *testing.T) {
		gone := e.item(t, "GON222", "Retired gadget")
		require.NoError(t, store.ArchiveInventoryItem(e.ctx, e.pool, gone.ID))
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseSearch, "retired", "retired", browseFilterAll)
		assert.Contains(t, lastUpdate(), "_No items found._")
	})

	t.Run("any member can browse, with no inventory permission at all", func(t *testing.T) {
		opens, updates := len(e.fake.viewCalls("views.open")), len(e.fake.viewCalls("views.update"))
		e.click(t, "U_VIEW", ActionInventoryBrowseOpen, "open")
		for _, a := range []struct{ id, value string }{
			{ActionInventoryBrowseSearch, "thing"},
			{ActionInventoryBrowseFilter, ""},
			{ActionInventoryBrowseNext, "page:2"},
			{ActionInventoryBrowsePrev, "page:1"},
		} {
			e.browseAction(t, "U_VIEW", a.id, a.value, "thing", browseFilterAll)
		}
		assert.Len(t, e.fake.viewCalls("views.open"), opens+1)
		assert.Len(t, e.fake.viewCalls("views.update"), updates+4)
	})

	t.Run("someone the app has not met yet can browse too", func(t *testing.T) {
		_, err := store.GetUserByExternalID(e.ctx, e.pool, "slack", "U_NEWBIE")
		require.ErrorIs(t, err, store.ErrNotFound)
		opens := len(e.fake.viewCalls("views.open"))
		e.click(t, "U_NEWBIE", ActionInventoryBrowseOpen, "open")
		assert.Len(t, e.fake.viewCalls("views.open"), opens+1)
		_, err = store.GetUserByExternalID(e.ctx, e.pool, "slack", "U_NEWBIE")
		assert.NoError(t, err, "they are recognized as a member from then on")
	})

	t.Run("it stays read-only for everyone", func(t *testing.T) {
		e.click(t, "U_VIEW", ActionInventoryBrowseOpen, "open")
		body := e.fake.lastView(t, "views.open")
		for _, action := range []string{ActionInventoryClaim, ActionInventoryVerify, ActionInventoryHandOff, ActionInventoryAdminRevert} {
			assert.NotContains(t, body, action)
		}
	})

	t.Run("a page past the end shows the last page", func(t *testing.T) {
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseNext, "page:99", "", browseFilterAll)
		assert.Contains(t, lastUpdate(), "Page 3 of 3")
		e.browseAction(t, "U_VIEW", ActionInventoryBrowseNext, "page:abc", "", browseFilterAll)
		assert.Contains(t, lastUpdate(), "Page 1 of 3", "a garbled page value means page one")
	})
}

func TestHomeBrowseInventoryButton(t *testing.T) {
	e := newInvEnv(t)
	e.user(t, "U_PLAIN", "Pat Plain") // a member with no role
	viewer := e.user(t, "U_VIEW", "Vic Viewer")
	role, err := store.CreateRole(e.ctx, e.pool, "role-view", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(e.ctx, e.pool, role.ID, permissions.InventoryView))
	group, err := store.CreateRoleGroup(e.ctx, e.pool, "group-view", "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(e.ctx, e.pool, group.ID, role.ID))
	require.NoError(t, store.AssignGroupToUser(e.ctx, e.pool, viewer.ID, group.ID))

	// The Home buttons are behind inventory.view, which acts as a rollout flag; the lists and actions behind them
	// check no permission (see TestInventoryBrowseFlow).
	Publish(e.ctx, e.pool, e.encKey, e.client, "U_VIEW")
	home := e.fake.lastView(t, "views.publish")
	assert.Contains(t, home, "Browse Inventory")
	assert.Contains(t, home, ActionInventoryBrowseOpen)
	assert.Contains(t, home, ActionOpenInventory)

	Publish(e.ctx, e.pool, e.encKey, e.client, "U_PLAIN")
	home = e.fake.lastView(t, "views.publish")
	assert.NotContains(t, home, ActionInventoryBrowseOpen, "without inventory.view the buttons are not shown")
	assert.NotContains(t, home, ActionOpenInventory)
}
