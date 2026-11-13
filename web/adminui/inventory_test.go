package adminui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
	"commons/web"
)

// invGrant creates a user holding exactly the given permission keys.
func invGrant(t *testing.T, pool *pgxpool.Pool, name string, keys ...string) string {
	t.Helper()
	ctx := t.Context()
	var userID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (display_name) VALUES ($1) RETURNING id`, name).Scan(&userID))

	role, err := store.CreateRole(ctx, pool, "role-"+name, "")
	require.NoError(t, err)
	for _, k := range keys {
		require.NoError(t, store.AddRolePermission(ctx, pool, role.ID, k))
	}
	group, err := store.CreateRoleGroup(ctx, pool, "group-"+name, "Group "+name, "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(ctx, pool, group.ID, role.ID))
	require.NoError(t, store.AssignGroupToUser(ctx, pool, userID, group.ID))
	return userID
}

func invUser(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(t.Context(),
		`INSERT INTO users (display_name) VALUES ($1) RETURNING id`, name).Scan(&id))
	return id
}

func invExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(t.Context(), sql, args...)
	require.NoError(t, err)
}

type invReq struct {
	userID     string
	method     string
	target     string
	form       url.Values
	path       map[string]string
	remoteAddr string // defaults to httptest's 192.0.2.1:1234
	userAgent  string
}

func invDo(t *testing.T, h http.Handler, rq invReq) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if rq.form != nil {
		body = strings.NewReader(rq.form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(rq.method, rq.target, body)
	if rq.form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if rq.remoteAddr != "" {
		req.RemoteAddr = rq.remoteAddr
	}
	if rq.userAgent != "" {
		req.Header.Set("User-Agent", rq.userAgent)
	}
	for k, v := range rq.path {
		req.SetPathValue(k, v)
	}
	req = req.WithContext(web.ContextWithUserID(req.Context(), rq.userID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestInventoryPermissionGates(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}

	item, err := store.CreateInventoryItemWithNewCode(t.Context(), pool, store.InventoryItemInput{Name: "Gate item"})
	require.NoError(t, err)
	def, err := store.CreateInventoryFieldDefinition(t.Context(), pool, "serial", "Serial")
	require.NoError(t, err)

	none := invGrant(t, pool, "none", permissions.LibraryView)
	viewer := invGrant(t, pool, "viewer", permissions.InventoryView)
	manager := invGrant(t, pool, "manager", permissions.InventoryManage)

	id := map[string]string{"id": item.ID}
	fid := map[string]string{"id": def.ID}
	read := []struct {
		name string
		h    http.Handler
		rq   invReq
	}{
		{"page", d.InventoryPage(), invReq{method: "GET", target: "/admin/inventory"}},
		{"table", d.InventoryTable(), invReq{method: "GET", target: "/admin/fragments/inventory-table"}},
		{"detail", d.InventoryDetailPage(), invReq{method: "GET", target: "/admin/inventory/x", path: id}},
		{"fields page", d.InventoryFieldsPage(), invReq{method: "GET", target: "/admin/inventory/fields"}},
	}
	write := []struct {
		name string
		h    http.Handler
		rq   invReq
	}{
		{"new modal", d.InventoryNewModal(), invReq{method: "GET", target: "/x"}},
		{"edit modal", d.InventoryEditModal(), invReq{method: "GET", target: "/x", path: id}},
		{"create", d.InventoryCreate(), invReq{method: "POST", target: "/x", form: url.Values{"name": {"n"}}}},
		{"update", d.InventoryUpdate(), invReq{method: "PUT", target: "/x", path: id, form: url.Values{"name": {"n"}}}},
		{"qr png", d.InventoryQRPNG(), invReq{method: "GET", target: "/x", path: id}},
		{"labels", d.InventoryLabels(), invReq{method: "GET", target: "/x?id=" + item.ID}},
		{"rotate code", d.InventoryRotateCode(), invReq{method: "POST", target: "/x", path: id}},
		{"archive", d.InventoryArchive(), invReq{method: "POST", target: "/x", path: id}},
		{"restore", d.InventoryRestore(), invReq{method: "POST", target: "/x", path: id}},
		{"accessory add", d.InventoryAccessoryAdd(), invReq{method: "POST", target: "/x", path: id, form: url.Values{"name": {"n"}}}},
		{"accessory delete", d.InventoryAccessoryDelete(), invReq{method: "DELETE", target: "/x", path: map[string]string{"id": item.ID, "accessoryID": item.ID}}},
		{"field create", d.InventoryFieldsCreate(), invReq{method: "POST", target: "/x", form: url.Values{"label": {"L"}}}},
		{"field update", d.InventoryFieldsUpdate(), invReq{method: "PUT", target: "/x", path: fid, form: url.Values{"label": {"L"}}}},
		{"field delete", d.InventoryFieldsDelete(), invReq{method: "DELETE", target: "/x", path: fid}},
		{"field move", d.InventoryFieldsMove(), invReq{method: "POST", target: "/x?dir=up", path: fid}},
	}

	for _, c := range read {
		t.Run("no permission cannot "+c.name, func(t *testing.T) {
			c.rq.userID = none
			assert.Equal(t, http.StatusForbidden, invDo(t, c.h, c.rq).Code)
		})
		t.Run("viewer can "+c.name, func(t *testing.T) {
			c.rq.userID = viewer
			assert.Equal(t, http.StatusOK, invDo(t, c.h, c.rq).Code)
		})
		t.Run("manager can "+c.name, func(t *testing.T) {
			c.rq.userID = manager
			assert.Equal(t, http.StatusOK, invDo(t, c.h, c.rq).Code, "manage implies view")
		})
	}
	for _, c := range write {
		t.Run("no permission cannot "+c.name, func(t *testing.T) {
			c.rq.userID = none
			assert.Equal(t, http.StatusForbidden, invDo(t, c.h, c.rq).Code)
		})
		t.Run("viewer cannot "+c.name, func(t *testing.T) {
			c.rq.userID = viewer
			assert.Equal(t, http.StatusForbidden, invDo(t, c.h, c.rq).Code)
		})
	}

	got, err := store.GetInventoryItem(t.Context(), pool, item.ID)
	require.NoError(t, err)
	assert.Equal(t, "Gate item", got.Name)
	assert.Nil(t, got.ArchivedAt, "rejected writes changed nothing")
}

func TestInventoryItemFormHandlers(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	ctx := t.Context()

	manager := invGrant(t, pool, "manager", permissions.InventoryManage)
	admin := invGrant(t, pool, "admin", permissions.InventoryManage, permissions.InventoryAdmin)

	t.Run("validation errors re-render the modal and create nothing", func(t *testing.T) {
		w := invDo(t, d.InventoryCreate(), invReq{userID: manager, method: "POST", target: "/x", form: url.Values{"name": {"  "}}})
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "Name is required.")

		w = invDo(t, d.InventoryCreate(), invReq{userID: manager, method: "POST", target: "/x",
			form: url.Values{"name": {"Thing"}, "verify_every_days": {"0"}}})
		assert.Contains(t, w.Body.String(), "Verify every must be a whole number")
		assert.Contains(t, w.Body.String(), `value="Thing"`, "entered values are kept")

		items, err := store.ListInventoryItems(ctx, pool, true)
		require.NoError(t, err)
		assert.Empty(t, items)
	})

	t.Run("manager creates an item and cannot set grace", func(t *testing.T) {
		w := invDo(t, d.InventoryCreate(), invReq{userID: manager, method: "POST", target: "/x", form: url.Values{
			"name": {"Projector"}, "location": {"Closet"}, "verify_every_days": {"14"}, "verify_grace_days": {"9"},
		}})
		require.Equal(t, http.StatusNoContent, w.Code)
		assert.Equal(t, "/admin/inventory", w.Header().Get("HX-Redirect"))

		items, err := store.ListInventoryItems(ctx, pool, false)
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "Projector", items[0].Name)
		assert.Equal(t, 14, *items[0].VerifyEveryDays)
		assert.Nil(t, items[0].VerifyGraceDays, "grace is ignored without inventory.admin")
		assert.Len(t, items[0].Code, 6)
	})

	t.Run("admin sets and clears grace; manager edit leaves it alone", func(t *testing.T) {
		items, _ := store.ListInventoryItems(ctx, pool, false)
		id := map[string]string{"id": items[0].ID}

		w := invDo(t, d.InventoryUpdate(), invReq{userID: admin, method: "PUT", target: "/x", path: id, form: url.Values{
			"name": {"Projector 2"}, "verify_every_days": {"7"}, "verify_grace_days": {"3"},
		}})
		require.Equal(t, http.StatusNoContent, w.Code)
		got, _ := store.GetInventoryItem(ctx, pool, items[0].ID)
		assert.Equal(t, "Projector 2", got.Name)
		assert.Equal(t, 3, *got.VerifyGraceDays)
		assert.Nil(t, got.Location, "blank location clears it")

		w = invDo(t, d.InventoryUpdate(), invReq{userID: manager, method: "PUT", target: "/x", path: id, form: url.Values{
			"name": {"Projector 3"}, "verify_every_days": {"7"}, "verify_grace_days": {"20"},
		}})
		require.Equal(t, http.StatusNoContent, w.Code)
		got, _ = store.GetInventoryItem(ctx, pool, items[0].ID)
		assert.Equal(t, "Projector 3", got.Name)
		assert.Equal(t, 3, *got.VerifyGraceDays, "manager cannot change grace")

		w = invDo(t, d.InventoryUpdate(), invReq{userID: admin, method: "PUT", target: "/x", path: id, form: url.Values{
			"name": {"Projector 3"}, "verify_every_days": {""}, "verify_grace_days": {""},
		}})
		require.Equal(t, http.StatusNoContent, w.Code)
		got, _ = store.GetInventoryItem(ctx, pool, items[0].ID)
		assert.Nil(t, got.VerifyGraceDays)
		assert.Nil(t, got.VerifyEveryDays, "blank cadence means no reminders")
	})

	t.Run("modal only shows the grace field to admins", func(t *testing.T) {
		items, _ := store.ListInventoryItems(ctx, pool, false)
		id := map[string]string{"id": items[0].ID}
		w := invDo(t, d.InventoryEditModal(), invReq{userID: manager, method: "GET", target: "/x", path: id})
		assert.NotContains(t, w.Body.String(), "verify_grace_days")
		w = invDo(t, d.InventoryEditModal(), invReq{userID: admin, method: "GET", target: "/x", path: id})
		assert.Contains(t, w.Body.String(), "verify_grace_days")
	})

	t.Run("archived items are listed only on request and cannot be edited", func(t *testing.T) {
		items, _ := store.ListInventoryItems(ctx, pool, false)
		id := map[string]string{"id": items[0].ID}

		w := invDo(t, d.InventoryArchive(), invReq{userID: manager, method: "POST", target: "/x", path: id})
		require.Equal(t, http.StatusOK, w.Code)

		w = invDo(t, d.InventoryPage(), invReq{userID: manager, method: "GET", target: "/admin/inventory"})
		assert.NotContains(t, w.Body.String(), "Projector 3")
		w = invDo(t, d.InventoryPage(), invReq{userID: manager, method: "GET", target: "/admin/inventory?archived=1"})
		assert.Contains(t, w.Body.String(), "Projector 3")
		assert.Contains(t, w.Body.String(), "Restore")

		w = invDo(t, d.InventoryUpdate(), invReq{userID: manager, method: "PUT", target: "/x", path: id,
			form: url.Values{"name": {"Renamed"}}})
		assert.Contains(t, w.Body.String(), "Archived items can")
		got, _ := store.GetInventoryItem(ctx, pool, items[0].ID)
		assert.Equal(t, "Projector 3", got.Name)

		w = invDo(t, d.InventoryRestore(), invReq{userID: manager, method: "POST", target: "/x", path: id})
		require.Equal(t, http.StatusOK, w.Code)
		w = invDo(t, d.InventoryTable(), invReq{userID: manager, method: "GET", target: "/x"})
		assert.Contains(t, w.Body.String(), "Projector 3")
	})

	t.Run("viewers do not see management buttons", func(t *testing.T) {
		viewer := invGrant(t, pool, "viewer", permissions.InventoryView)
		w := invDo(t, d.InventoryPage(), invReq{userID: viewer, method: "GET", target: "/admin/inventory"})
		body := w.Body.String()
		assert.Contains(t, body, "Projector 3")
		assert.NotContains(t, body, "Add item")
		assert.NotContains(t, body, ">Archive<")
		assert.Contains(t, body, "/admin/inventory/fields", "nav links to custom fields")
	})
}

func TestInventoryDetailPage(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	ctx := t.Context()

	viewer := invGrant(t, pool, "viewer", permissions.InventoryView)
	var holder string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ('Holly Holder') RETURNING id`).Scan(&holder))

	item, err := store.CreateInventoryItemWithNewCode(ctx, pool, store.InventoryItemInput{Name: "Laptop"})
	require.NoError(t, err)
	_, err = store.CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial Number")
	require.NoError(t, err)
	_, err = store.CreateInventoryFieldDefinition(ctx, pool, "color", "Color")
	require.NoError(t, err)
	require.NoError(t, store.SetInventoryItemMetadata(ctx, pool, item.ID, map[string]string{"serial": "SN-123"}))
	_, err = store.ApplyCustodyChange(ctx, pool, store.CustodyChange{ItemID: item.ID, UserID: holder, Source: "slack"})
	require.NoError(t, err)
	_, err = store.RecordPendingClaim(ctx, pool, item.ID, "Robert", "bob@elsewhere.example", nil, nil)
	require.NoError(t, err)

	w := invDo(t, d.InventoryDetailPage(), invReq{userID: viewer, method: "GET", target: "/x", path: map[string]string{"id": item.ID}})
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, "Laptop")
	assert.Contains(t, body, store.FormatInventoryCode(item.Code))
	assert.Contains(t, body, "Holly Holder")
	assert.Contains(t, body, "Serial Number")
	assert.Contains(t, body, "SN-123")
	assert.NotContains(t, body, ">Color<", "fields without a value are not listed")
	assert.Contains(t, body, "Taken (Slack)")
	assert.Contains(t, body, "Claim pending (no matching member)")
	assert.Contains(t, body, "Robert &lt;bob@elsewhere.example&gt;", "unmatched claimant shown by what they typed")
	assert.Contains(t, body, "5 days", "default grace from config")
	assert.Contains(t, body, "(default)")

	w = invDo(t, d.InventoryDetailPage(), invReq{userID: viewer, method: "GET", target: "/x",
		path: map[string]string{"id": "00000000-0000-0000-0000-000000000000"}})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestInventoryFieldHandlers(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, pool, "manager", permissions.InventoryManage)

	create := func(label string) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryFieldsCreate(), invReq{userID: manager, method: "POST", target: "/x", form: url.Values{"label": {label}}})
	}

	w := create("Serial number")
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, http.StatusNoContent, create("Purchase date").Code)

	w = create("serial  NUMBER")
	assert.Contains(t, w.Body.String(), "already exists")
	w = create("!!!")
	assert.Contains(t, w.Body.String(), "Use letters or numbers")

	defs, err := store.ListInventoryFieldDefinitions(ctx, pool, false)
	require.NoError(t, err)
	require.Len(t, defs, 2)
	assert.Equal(t, "serial_number", defs[0].Key)
	assert.Equal(t, "purchase_date", defs[1].Key)

	// Move the first field down, then up again.
	w = invDo(t, d.InventoryFieldsMove(), invReq{userID: manager, method: "POST", target: "/x?dir=down", path: map[string]string{"id": defs[0].ID}})
	require.Equal(t, http.StatusNoContent, w.Code)
	defs, _ = store.ListInventoryFieldDefinitions(ctx, pool, false)
	assert.Equal(t, "purchase_date", defs[0].Key)

	w = invDo(t, d.InventoryFieldsMove(), invReq{userID: manager, method: "POST", target: "/x?dir=up", path: map[string]string{"id": defs[0].ID}})
	require.Equal(t, http.StatusNoContent, w.Code, "moving the first field up is a no-op")
	defs, _ = store.ListInventoryFieldDefinitions(ctx, pool, false)
	assert.Equal(t, "purchase_date", defs[0].Key)

	// Rename and deactivate (an unchecked box is absent from the form).
	w = invDo(t, d.InventoryFieldsUpdate(), invReq{userID: manager, method: "PUT", target: "/x",
		path: map[string]string{"id": defs[0].ID}, form: url.Values{"label": {"Bought on"}}})
	require.Equal(t, http.StatusNoContent, w.Code)
	defs, _ = store.ListInventoryFieldDefinitions(ctx, pool, false)
	assert.Equal(t, "Bought on", defs[0].Label)
	assert.False(t, defs[0].Active)
	assert.Equal(t, "purchase_date", defs[0].Key, "the key never changes")

	w = invDo(t, d.InventoryFieldsDelete(), invReq{userID: manager, method: "DELETE", target: "/x", path: map[string]string{"id": defs[1].ID}})
	require.Equal(t, http.StatusOK, w.Code)
	defs, _ = store.ListInventoryFieldDefinitions(ctx, pool, false)
	assert.Len(t, defs, 1)

	w = invDo(t, d.InventoryFieldsPage(), invReq{userID: manager, method: "GET", target: "/admin/inventory/fields"})
	assert.Contains(t, w.Body.String(), "Bought on")
}
