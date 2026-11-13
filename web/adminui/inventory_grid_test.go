package adminui

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

type gridFixture struct {
	d                                  Deps
	viewer, manager, none              string
	camera, laptop, projector, retired *store.InventoryItem
	alice                              string
}

func newGridFixture(t *testing.T) *gridFixture {
	t.Helper()
	pool := testhelpers.SetupTestDB(t)
	ctx := t.Context()
	f := &gridFixture{d: Deps{Pool: pool, EncKey: testhelpers.EncKey()}}
	f.viewer = invGrant(t, pool, "viewer", permissions.InventoryView)
	f.manager = invGrant(t, pool, "manager", permissions.InventoryManage)
	f.none = invGrant(t, pool, "none", permissions.LibraryView)

	mk := func(code, name, location string) *store.InventoryItem {
		in := store.InventoryItemInput{Name: name}
		if location != "" {
			in.Location = &location
		}
		it, err := store.CreateInventoryItem(ctx, pool, in, code)
		require.NoError(t, err)
		return it
	}
	f.camera = mk("CAM222", "Camera", "Studio")
	f.laptop = mk("LAP222", "Laptop", "")
	f.projector = mk("K7M4QX", "Projector", "Closet")
	f.retired = mk("OLD222", "Old tripod", "")

	f.alice = invUser(t, pool, "Alice Anderson")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, f.alice)
	_, err := store.ApplyCustodyChange(ctx, pool, store.CustodyChange{ItemID: f.projector.ID, UserID: f.alice, Source: "slack"})
	require.NoError(t, err)

	_, err = store.CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial")
	require.NoError(t, err)
	color, err := store.CreateInventoryFieldDefinition(ctx, pool, "color", "Color")
	require.NoError(t, err)
	require.NoError(t, store.SetInventoryItemMetadata(ctx, pool, f.camera.ID, map[string]string{"serial": "SN-300", "color": "black"}))
	require.NoError(t, store.SetInventoryItemMetadata(ctx, pool, f.projector.ID, map[string]string{"serial": "SN-100"}))
	require.NoError(t, store.SetInventoryItemMetadata(ctx, pool, f.laptop.ID, map[string]string{"serial": "SN-200"}))
	require.NoError(t, store.UpdateInventoryFieldDefinition(ctx, pool, color.ID, "Color", false)) // now inactive

	_, err = store.RecordPendingClaim(ctx, pool, f.camera.ID, "Robert", "robert@elsewhere.example", nil, nil)
	require.NoError(t, err)
	require.NoError(t, store.ArchiveInventoryItem(ctx, pool, f.retired.ID))
	return f
}

func (f *gridFixture) get(t *testing.T, h http.Handler, user, target string, path map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return invDo(t, h, invReq{userID: user, method: "GET", target: target, path: path})
}

// order returns the names in the order they appear in body, as indexes, for checking sort order.
func appearsBefore(body, first, second string) bool {
	a, b := strings.Index(body, first), strings.Index(body, second)
	return a >= 0 && b >= 0 && a < b
}

func TestInventoryGridPermissions(t *testing.T) {
	f := newGridFixture(t)
	cell := map[string]string{"id": f.camera.ID, "col": "name"}

	reads := []struct {
		name string
		h    http.Handler
		path map[string]string
	}{
		{"page", f.d.InventoryGridPage(), nil},
		{"fragment", f.d.InventoryGridFragment(), nil},
		{"csv", f.d.InventoryGridCSV(), nil},
		{"cell", f.d.InventoryGridCell(), cell},
	}
	for _, c := range reads {
		assert.Equal(t, http.StatusForbidden, f.get(t, c.h, f.none, "/x", c.path).Code, "%s without permission", c.name)
		assert.Equal(t, http.StatusOK, f.get(t, c.h, f.viewer, "/x", c.path).Code, "%s for a viewer", c.name)
		assert.Equal(t, http.StatusOK, f.get(t, c.h, f.manager, "/x", c.path).Code, "%s for a manager", c.name)
	}

	assert.Equal(t, http.StatusForbidden, f.get(t, f.d.InventoryGridCellEdit(), f.none, "/x", cell).Code)
	assert.Equal(t, http.StatusForbidden, f.get(t, f.d.InventoryGridCellEdit(), f.viewer, "/x", cell).Code, "viewers can't edit")
	assert.Equal(t, http.StatusOK, f.get(t, f.d.InventoryGridCellEdit(), f.manager, "/x", cell).Code)

	for _, user := range []string{f.none, f.viewer} {
		w := invDo(t, f.d.InventoryGridCellSave(), invReq{userID: user, method: "PATCH", target: "/x", path: cell, form: url.Values{"value": {"Hacked"}}})
		assert.Equal(t, http.StatusForbidden, w.Code)
	}
	got, _ := store.GetInventoryItem(t.Context(), f.d.Pool, f.camera.ID)
	assert.Equal(t, "Camera", got.Name)
}

func TestInventoryGridPage(t *testing.T) {
	f := newGridFixture(t)
	page := func(user, query string) string {
		w := f.get(t, f.d.InventoryGridPage(), user, "/admin/inventory/grid"+query, nil)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	t.Run("columns, holder, and the claim pending marker", func(t *testing.T) {
		body := page(f.viewer, "")
		for _, want := range []string{"Name", "Code", "Location", "Holder", "Last verified", "Serial"} {
			assert.Contains(t, body, want)
		}
		assert.NotContains(t, body, ">Color", "inactive fields get no column")
		assert.NotContains(t, body, "black", "and their values aren't shown")
		assert.Contains(t, body, "Alice Anderson")
		assert.Contains(t, body, "K7M-4QX")
		assert.Contains(t, body, "SN-300")
		assert.Contains(t, body, "Claim pending")
		assert.Equal(t, 1, strings.Count(body, "Claim pending</span>"), "only the camera has an unmatched claim")
		assert.Contains(t, body, "3 items")
		assert.Contains(t, body, "/admin/inventory/grid", "the nav item")
	})

	t.Run("archived items are hidden unless asked for", func(t *testing.T) {
		assert.NotContains(t, page(f.viewer, ""), "Old tripod")
		body := page(f.viewer, "?archived=1")
		assert.Contains(t, body, "Old tripod")
		assert.Contains(t, body, ">Archived</span>")
		assert.Contains(t, body, "4 items")
		assert.Regexp(t, `name="archived"[^>]*checked`, body, "the checkbox reflects the filter")
	})

	t.Run("search and the claim pending filter narrow the rows", func(t *testing.T) {
		body := page(f.viewer, "?q=alice")
		assert.Contains(t, body, "Projector")
		assert.NotContains(t, body, "Camera")
		assert.Contains(t, body, `value="alice"`, "the search box keeps the term")

		body = page(f.viewer, "?pending=1")
		assert.Contains(t, body, "Camera")
		assert.NotContains(t, body, "Projector")
		assert.NotContains(t, body, "Laptop")

		body = page(f.viewer, "?q=zzz-nothing")
		assert.Contains(t, body, "No items match.")
	})

	t.Run("sorting", func(t *testing.T) {
		body := page(f.viewer, "")
		assert.True(t, appearsBefore(body, "Camera", "Laptop") && appearsBefore(body, "Laptop", "Projector"), "name order by default")

		body = page(f.viewer, "?sort=name&dir=desc")
		assert.True(t, appearsBefore(body, "Projector", "Laptop") && appearsBefore(body, "Laptop", "Camera"))

		body = page(f.viewer, "?sort=field:serial")
		assert.True(t, appearsBefore(body, "SN-100", "SN-200") && appearsBefore(body, "SN-200", "SN-300"), "by a custom field")

		body = page(f.viewer, "?sort=field:color")
		assert.True(t, appearsBefore(body, "Camera", "Laptop"), "an inactive field can't be sorted by, so it falls back to name")
		body = page(f.viewer, "?sort=holder&dir=desc")
		assert.True(t, appearsBefore(body, "Projector", "Camera"), "holders sort too")
	})

	t.Run("column headings sort, and reverse when already sorted", func(t *testing.T) {
		body := page(f.viewer, "?sort=name")
		assert.Contains(t, body, "dir=desc&amp;page=1&amp;sort=name", "Name is sorted ascending, so its heading reverses it")
		assert.Contains(t, body, "page=1&amp;sort=code", "other headings start ascending")
		assert.NotContains(t, body, "dir=desc&amp;page=1&amp;sort=code")
		assert.Contains(t, body, "sort=field%3Aserial", "custom field headings sort too")
		assert.Contains(t, body, "Name ▲")
		body = page(f.viewer, "?sort=name&dir=desc")
		assert.Contains(t, body, "Name ▼")
	})

	t.Run("the fragment is just the table, with the sort state for the filters", func(t *testing.T) {
		w := f.get(t, f.d.InventoryGridFragment(), f.viewer, "/admin/fragments/inventory-grid?q=cam&sort=location&dir=desc", nil)
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.NotContains(t, body, "<html")
		assert.Contains(t, body, `id="inventory-grid"`)
		assert.Contains(t, body, `id="grid-sort" name="sort" value="location"`)
		assert.Contains(t, body, `id="grid-dir" name="dir" value="desc"`)
		assert.Contains(t, body, "Camera")
		assert.NotContains(t, body, "Projector")
	})

	t.Run("only people who can manage get editable cells", func(t *testing.T) {
		viewerBody := page(f.viewer, "")
		assert.NotContains(t, viewerBody, "Click to edit")

		managerBody := page(f.manager, "")
		assert.Contains(t, managerBody, `title="Click to edit"`)
		assert.Contains(t, managerBody, "/admin/fragments/inventory-grid/"+f.camera.ID+"/cell/name/edit")
		assert.Contains(t, managerBody, "/admin/fragments/inventory-grid/"+f.camera.ID+"/cell/field:serial/edit")
		assert.NotContains(t, managerBody, "/cell/code", "the code isn't editable")
		assert.NotContains(t, managerBody, "/cell/holder", "neither is the holder")

		archivedBody := page(f.manager, "?archived=1")
		assert.NotContains(t, archivedBody, "/grid/"+f.retired.ID+"/cell/", "archived items have no editable cells")
	})
}

func TestInventoryGridPaging(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	viewer := invGrant(t, pool, "viewer", permissions.InventoryView)
	for i := 0; i < 25; i++ {
		_, err := store.CreateInventoryItem(t.Context(), pool, store.InventoryItemInput{Name: fmt.Sprintf("Thing %02d", i)}, fmt.Sprintf("THG%03d", i+200))
		require.NoError(t, err)
	}
	frag := func(query string) string {
		w := invDo(t, d.InventoryGridFragment(), invReq{userID: viewer, method: "GET", target: "/x" + query})
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	body := frag("")
	assert.Contains(t, body, "Thing 00")
	assert.Contains(t, body, "Thing 19")
	assert.NotContains(t, body, "Thing 20")
	assert.Contains(t, body, "25 items")
	assert.Contains(t, body, "Page 1 of 2")
	assert.Contains(t, body, "page=2", "a next link")

	body = frag("?page=2")
	assert.Contains(t, body, "Thing 20")
	assert.Contains(t, body, "Thing 24")
	assert.NotContains(t, body, "Thing 19")
	assert.Contains(t, body, "Page 2 of 2")
	assert.Contains(t, body, "page=1", "a previous link")

	body = frag("?page=99")
	assert.Contains(t, body, "Page 2 of 2", "a page past the end shows the last page")
	assert.Contains(t, body, "Thing 24")

	body = frag("?page=-3")
	assert.Contains(t, body, "Page 1 of 2")

	body = frag("?q=thing+0")
	assert.Contains(t, body, "10 items", "the total counts only what matches the search")

	body = frag("?q=thing&sort=name&dir=desc")
	assert.Contains(t, body, "25 items")
	assert.True(t, appearsBefore(body, "Thing 24", "Thing 05"), "newest name first when descending")
	assert.NotContains(t, body, "Thing 04", "page one holds the first 20 in that order")
	assert.Contains(t, body, "dir=desc&amp;page=2&amp;q=thing&amp;sort=name", "the next link keeps the search and the sort")
}

func TestInventoryGridCSV(t *testing.T) {
	f := newGridFixture(t)
	export := func(user, query string) [][]string {
		t.Helper()
		w := f.get(t, f.d.InventoryGridCSV(), user, "/admin/inventory/grid.csv"+query, nil)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/csv")
		assert.Regexp(t, `^attachment; filename="inventory-\d{4}-\d{2}-\d{2}\.csv"$`, w.Header().Get("Content-Disposition"))
		recs, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
		require.NoError(t, err)
		return recs
	}
	names := func(recs [][]string) []string {
		var out []string
		for _, r := range recs[1:] {
			out = append(out, r[0])
		}
		return out
	}

	t.Run("headers and rows", func(t *testing.T) {
		recs := export(f.viewer, "")
		assert.Equal(t, []string{"Name", "Code", "Location", "Description", "Holder", "Holder email", "Last verified", "Status", "Claim pending", "Accessories", "Serial"}, recs[0],
			"active custom fields follow the built-in columns, and the holder's email lets an export be imported back")
		assert.Equal(t, []string{"Camera", "Laptop", "Projector"}, names(recs))

		camera := recs[1]
		assert.Equal(t, "CAM-222", camera[1])
		assert.Equal(t, "Studio", camera[2])
		assert.Equal(t, "Active", camera[7])
		assert.Equal(t, "Yes", camera[8])
		assert.Equal(t, "SN-300", camera[10])

		projector := recs[3]
		assert.Equal(t, "Alice Anderson", projector[4])
		assert.Equal(t, "alice@example.com", projector[5])
		assert.Equal(t, time.Now().Format("2006-01-02"), projector[6], "last verified is a plain date")
		assert.Equal(t, "", projector[8])
	})

	t.Run("it follows the filters and the sort", func(t *testing.T) {
		assert.Equal(t, []string{"Camera", "Laptop", "Old tripod", "Projector"}, names(export(f.viewer, "?archived=1")))
		recs := export(f.viewer, "?archived=1")
		assert.Equal(t, "Archived", recs[3][7])
		assert.Equal(t, []string{"Camera"}, names(export(f.viewer, "?pending=1")))
		assert.Equal(t, []string{"Projector"}, names(export(f.viewer, "?q=alice")))
		assert.Equal(t, []string{"Projector", "Laptop", "Camera"}, names(export(f.viewer, "?sort=name&dir=desc")))
		assert.Equal(t, []string{"Projector", "Laptop", "Camera"}, names(export(f.viewer, "?sort=field:serial")))
	})

	t.Run("it is not limited to one page", func(t *testing.T) {
		for i := 0; i < 25; i++ {
			_, err := store.CreateInventoryItem(t.Context(), f.d.Pool, store.InventoryItemInput{Name: fmt.Sprintf("Extra %02d", i)}, fmt.Sprintf("EXT%03d", i+200))
			require.NoError(t, err)
		}
		assert.Len(t, names(export(f.manager, "")), 28)
	})

	t.Run("cells that look like formulas are defused", func(t *testing.T) {
		for _, name := range []string{"=SUM(A1)", "+1", "-2", "@cmd"} {
			_, err := store.CreateInventoryItem(t.Context(), f.d.Pool, store.InventoryItemInput{Name: name}, store.NewInventoryCode())
			require.NoError(t, err)
		}
		got := strings.Join(names(export(f.viewer, "?q=sum")), ",")
		assert.Equal(t, "'=SUM(A1)", got)
		all := names(export(f.viewer, ""))
		assert.Contains(t, all, "'+1")
		assert.Contains(t, all, "'-2")
		assert.Contains(t, all, "'@cmd")
	})
}

func TestInventoryGridCellEditing(t *testing.T) {
	f := newGridFixture(t)
	ctx := t.Context()
	cellPath := func(it *store.InventoryItem, col string) map[string]string {
		return map[string]string{"id": it.ID, "col": col}
	}
	edit := func(it *store.InventoryItem, col string) *httptest.ResponseRecorder {
		return f.get(t, f.d.InventoryGridCellEdit(), f.manager, "/x", cellPath(it, col))
	}
	save := func(it *store.InventoryItem, col, value string) *httptest.ResponseRecorder {
		return invDo(t, f.d.InventoryGridCellSave(), invReq{userID: f.manager, method: "PATCH", target: "/x", path: cellPath(it, col), form: url.Values{"value": {value}}})
	}
	reload := func(it *store.InventoryItem) *store.InventoryItem {
		got, err := store.GetInventoryItem(ctx, f.d.Pool, it.ID)
		require.NoError(t, err)
		return got
	}

	t.Run("the editor shows the current value and saves with PATCH", func(t *testing.T) {
		w := edit(f.camera, "name")
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, `value="Camera"`)
		assert.Contains(t, body, `hx-patch="/admin/fragments/inventory-grid/`+f.camera.ID+`/cell/name"`)
		assert.Contains(t, body, `id="cell-`+f.camera.ID+`-name"`, "same id as the cell it replaces")
		assert.Contains(t, body, "autofocus")
		assert.Contains(t, body, `aria-label="Name for Camera"`)
		assert.Contains(t, edit(f.camera, "field:serial").Body.String(), `value="SN-300"`)
	})

	t.Run("saving a name returns the display cell and keeps the claim marker", func(t *testing.T) {
		w := save(f.camera, "name", "  Camera body  ")
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "Camera body")
		assert.NotContains(t, body, "<form", "back to a plain cell")
		assert.Contains(t, body, "Claim pending")
		assert.Equal(t, "Camera body", reload(f.camera).Name)
	})

	t.Run("a location can be set and cleared", func(t *testing.T) {
		save(f.laptop, "location", "Desk")
		assert.Equal(t, "Desk", *reload(f.laptop).Location)
		w := save(f.laptop, "location", "")
		assert.Nil(t, reload(f.laptop).Location)
		assert.Contains(t, w.Body.String(), "—", "an empty cell shows a dash")
	})

	t.Run("custom fields are saved as text and an empty value removes them", func(t *testing.T) {
		w := save(f.laptop, "field:serial", "  SN-999 ")
		assert.Contains(t, w.Body.String(), "SN-999")
		assert.Equal(t, "SN-999", reload(f.laptop).Metadata["serial"])

		save(f.laptop, "field:serial", "")
		_, present := reload(f.laptop).Metadata["serial"]
		assert.False(t, present)
	})

	t.Run("a bad value comes back in the editor with a message and nothing is saved", func(t *testing.T) {
		w := save(f.projector, "name", "   ")
		body := w.Body.String()
		assert.Contains(t, body, "A name is required.")
		assert.Contains(t, body, "<form")
		assert.Equal(t, "Projector", reload(f.projector).Name)

		w = save(f.projector, "field:serial", strings.Repeat("x", 2001))
		assert.Contains(t, w.Body.String(), "too long")
		assert.Equal(t, "SN-100", reload(f.projector).Metadata["serial"])
	})

	t.Run("values are escaped", func(t *testing.T) {
		w := save(f.projector, "name", `<script>alert(1)</script>`)
		assert.NotContains(t, w.Body.String(), "<script>")
		assert.Contains(t, w.Body.String(), "&lt;script&gt;")
		save(f.projector, "name", "Projector")
	})

	t.Run("only the editable columns exist", func(t *testing.T) {
		for _, col := range []string{"code", "holder", "verified", "description", "field:color", "field:nope", "field:"} {
			assert.Equal(t, http.StatusNotFound, edit(f.camera, col).Code, "edit %s", col)
			assert.Equal(t, http.StatusNotFound, save(f.camera, col, "x").Code, "save %s", col)
		}
		assert.Equal(t, "CAM222", reload(f.camera).Code)
	})

	t.Run("unknown items are 404", func(t *testing.T) {
		missing := &store.InventoryItem{ID: "00000000-0000-0000-0000-000000000000"}
		assert.Equal(t, http.StatusNotFound, edit(missing, "name").Code)
		assert.Equal(t, http.StatusNotFound, save(missing, "name", "x").Code)
		notUUID := &store.InventoryItem{ID: "not-a-uuid"}
		assert.Equal(t, http.StatusNotFound, edit(notUUID, "name").Code)
	})

	t.Run("archived items can't be edited", func(t *testing.T) {
		body := edit(f.retired, "name").Body.String()
		assert.NotContains(t, body, "<form", "the editor is not offered")
		w := save(f.retired, "name", "Renamed")
		assert.Contains(t, w.Body.String(), "Archived items can&#39;t be edited")
		assert.Equal(t, "Old tripod", reload(f.retired).Name)
	})

	t.Run("cancel gives back the plain cell, editable only for managers", func(t *testing.T) {
		managerCell := f.get(t, f.d.InventoryGridCell(), f.manager, "/x", cellPath(f.camera, "name")).Body.String()
		assert.Contains(t, managerCell, "Click to edit")
		viewerCell := f.get(t, f.d.InventoryGridCell(), f.viewer, "/x", cellPath(f.camera, "name")).Body.String()
		assert.NotContains(t, viewerCell, "Click to edit")
		assert.Contains(t, viewerCell, f.camera.ID)
	})
}
