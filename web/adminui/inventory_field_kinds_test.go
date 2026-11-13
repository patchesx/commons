package adminui

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

func TestInventorySelectFieldHandlers(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, pool, "manager", permissions.InventoryManage)
	viewer := invGrant(t, pool, "viewer", permissions.InventoryView)

	create := func(form url.Values) string {
		return invDo(t, d.InventoryFieldsCreate(), invReq{userID: manager, method: "POST", target: "/x", form: form}).Body.String()
	}
	field := func(key string) store.InventoryFieldDefinition {
		defs, err := store.ListInventoryFieldDefinitions(ctx, pool, false)
		require.NoError(t, err)
		for _, f := range defs {
			if f.Key == key {
				return f
			}
		}
		t.Fatalf("no field %q", key)
		return store.InventoryFieldDefinition{}
	}

	t.Run("a new field can be a dropdown", func(t *testing.T) {
		w := invDo(t, d.InventoryFieldsCreate(), invReq{userID: manager, method: "POST", target: "/x",
			form: url.Values{"label": {"Type"}, "kind": {"select"}, "options": {"A/V\r\nEvents\n\n a/v "}}})
		require.Equal(t, http.StatusNoContent, w.Code)
		f := field("type")
		assert.Equal(t, store.InventoryFieldSelect, f.Kind)
		assert.Equal(t, []string{"A/V", "Events"}, f.Options)
	})

	t.Run("a dropdown with no choices is refused and nothing is created", func(t *testing.T) {
		body := create(url.Values{"label": {"Empty"}, "kind": {"select"}, "options": {"  \n"}})
		assert.Contains(t, body, "Add at least one choice")
		defs, _ := store.ListInventoryFieldDefinitions(ctx, pool, false)
		assert.Len(t, defs, 1)
	})

	t.Run("a plain field stays text", func(t *testing.T) {
		require.Equal(t, http.StatusNoContent, invDo(t, d.InventoryFieldsCreate(), invReq{userID: manager, method: "POST", target: "/x",
			form: url.Values{"label": {"Serial"}}}).Code)
		assert.Equal(t, store.InventoryFieldText, field("serial").Kind)
	})

	t.Run("an existing text field converts, and the page offers the values in use", func(t *testing.T) {
		item, err := store.CreateInventoryItemWithNewCode(ctx, pool, store.InventoryItemInput{Name: "Thing"})
		require.NoError(t, err)
		require.NoError(t, store.SetInventoryItemMetadata(ctx, pool, item.ID, map[string]string{"serial": "SN-1"}))

		page := invDo(t, d.InventoryFieldsPage(), invReq{userID: manager, method: "GET", target: "/x"}).Body.String()
		assert.Contains(t, page, "SN-1", "existing values are listed as the starting choices")
		assert.Contains(t, page, "A/V\nEvents", "a dropdown's own list fills its box")

		serial := field("serial")
		w := invDo(t, d.InventoryFieldsUpdate(), invReq{userID: manager, method: "PUT", target: "/x", path: map[string]string{"id": serial.ID},
			form: url.Values{"label": {"Serial"}, "active": {"on"}, "kind": {"select"}, "options": {"SN-1\nSN-2"}}})
		require.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, store.InventoryFieldSelect, field("serial").Kind)

		w = invDo(t, d.InventoryFieldsUpdate(), invReq{userID: manager, method: "PUT", target: "/x", path: map[string]string{"id": serial.ID},
			form: url.Values{"label": {"Serial"}, "active": {"on"}, "kind": {"select"}, "options": {""}}})
		assert.Contains(t, w.Body.String(), "Add at least one choice")
		assert.Equal(t, store.InventoryFieldSelect, field("serial").Kind, "a refused save changes nothing")

		w = invDo(t, d.InventoryFieldsUpdate(), invReq{userID: manager, method: "PUT", target: "/x", path: map[string]string{"id": serial.ID},
			form: url.Values{"label": {"Serial"}, "active": {"on"}, "kind": {"text"}}})
		require.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, store.InventoryFieldText, field("serial").Kind)
	})

	t.Run("viewers see the list but no editor", func(t *testing.T) {
		page := invDo(t, d.InventoryFieldsPage(), invReq{userID: viewer, method: "GET", target: "/x"}).Body.String()
		assert.Contains(t, page, "Dropdown: A/V")
		assert.NotContains(t, page, "<textarea")
	})
}

func TestInventoryGridDropdownCell(t *testing.T) {
	f := newGridFixture(t)
	ctx := t.Context()
	defs, err := store.ListInventoryFieldDefinitions(ctx, f.d.Pool, false)
	require.NoError(t, err)
	var serial store.InventoryFieldDefinition
	for _, d := range defs {
		if d.Key == "serial" {
			serial = d
		}
	}
	require.NoError(t, store.SetInventoryFieldKind(ctx, f.d.Pool, serial.ID, store.InventoryFieldSelect, []string{"SN-100", "SN-200"}))
	cell := func(it *store.InventoryItem) map[string]string {
		return map[string]string{"id": it.ID, "col": "field:serial"}
	}

	t.Run("the editor is a dropdown with the current value selected", func(t *testing.T) {
		body := f.get(t, f.d.InventoryGridCellEdit(), f.manager, "/x", cell(f.projector)).Body.String()
		assert.Contains(t, body, "<select")
		assert.Contains(t, body, `<option value="SN-200">`)
		assert.Contains(t, body, `<option value="SN-100" selected>`)
		assert.NotContains(t, body, `type="text"`)
	})

	t.Run("a value that left the list stays selectable", func(t *testing.T) {
		body := f.get(t, f.d.InventoryGridCellEdit(), f.manager, "/x", cell(f.camera)).Body.String() // SN-300
		assert.Contains(t, body, `<option value="SN-300" selected>`)
	})

	t.Run("saving a choice works and anything else is refused", func(t *testing.T) {
		save := func(v string) string {
			return invDo(t, f.d.InventoryGridCellSave(), invReq{userID: f.manager, method: "PATCH", target: "/x", path: cell(f.laptop), form: url.Values{"value": {v}}}).Body.String()
		}
		assert.Contains(t, save("Hacked"), "Pick one of the choices")
		got, _ := store.GetInventoryItem(ctx, f.d.Pool, f.laptop.ID)
		assert.Equal(t, "SN-200", got.Metadata["serial"])

		save("SN-100")
		got, _ = store.GetInventoryItem(ctx, f.d.Pool, f.laptop.ID)
		assert.Equal(t, "SN-100", got.Metadata["serial"])

		save("")
		got, _ = store.GetInventoryItem(ctx, f.d.Pool, f.laptop.ID)
		assert.NotContains(t, got.Metadata, "serial", "the blank choice clears it")
	})

	t.Run("text fields still get a text box", func(t *testing.T) {
		body := f.get(t, f.d.InventoryGridCellEdit(), f.manager, "/x", map[string]string{"id": f.camera.ID, "col": "name"}).Body.String()
		assert.Contains(t, body, `type="text"`)
		assert.NotContains(t, body, "<select")
	})
}
