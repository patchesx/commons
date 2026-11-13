package adminui

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

const testAdminURL = "https://commons.example.com"

func invSetAdminURL(t *testing.T, d Deps) {
	t.Helper()
	// The configured URL may carry a path; QR codes use only scheme and host.
	require.NoError(t, store.SetServiceConfig(t.Context(), d.Pool, "bot", "admin_url", testAdminURL+"/admin", false, nil, d.EncKey))
}

func TestInventoryClaimURL(t *testing.T) {
	assert.Equal(t, "https://x.example/inventory/K7M4QX", inventoryClaimURL("https://x.example", "K7M4QX"))
	assert.Equal(t, "https://x.example/inventory/K7M4QX", inventoryClaimURL("https://x.example/", "K7M4QX"))
}

func TestQRSVG(t *testing.T) {
	svg, err := qrSVG("https://x.example/inventory/K7M4QX")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(svg, "<svg"))
	assert.Contains(t, svg, `viewBox="0 0 `)
	assert.Contains(t, svg, "<path")
	again, _ := qrSVG("https://x.example/inventory/K7M4QX")
	assert.Equal(t, svg, again, "output is deterministic")
	other, _ := qrSVG("https://x.example/inventory/ZZZZZZ")
	assert.NotEqual(t, svg, other)
}

func TestInventoryQRPNG(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)

	item, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Projector"})
	require.NoError(t, err)
	get := func(id, query string) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryQRPNG(), invReq{userID: manager, method: "GET", target: "/x" + query, path: map[string]string{"id": id}})
	}

	t.Run("needs the admin URL configured", func(t *testing.T) {
		w := get(item.ID, "")
		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Contains(t, w.Body.String(), "admin URL")
	})

	invSetAdminURL(t, d)

	t.Run("serves a PNG that encodes the claim URL", func(t *testing.T) {
		w := get(item.ID, "?size=300")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
		assert.Contains(t, w.Header().Get("Content-Disposition"), item.Code)

		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		require.NoError(t, err)
		assert.Equal(t, 300, img.Bounds().Dx())
		assert.Equal(t, 300, img.Bounds().Dy())

		want, err := qrcode.Encode(testAdminURL+"/inventory/"+item.Code, qrcode.Medium, 300)
		require.NoError(t, err)
		assert.Equal(t, want, w.Body.Bytes(), "encodes <root URL>/inventory/<code>, with no path from the admin URL")
	})

	t.Run("size is clamped", func(t *testing.T) {
		w := get(item.ID, "?size=999999")
		require.Equal(t, http.StatusOK, w.Code)
		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		require.NoError(t, err)
		assert.Equal(t, maxQRPixels, img.Bounds().Dx())

		w = get(item.ID, "?size=abc")
		img, err = png.Decode(bytes.NewReader(w.Body.Bytes()))
		require.NoError(t, err)
		assert.Equal(t, defaultQRPixels, img.Bounds().Dx())
	})

	t.Run("unknown and archived items are 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, get("00000000-0000-0000-0000-000000000000", "").Code)
		require.NoError(t, store.ArchiveInventoryItem(ctx, d.Pool, item.ID))
		assert.Equal(t, http.StatusNotFound, get(item.ID, "").Code)
	})
}

func TestInventoryLabels(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)

	a, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Projector"})
	require.NoError(t, err)
	b, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: `<script>alert(1)</script>`})
	require.NoError(t, err)
	gone, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Retired"})
	require.NoError(t, err)
	require.NoError(t, store.ArchiveInventoryItem(ctx, d.Pool, gone.ID))

	get := func(q url.Values) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryLabels(), invReq{userID: manager, method: "GET", target: "/x?" + q.Encode()})
	}

	t.Run("needs the admin URL configured", func(t *testing.T) {
		w := get(url.Values{"id": {a.ID}})
		assert.Equal(t, http.StatusConflict, w.Code)
	})

	invSetAdminURL(t, d)

	t.Run("requires a selection", func(t *testing.T) {
		w := get(url.Values{})
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "Select at least one item")
	})

	t.Run("renders code above the QR above the name, QR encoding the claim URL", func(t *testing.T) {
		w := get(url.Values{"id": {a.ID, b.ID, a.ID}, "width": {"3"}})
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()

		assert.Equal(t, 2, strings.Count(body, `class="label"`), "duplicate ids make one label")
		assert.Contains(t, body, store.FormatInventoryCode(a.Code))
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "width:3.00in")

		svgA, err := qrSVG(testAdminURL + "/inventory/" + a.Code)
		require.NoError(t, err)
		assert.Contains(t, body, svgA)

		codeAt := strings.Index(body, store.FormatInventoryCode(a.Code))
		svgAt := strings.Index(body, svgA)
		nameAt := strings.Index(body[svgAt:], "Projector") + svgAt
		assert.Less(t, codeAt, svgAt, "code is above the QR code")
		assert.Less(t, svgAt, nameAt, "item name is below the QR code")

		assert.Contains(t, body, `value="`+a.ID+`"`, "the sheet keeps the selection so the width can be re-applied")
	})

	t.Run("item names are escaped", func(t *testing.T) {
		w := get(url.Values{"id": {b.ID}})
		body := w.Body.String()
		assert.NotContains(t, body, "<script>alert(1)</script>")
		assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	})

	t.Run("width defaults and is clamped", func(t *testing.T) {
		assert.Contains(t, get(url.Values{"id": {a.ID}}).Body.String(), "width:2.00in")
		assert.Contains(t, get(url.Values{"id": {a.ID}, "width": {"99"}}).Body.String(), "width:6.00in")
		assert.Contains(t, get(url.Values{"id": {a.ID}, "width": {"0.1"}}).Body.String(), "width:0.75in")
		assert.Contains(t, get(url.Values{"id": {a.ID}, "width": {"wide"}}).Body.String(), "width:2.00in")
	})

	t.Run("archived and unknown items are skipped", func(t *testing.T) {
		w := get(url.Values{"id": {a.ID, gone.ID, "not-a-uuid"}})
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 1, strings.Count(w.Body.String(), `class="label"`))
		assert.NotContains(t, w.Body.String(), "Retired")

		w = get(url.Values{"id": {gone.ID}})
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestInventoryRotateCodeHandler(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)
	holder := invGrant(t, d.Pool, "holder", permissions.LibraryView)

	item, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Laptop"})
	require.NoError(t, err)
	_, err = store.ApplyCustodyChange(ctx, d.Pool, store.CustodyChange{ItemID: item.ID, UserID: holder, Source: "slack"})
	require.NoError(t, err)

	rotate := func(id string) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryRotateCode(), invReq{userID: manager, method: "POST", target: "/x", path: map[string]string{"id": id}})
	}

	w := rotate(item.ID)
	require.Equal(t, http.StatusNoContent, w.Code)

	got, err := store.GetInventoryItem(ctx, d.Pool, item.ID)
	require.NoError(t, err)
	assert.NotEqual(t, item.Code, got.Code)
	assert.Regexp(t, `^[A-HJ-NP-Z2-9]{6}$`, got.Code)
	assert.Equal(t, holder, *got.CurrentHolderUserID, "rotating a code keeps the holder")

	_, err = store.GetInventoryItemByCode(ctx, d.Pool, item.Code)
	assert.ErrorIs(t, err, store.ErrNotFound, "the old label stops working")
	_, err = store.GetInventoryItemByCode(ctx, d.Pool, got.Code)
	assert.NoError(t, err)

	require.NoError(t, store.ArchiveInventoryItem(ctx, d.Pool, item.ID))
	w = rotate(item.ID)
	assert.Contains(t, w.Body.String(), "item not found")
	w = rotate("00000000-0000-0000-0000-000000000000")
	assert.Contains(t, w.Body.String(), "item not found")
}

func TestInventoryLabelControlsAreManagerOnly(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)
	viewer := invGrant(t, d.Pool, "viewer", permissions.InventoryView)

	active, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Active one"})
	require.NoError(t, err)
	archived, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Archived one"})
	require.NoError(t, err)
	require.NoError(t, store.ArchiveInventoryItem(ctx, d.Pool, archived.ID))

	page := func(user, target string) string {
		return invDo(t, d.InventoryPage(), invReq{userID: user, method: "GET", target: target}).Body.String()
	}

	body := page(manager, "/admin/inventory?archived=1")
	assert.Contains(t, body, `id="inv-labels-form"`)
	assert.Contains(t, body, `name="width"`)
	assert.Equal(t, 1, strings.Count(body, `name="id"`), "only active items get a checkbox")
	assert.Contains(t, body, `value="`+active.ID+`"`)
	assert.Contains(t, body, "QR labels need the admin URL", "without the admin URL the form says why")
	assert.Contains(t, body, `<button type="submit" disabled`, "and the print button is disabled")
	assert.Contains(t, body, "if (!document.querySelector(", "an empty selection is reported, not silently ignored")

	invSetAdminURL(t, d)
	body = page(manager, "/admin/inventory")
	assert.NotContains(t, body, `<button type="submit" disabled`)
	assert.NotContains(t, body, "QR labels need the admin URL")
	assert.Contains(t, body, "Tick at least one item below first.")

	body = page(viewer, "/admin/inventory")
	assert.NotContains(t, body, "inv-labels-form")
	assert.NotContains(t, body, `name="id"`)

	detail := func(user string) string {
		return invDo(t, d.InventoryDetailPage(), invReq{userID: user, method: "GET", target: "/x", path: map[string]string{"id": active.ID}}).Body.String()
	}
	body = detail(manager)
	assert.Contains(t, body, "Download QR (PNG)")
	assert.Contains(t, body, "/admin/inventory/"+active.ID+"/qr.png")
	assert.Contains(t, body, "Print label")
	assert.Contains(t, body, "Rotate code")
	// Bulma's .is-flex is display:flex !important, so the confirmation must start hidden by class,
	// not by inline display:none, and only become visible through the Alpine toggle.
	assert.Contains(t, body, `class="is-hidden is-align-items-center"`, "rotate confirmation starts hidden")
	assert.Contains(t, body, `:class="{ 'is-flex': confirming, 'is-hidden': !confirming }"`)
	assert.NotContains(t, body, `class="is-flex is-align-items-center"`)

	body = detail(viewer)
	assert.NotContains(t, body, "Download QR")
	assert.NotContains(t, body, "Rotate code")

	archivedDetail := invDo(t, d.InventoryDetailPage(), invReq{userID: manager, method: "GET", target: "/x", path: map[string]string{"id": archived.ID}}).Body.String()
	assert.NotContains(t, archivedDetail, "Rotate code", "archived items have no live label")
}
