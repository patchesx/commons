package adminui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"math"
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
	admintempl "commons/web/templ"
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

		assert.Equal(t, 2, strings.Count(body, `class="cell label"`), "duplicate ids make one label")
		assert.Contains(t, body, store.FormatInventoryCode(a.Code))
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "width:3.000in;height:4.350in", "label boxes are sized exactly")

		metrics := layoutLabelSheet(url.Values{"width": {"3"}}, nil)
		pixels := int(math.Ceil(metrics.QRSide() * labelQRDPI))
		raw, err := qrcode.Encode(testAdminURL+"/inventory/"+a.Code, qrcode.Medium, pixels)
		require.NoError(t, err)
		pngA := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
		assert.Contains(t, body, `src="`+pngA+`"`, "the image encodes the claim URL and is sized for the label")
		assert.NotContains(t, body, "<svg", "no inline vector markup, which Safari can print as blank")

		codeAt := strings.Index(body, store.FormatInventoryCode(a.Code))
		pngAt := strings.Index(body, pngA)
		nameAt := strings.Index(body[pngAt:], "Projector") + pngAt
		assert.Less(t, codeAt, pngAt, "code is above the QR code")
		assert.Less(t, pngAt, nameAt, "item name is below the QR code")

		assert.Contains(t, body, `value="`+a.ID+`"`, "the sheet keeps the selection so the width can be re-applied")
	})

	t.Run("item names are escaped", func(t *testing.T) {
		w := get(url.Values{"id": {b.ID}})
		body := w.Body.String()
		assert.NotContains(t, body, "<script>alert(1)</script>")
		assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	})

	t.Run("width defaults and is clamped", func(t *testing.T) {
		assert.Contains(t, get(url.Values{"id": {a.ID}}).Body.String(), "width:2.000in;height:2.900in")
		assert.Contains(t, get(url.Values{"id": {a.ID}, "width": {"99"}}).Body.String(), "width:6.000in")
		assert.Contains(t, get(url.Values{"id": {a.ID}, "width": {"0.1"}}).Body.String(), "width:0.750in")
		assert.Contains(t, get(url.Values{"id": {a.ID}, "width": {"wide"}}).Body.String(), "width:2.000in;height:2.900in")
	})

	t.Run("archived and unknown items are skipped", func(t *testing.T) {
		w := get(url.Values{"id": {a.ID, gone.ID, "not-a-uuid"}})
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 1, strings.Count(w.Body.String(), `class="cell label"`))
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
	assert.NotContains(t, body, `target="_blank"`, "the settings page opens in this tab, so the PDF is the only extra one")

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
	assert.NotContains(t, body, `target="_blank"`, "Print label opens the settings page in this tab")
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

func TestLayoutLabelSheet(t *testing.T) {
	mk := func(n int) []admintempl.InventoryLabel {
		out := make([]admintempl.InventoryLabel, n)
		for i := range out {
			out[i] = admintempl.InventoryLabel{Code: fmt.Sprintf("C%d", i), Name: "n"}
		}
		return out
	}
	layout := func(n int, kv ...string) admintempl.InventoryLabelSheet {
		q := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		return layoutLabelSheet(q, mk(n))
	}
	count := func(page []*admintempl.InventoryLabel) (labels, skipped int) {
		for _, l := range page {
			if l == nil {
				skipped++
			} else {
				labels++
			}
		}
		return
	}

	t.Run("the default is a 2in label, 3 across, on Letter", func(t *testing.T) {
		s := layout(1)
		assert.Equal(t, 3, s.Cols, "7.5in of width holds three 2in labels with 0.125in gaps")
		assert.InDelta(t, 2.9, s.Height, 0.001, "height follows the width")
		assert.True(t, s.HeightAuto)
		assert.Equal(t, 3, s.Rows)
		assert.Equal(t, 9, s.PerPage())
	})

	t.Run("a grid never overruns the page", func(t *testing.T) {
		for _, w := range []float64{0.75, 1, 1.5, 2, 2.5, 3, 4, 6} {
			for _, g := range []float64{0, 0.125, 0.5} {
				for _, m := range []float64{0.25, 0.5, 1} {
					s := layout(1, "width", fmt.Sprint(w), "gap", fmt.Sprint(g), "margin", fmt.Sprint(m))
					usedW := 2*s.Margin + float64(s.Cols)*s.Width + float64(s.Cols-1)*s.Gap
					usedH := 2*s.Margin + float64(s.Rows)*s.Height + float64(s.Rows-1)*s.Gap
					assert.LessOrEqual(t, usedW, 8.5+1e-9, "width %v gap %v margin %v", w, g, m)
					assert.LessOrEqual(t, usedH, 10.95+1e-9, "width %v gap %v margin %v", w, g, m)
					assert.GreaterOrEqual(t, s.PerPage(), 1)
				}
			}
		}
	})

	t.Run("an exact fit keeps its last column and row", func(t *testing.T) {
		s := layout(1, "width", "2", "height", "2", "gap", "0.5", "margin", "0.5")
		assert.Equal(t, 3, s.Cols, "3 x 2in + 2 x 0.5in gap = 7in, inside 7.5in")
		s = layout(1, "width", "2.5", "height", "2", "gap", "0", "margin", "0.5")
		assert.Equal(t, 3, s.Cols, "3 x 2.5in = 7.5in exactly")
		assert.Equal(t, 4, s.Rows, "5 x 2in = 10in is over the 9.95in of usable height, so 4 rows")
	})

	t.Run("labels split into sheets without splitting one", func(t *testing.T) {
		s := layout(20)
		require.Len(t, s.Pages, 3)
		l0, _ := count(s.Pages[0])
		l1, _ := count(s.Pages[1])
		l2, _ := count(s.Pages[2])
		assert.Equal(t, []int{9, 9, 2}, []int{l0, l1, l2})
		assert.Equal(t, 20, s.Total)
	})

	t.Run("start skips positions on the first sheet only", func(t *testing.T) {
		s := layout(10, "start", "4")
		assert.Equal(t, 3, s.Skip)
		l0, skipped0 := count(s.Pages[0])
		assert.Equal(t, 6, l0)
		assert.Equal(t, 3, skipped0)
		require.Len(t, s.Pages, 2)
		l1, skipped1 := count(s.Pages[1])
		assert.Equal(t, 4, l1)
		assert.Equal(t, 0, skipped1)
		assert.Nil(t, s.Pages[0][0])
		assert.NotNil(t, s.Pages[0][3])

		assert.Equal(t, 0, layout(1, "start", "-5").Skip)
		assert.Equal(t, 0, layout(1, "start", "x").Skip)
		assert.Equal(t, 8, layout(1, "start", "999").Skip, "past the last position means the last one")
	})

	t.Run("settings out of range are pulled in", func(t *testing.T) {
		s := layout(1, "width", "99", "margin", "9", "gap", "9", "height", "99")
		assert.InDelta(t, 1.0, s.Margin, 1e-9)
		assert.InDelta(t, 0.5, s.Gap, 1e-9)
		assert.LessOrEqual(t, s.Width, 6.5)
		assert.LessOrEqual(t, s.Height, 10.95-2)
		s = layout(1, "width", "0.1", "height", "0.1", "gap", "-1", "margin", "0")
		assert.InDelta(t, 0.75, s.Width, 1e-9)
		assert.InDelta(t, 0.75, s.Height, 1e-9)
		assert.InDelta(t, 0.25, s.Margin, 1e-9)
		assert.Equal(t, 0.0, s.Gap)
		assert.True(t, layout(1, "height", "wide").HeightAuto, "an unreadable height means auto")
	})
}

func TestInventoryLabelSheetPage(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)
	invSetAdminURL(t, d)
	var ids []string
	for i := range 11 {
		it, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: fmt.Sprintf("Item %02d", i)})
		require.NoError(t, err)
		ids = append(ids, it.ID)
	}
	get := func(kv ...string) string {
		q := url.Values{"id": ids}
		for i := 0; i < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		w := invDo(t, d.InventoryLabels(), invReq{userID: manager, method: "GET", target: "/x?" + q.Encode()})
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	t.Run("it prints on Letter, in pages of whole labels", func(t *testing.T) {
		body := get()
		assert.Contains(t, body, "size: 8.5in 11in; margin: 0")
		assert.Equal(t, 2, strings.Count(body, `<div class="page"`), "11 labels at 9 a sheet is two sheets")
		assert.Equal(t, 11, strings.Count(body, `class="cell label"`))
		assert.Contains(t, body, "3 across × 3 down = 9 labels per sheet")
		assert.Contains(t, body, "11 to print on 2 sheet(s)")
		assert.Contains(t, body, "break-after: page")
		assert.Contains(t, body, ".page:last-child { page-break-after: auto; break-after: auto; }", "no blank trailing page")
	})

	t.Run("a partly used sheet skips positions", func(t *testing.T) {
		body := get("start", "8")
		assert.Equal(t, 7, strings.Count(body, `class="cell skipped"`))
		assert.Equal(t, 11, strings.Count(body, `class="cell label"`), "every label is still printed")
		assert.Equal(t, 2, strings.Count(body, `<div class="page"`), "7 skipped + 11 labels = 18 positions, exactly two sheets")
	})

	t.Run("the settings come back in the form so they can be re-applied", func(t *testing.T) {
		body := get("width", "1.5", "height", "2", "gap", "0.25", "margin", "0.375", "start", "3")
		assert.Contains(t, body, `name="width"`)
		assert.Contains(t, body, `value="1.5"`)
		assert.Contains(t, body, `name="height"`)
		assert.Contains(t, body, `value="2"`)
		assert.Contains(t, body, `value="0.25"`)
		assert.Contains(t, body, `value="0.375"`)
		assert.Contains(t, body, `name="start" min="1" max="`)
		assert.Contains(t, body, `value="3"`)
		auto := get("width", "1.5")
		assert.Regexp(t, `name="height"[^>]*value=""`, auto, "an auto height stays blank so it follows the width")
	})

	t.Run("controls are hidden when printing", func(t *testing.T) {
		assert.Contains(t, get(), ".controls { display: none; }")
	})
}

func TestInventoryLabelsPDF(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	ctx := t.Context()
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)
	viewer := invGrant(t, d.Pool, "viewer", permissions.InventoryView)
	var ids []string
	for i := range 22 {
		it, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: fmt.Sprintf("Item %02d", i)})
		require.NoError(t, err)
		ids = append(ids, it.ID)
	}
	gone, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Retired"})
	require.NoError(t, err)
	require.NoError(t, store.ArchiveInventoryItem(ctx, d.Pool, gone.ID))

	get := func(user string, q url.Values) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryLabelsPDF(), invReq{userID: user, method: "GET", target: "/x?" + q.Encode()})
	}
	pages := func(w *httptest.ResponseRecorder) int { return bytes.Count(w.Body.Bytes(), []byte("/Type /Page\n")) }
	images := func(w *httptest.ResponseRecorder) int { return bytes.Count(w.Body.Bytes(), []byte("/Subtype /Image")) }

	t.Run("it needs the admin URL, a selection, and the manage permission", func(t *testing.T) {
		assert.Equal(t, http.StatusConflict, get(manager, url.Values{"id": ids[:1]}).Code)
		invSetAdminURL(t, d)
		assert.Equal(t, http.StatusBadRequest, get(manager, url.Values{}).Code)
		assert.Equal(t, http.StatusForbidden, get(viewer, url.Values{"id": ids[:1]}).Code)
		assert.Equal(t, http.StatusNotFound, get(manager, url.Values{"id": {gone.ID}}).Code)
	})

	t.Run("it is a Letter PDF with one page per sheet and one image per label", func(t *testing.T) {
		w := get(manager, url.Values{"id": ids})
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
		assert.Contains(t, w.Header().Get("Content-Disposition"), "inline")
		assert.True(t, bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")))
		assert.Contains(t, w.Body.String(), "/MediaBox [0 0 612.00 792.00]", "US Letter")
		assert.Equal(t, 3, pages(w), "22 labels at 9 a sheet")
		assert.Equal(t, 22, images(w))
	})

	t.Run("it follows the same settings as the page", func(t *testing.T) {
		assert.Equal(t, 1, pages(get(manager, url.Values{"id": ids[:5]})))
		assert.Equal(t, 2, pages(get(manager, url.Values{"id": ids[:5], "start": {"8"}})), "start skips positions, so five labels spill onto a second sheet")
		small := get(manager, url.Values{"id": ids, "width": {"1.5"}})
		assert.Equal(t, 2, pages(small), "22 at 16 a sheet")
		assert.Equal(t, 1, pages(get(manager, url.Values{"id": ids, "width": {"1"}, "height": {"1"}, "gap": {"0"}})), "22 labels fit on one sheet when they are 1in")
	})

	t.Run("archived and unknown items are skipped, duplicates collapse", func(t *testing.T) {
		w := get(manager, url.Values{"id": {ids[0], ids[0], gone.ID, "not-a-uuid"}})
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 1, images(w))
	})

	t.Run("names outside the PDF fonts do not break it", func(t *testing.T) {
		odd, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, store.InventoryItemInput{Name: "Café 横幅 🎤 " + strings.Repeat("long ", 40)})
		require.NoError(t, err)
		w := get(manager, url.Values{"id": {odd.ID}})
		require.Equal(t, http.StatusOK, w.Code)
		assert.True(t, bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")))
	})
}

func TestLabelPageDownloadsAPDF(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	manager := invGrant(t, d.Pool, "manager", permissions.InventoryManage)
	invSetAdminURL(t, d)
	it, err := store.CreateInventoryItemWithNewCode(t.Context(), d.Pool, store.InventoryItemInput{Name: "Projector"})
	require.NoError(t, err)
	body := invDo(t, d.InventoryLabels(), invReq{userID: manager, method: "GET", target: "/x?id=" + it.ID}).Body.String()
	assert.Contains(t, body, `formaction="/admin/inventory/labels.pdf"`, "the button submits the form's current settings")
	assert.Contains(t, body, `formtarget="_blank"`)
	assert.Contains(t, body, "font-family: Helvetica, Arial, sans-serif", "the preview uses the PDF's fonts so the two look alike")
	assert.Contains(t, body, `font-family: Courier, "Courier New", monospace`)
	assert.Contains(t, body, "Download / print PDF")
	assert.NotContains(t, body, "window.print", "no browser print button")
}
