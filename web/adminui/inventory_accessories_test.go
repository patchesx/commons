package adminui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

func TestInventoryAccessoryHandlers(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, pool, "manager", permissions.InventoryManage)
	viewer := invGrant(t, pool, "viewer", permissions.InventoryView)

	item, err := store.CreateInventoryItemWithNewCode(ctx, pool, store.InventoryItemInput{Name: "PA system"})
	require.NoError(t, err)
	bare, err := store.CreateInventoryItemWithNewCode(ctx, pool, store.InventoryItemInput{Name: "6ft table"})
	require.NoError(t, err)
	id := map[string]string{"id": item.ID}

	add := func(name string) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryAccessoryAdd(), invReq{userID: manager, method: "POST", target: "/x", path: id, form: url.Values{"name": {name}}})
	}

	require.Equal(t, http.StatusNoContent, add("Speaker").Code)
	require.Equal(t, http.StatusNoContent, add("Stand").Code)

	w := add("speaker")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "already has an accessory")
	w = add("   ")
	assert.Contains(t, w.Body.String(), "Enter a name")

	t.Run("they show on the item's detail page and nowhere in the lists", func(t *testing.T) {
		w := invDo(t, d.InventoryDetailPage(), invReq{userID: viewer, method: "GET", target: "/x", path: id})
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "Accessories")
		assert.Contains(t, body, "Speaker")
		assert.Contains(t, body, "Stand")
		assert.NotContains(t, body, "Add an accessory", "a viewer can't edit")
		assert.NotContains(t, body, "Remove Speaker")

		w = invDo(t, d.InventoryDetailPage(), invReq{userID: manager, method: "GET", target: "/x", path: id})
		assert.Contains(t, w.Body.String(), "Add an accessory")
		assert.Contains(t, w.Body.String(), "Remove Speaker")

		w = invDo(t, d.InventoryDetailPage(), invReq{userID: viewer, method: "GET", target: "/x", path: map[string]string{"id": bare.ID}})
		assert.Contains(t, w.Body.String(), "No accessories")

		for name, h := range map[string]http.Handler{"list": d.InventoryTable(), "grid": d.InventoryGridFragment()} {
			w := invDo(t, h, invReq{userID: viewer, method: "GET", target: "/x"})
			require.Equal(t, http.StatusOK, w.Code, name)
			assert.NotContains(t, w.Body.String(), "Speaker", name)
			assert.Contains(t, w.Body.String(), "PA system", name)
		}
	})

	t.Run("the export carries them in one cell the import reads back", func(t *testing.T) {
		w := invDo(t, d.InventoryGridCSV(), invReq{userID: viewer, method: "GET", target: "/x"})
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"Speaker, Stand"`)
	})

	t.Run("delete", func(t *testing.T) {
		list, err := store.ListInventoryAccessories(ctx, pool, item.ID)
		require.NoError(t, err)
		require.Len(t, list, 2)
		del := func(itemID, accID string) string {
			return invDo(t, d.InventoryAccessoryDelete(), invReq{userID: manager, method: "DELETE", target: "/x",
				path: map[string]string{"id": itemID, "accessoryID": accID}}).Body.String()
		}
		assert.Contains(t, del(bare.ID, list[0].ID), "accessory not found", "not through another item")
		assert.Contains(t, del(item.ID, "not-a-uuid"), "accessory not found")
		still, _ := store.ListInventoryAccessories(ctx, pool, item.ID)
		require.Len(t, still, 2)
		assert.NotContains(t, del(item.ID, list[0].ID), "not found")
		left, _ := store.ListInventoryAccessories(ctx, pool, item.ID)
		assert.Len(t, left, 1)
	})
}
