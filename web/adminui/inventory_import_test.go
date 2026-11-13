package adminui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
	"commons/web"
)

type importFixture struct {
	d                     Deps
	none, viewer, manager string
	admin                 string
	alice                 string
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	pool := testhelpers.SetupTestDB(t)
	f := &importFixture{d: Deps{Pool: pool, EncKey: testhelpers.EncKey()}}
	f.none = invGrant(t, pool, "none", permissions.LibraryView)
	f.viewer = invGrant(t, pool, "viewer", permissions.InventoryView)
	f.manager = invGrant(t, pool, "manager", permissions.InventoryManage)
	f.admin = invGrant(t, pool, "admin", permissions.InventoryManage, permissions.InventoryAdmin)
	f.alice = invUser(t, pool, "Alice Anderson")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, f.alice)
	return f
}

func invUpload(t *testing.T, h http.Handler, user string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if content != nil {
		fw, err := mw.CreateFormFile("file", "inventory.csv")
		require.NoError(t, err)
		_, err = fw.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/admin/inventory/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(web.ContextWithUserID(req.Context(), user))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var importPayload = regexp.MustCompile(`name="csv_b64" value="([^"]+)"`)

// previewPayload pulls the base64 file the preview carries to the confirm step.
func previewPayload(t *testing.T, body string) string {
	t.Helper()
	m := importPayload.FindStringSubmatch(body)
	require.NotNil(t, m, "the preview should offer a confirm step")
	return m[1]
}

func (f *importFixture) confirm(t *testing.T, user, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return invDo(t, f.d.InventoryImportConfirm(), invReq{userID: user, method: "POST", target: "/x", form: url.Values{"csv_b64": {payload}}})
}

func (f *importFixture) importFile(t *testing.T, user, csv string) *httptest.ResponseRecorder {
	t.Helper()
	up := invUpload(t, f.d.InventoryImportUpload(), user, []byte(csv))
	require.Equal(t, http.StatusOK, up.Code)
	return f.confirm(t, user, previewPayload(t, up.Body.String()))
}

func TestInventoryImportPermissions(t *testing.T) {
	f := newImportFixture(t)
	data := []byte("Name\nA\n")
	for _, user := range []string{f.none, f.viewer} {
		assert.Equal(t, http.StatusForbidden, invDo(t, f.d.InventoryImportPage(), invReq{userID: user, method: "GET", target: "/x"}).Code)
		assert.Equal(t, http.StatusForbidden, invDo(t, f.d.InventoryImportTemplate(), invReq{userID: user, method: "GET", target: "/x"}).Code)
		assert.Equal(t, http.StatusForbidden, invUpload(t, f.d.InventoryImportUpload(), user, data).Code)
		assert.Equal(t, http.StatusForbidden, f.confirm(t, user, base64.StdEncoding.EncodeToString(data)).Code)
	}
	for _, user := range []string{f.manager, f.admin} {
		assert.Equal(t, http.StatusOK, invDo(t, f.d.InventoryImportPage(), invReq{userID: user, method: "GET", target: "/x"}).Code)
		assert.Equal(t, http.StatusOK, invDo(t, f.d.InventoryImportTemplate(), invReq{userID: user, method: "GET", target: "/x"}).Code)
	}
	items, _ := store.ListInventoryItems(t.Context(), f.d.Pool, true)
	assert.Empty(t, items, "the rejected attempts saved nothing")
}

func TestInventoryImportPageAndTemplate(t *testing.T) {
	f := newImportFixture(t)

	body := invDo(t, f.d.InventoryImportPage(), invReq{userID: f.manager, method: "GET", target: "/x"}).Body.String()
	assert.Contains(t, body, `enctype="multipart/form-data"`)
	assert.Contains(t, body, `name="file"`)
	assert.Contains(t, body, "/admin/inventory/import-template.csv")
	assert.Contains(t, body, "Name</strong> is required")
	assert.Contains(t, body, "never clears anything")
	assert.Contains(t, body, "ignored unless you are an inventory admin", "managers are told the holder column won't be used")

	body = invDo(t, f.d.InventoryImportPage(), invReq{userID: f.admin, method: "GET", target: "/x"}).Body.String()
	assert.Contains(t, body, "a member with that email becomes the holder")
	assert.Contains(t, body, "No one is messaged")

	w := invDo(t, f.d.InventoryImportTemplate(), invReq{userID: f.manager, method: "GET", target: "/x"})
	assert.Contains(t, w.Header().Get("Content-Type"), "text/csv")
	assert.Contains(t, w.Header().Get("Content-Disposition"), "inventory-import-template.csv")
	plan, err := store.PlanInventoryImport(t.Context(), f.d.Pool, w.Body.Bytes(), true)
	require.NoError(t, err)
	assert.Empty(t, plan.Errors, "the template is itself a valid file")
	assert.Equal(t, 1, plan.Creates())
}

func TestInventoryImportPreview(t *testing.T) {
	f := newImportFixture(t)
	upload := func(user, csv string) *httptest.ResponseRecorder {
		return invUpload(t, f.d.InventoryImportUpload(), user, []byte(csv))
	}
	itemCount := func() int {
		items, _ := store.ListInventoryItems(t.Context(), f.d.Pool, true)
		return len(items)
	}

	t.Run("a valid file previews what it will do, and saves nothing", func(t *testing.T) {
		csv := "Name,Code,Location,Holder email,Warranty\nProjector,,Closet,alice@example.com,2027-01-01\nCamera,CAM222,Studio,,\n"
		w := upload(f.admin, csv)
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "<strong>2</strong> to create")
		assert.Contains(t, body, "<strong>0</strong> to update")
		assert.Contains(t, body, "<strong>1</strong> holders to assign")
		assert.Contains(t, body, "<strong>1</strong> new custom fields")
		assert.Contains(t, body, "<strong>Warranty</strong>")
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "generated", "a blank code is shown as generated")
		assert.Contains(t, body, "CAM-222")
		assert.Contains(t, body, "Import 2 rows")
		assert.Equal(t, 0, itemCount())

		decoded, err := base64.StdEncoding.DecodeString(previewPayload(t, body))
		require.NoError(t, err)
		assert.Equal(t, csv, string(decoded), "the confirm step carries the exact file")
	})

	t.Run("without inventory.admin the holder column is ignored and says so", func(t *testing.T) {
		w := upload(f.manager, "Name,Holder email\nProjector,alice@example.com\n")
		body := w.Body.String()
		assert.Contains(t, body, "<strong>0</strong> holders to assign")
		assert.Contains(t, body, "Only inventory admins can assign holders")
	})

	t.Run("problems are listed by line and block the import", func(t *testing.T) {
		w := upload(f.admin, "Name,Code\nGood,\n,K7M4QX\nBad code,ABC\n")
		body := w.Body.String()
		assert.Contains(t, body, "Nothing was imported")
		assert.Contains(t, body, "Line 3:")
		assert.Contains(t, body, "Name is required")
		assert.Contains(t, body, "Line 4:")
		assert.Contains(t, body, "isn&#39;t valid")
		assert.NotContains(t, body, "csv_b64", "no confirm step while there are errors")
		assert.NotContains(t, body, "Import 3 rows")
		assert.Equal(t, 0, itemCount())
	})

	t.Run("file-level problems", func(t *testing.T) {
		body := upload(f.admin, "Code\nK7M4QX\n").Body.String()
		assert.Contains(t, body, "File:")
		assert.Contains(t, body, "no Name column")
		body = upload(f.admin, "").Body.String()
		assert.Contains(t, body, "file is empty")
	})

	t.Run("warnings don't block, and unmatched holders are called out", func(t *testing.T) {
		w := upload(f.admin, "Name,Holder email\nProjector,nobody@elsewhere.example\n")
		body := w.Body.String()
		assert.Contains(t, body, "Worth a look")
		assert.Contains(t, body, "No member has the email nobody@elsewhere.example")
		assert.Contains(t, body, "csv_b64", "it can still be imported")
	})

	t.Run("what the file says is escaped", func(t *testing.T) {
		w := upload(f.admin, "Name,Code\n<script>alert(1)</script>,ZZZ\n")
		body := w.Body.String()
		assert.NotContains(t, body, "<script>alert(1)</script>")
		assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	})

	t.Run("a header with no rows has nothing to import", func(t *testing.T) {
		body := upload(f.admin, "Name,Code\n").Body.String()
		assert.Contains(t, body, "no rows to import")
		assert.NotContains(t, body, "csv_b64")
	})

	t.Run("no file, or one that is not text", func(t *testing.T) {
		w := invUpload(t, f.d.InventoryImportUpload(), f.admin, nil)
		assert.Contains(t, w.Body.String(), "Choose a CSV file")
		w = invUpload(t, f.d.InventoryImportUpload(), f.admin, append([]byte("Name\n"), 0xff, 0xfe))
		assert.Contains(t, w.Body.String(), "UTF-8")
	})

	t.Run("a file over the size limit is refused", func(t *testing.T) {
		huge := append([]byte("Name\n"), bytes.Repeat([]byte("x"), maxImportUploadBytes)...)
		w := invUpload(t, f.d.InventoryImportUpload(), f.admin, huge)
		assert.Contains(t, w.Body.String(), "too large")
		assert.Equal(t, 0, itemCount())
	})

	t.Run("only a sample of a long file is shown", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("Name\n")
		for i := 0; i < 120; i++ {
			fmt.Fprintf(&b, "Item %03d\n", i)
		}
		body := upload(f.admin, b.String()).Body.String()
		assert.Contains(t, body, "Item 049")
		assert.NotContains(t, body, "Item 050")
		assert.Contains(t, body, "and 70 more rows")
		assert.Contains(t, body, "Import 120 rows")
	})
}

func TestInventoryImportConfirm(t *testing.T) {
	f := newImportFixture(t)
	ctx := t.Context()
	pool := f.d.Pool

	loc := "Closet"
	existing, err := store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "Projector", Location: &loc}, "K7M4QX")
	require.NoError(t, err)

	t.Run("it applies the file and reports what it did, with the new codes", func(t *testing.T) {
		w := f.importFile(t, f.admin, "Name,Code,Holder email,Serial\nProjector 4K,K7M-4QX,alice@example.com,SN-1\nCamera,,,SN-2\n")
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "Import complete")
		assert.Contains(t, body, "<strong>1</strong> created")
		assert.Contains(t, body, "<strong>1</strong> updated")
		assert.Contains(t, body, "<strong>1</strong> holders assigned")
		assert.Contains(t, body, "<strong>1</strong> custom fields created")
		assert.Contains(t, body, "Camera")

		got, _ := store.GetInventoryItem(ctx, pool, existing.ID)
		assert.Equal(t, "Projector 4K", got.Name)
		assert.Equal(t, "Closet", *got.Location, "a blank location is left alone")
		assert.Equal(t, "SN-1", got.Metadata["serial"])
		assert.Equal(t, f.alice, *got.CurrentHolderUserID)

		evs, _ := store.ListInventoryCustodyEvents(ctx, pool, existing.ID)
		assert.Equal(t, "admin", evs[0].Source)
		assert.Equal(t, "Imported from CSV", *evs[0].Note)

		items, _ := store.ListInventoryItems(ctx, pool, true)
		require.Len(t, items, 2)
		for _, it := range items {
			if it.Name == "Camera" {
				assert.Contains(t, body, store.FormatInventoryCode(it.Code), "the result lists the generated code")
				assert.Equal(t, "SN-2", it.Metadata["serial"])
			}
		}
	})

	t.Run("a manager's import ignores holders, even if the file names them", func(t *testing.T) {
		before, _ := store.ListInventoryCustodyEvents(ctx, pool, existing.ID)
		w := f.importFile(t, f.manager, "Name,Code,Holder email\nProjector 8K,K7M4QX,alice@example.com\n")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "<strong>0</strong> holders assigned")
		after, _ := store.ListInventoryCustodyEvents(ctx, pool, existing.ID)
		assert.Len(t, after, len(before))
		got, _ := store.GetInventoryItem(ctx, pool, existing.ID)
		assert.Equal(t, "Projector 8K", got.Name)
	})

	t.Run("the file is checked again at confirmation, so a tampered preview saves nothing", func(t *testing.T) {
		count := func() int { items, _ := store.ListInventoryItems(ctx, pool, true); return len(items) }
		before := count()
		bad := base64.StdEncoding.EncodeToString([]byte("Name,Code\nSneaky,\n,K7M4QX\n"))
		w := f.confirm(t, f.admin, bad)
		assert.Contains(t, w.Body.String(), "Nothing was imported")
		assert.Contains(t, w.Body.String(), "Name is required")
		assert.Equal(t, before, count())
	})

	t.Run("a missing or garbled preview payload asks for the file again", func(t *testing.T) {
		before, _ := store.ListInventoryItems(ctx, pool, true)
		for _, payload := range []string{"", "!!!not base64!!!"} {
			w := f.confirm(t, f.admin, payload)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), "Upload the file again")
		}
		after, _ := store.ListInventoryItems(ctx, pool, true)
		assert.Len(t, after, len(before))
	})

	t.Run("a confirmation whose file has no rows does nothing", func(t *testing.T) {
		w := f.confirm(t, f.admin, base64.StdEncoding.EncodeToString([]byte("Name\n")))
		assert.Contains(t, w.Body.String(), "no rows to import")
	})

	t.Run("a long file is imported in full, not just the sample", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("Name\n")
		for i := 0; i < 120; i++ {
			fmt.Fprintf(&b, "Bulk %03d\n", i)
		}
		w := f.importFile(t, f.admin, b.String())
		assert.Contains(t, w.Body.String(), "<strong>120</strong> created")
		items, _ := store.ListInventoryItems(ctx, pool, true)
		assert.GreaterOrEqual(t, len(items), 122)
	})
}

// An exported grid, imported straight back, must change nothing: that is what makes the CSV a safe way to
// edit many items at once.
func TestInventoryGridExportImportsBackUnchanged(t *testing.T) {
	f := newGridFixture(t)
	ctx := t.Context()
	admin := invGrant(t, f.d.Pool, "admin", permissions.InventoryManage, permissions.InventoryAdmin)

	// A name that looks like a formula, which the export defuses with an apostrophe.
	_, err := store.CreateInventoryItem(ctx, f.d.Pool, store.InventoryItemInput{Name: "=SUM(A1)"}, "FRM222")
	require.NoError(t, err)

	export := func() string {
		w := f.get(t, f.d.InventoryGridCSV(), f.viewer, "/admin/inventory/grid.csv", nil)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}
	before := export()
	require.Contains(t, before, "'=SUM(A1)", "the export defuses formulas")
	require.Contains(t, before, "alice@example.com", "and carries the holder's email")

	up := invUpload(t, f.d.InventoryImportUpload(), admin, []byte(before))
	require.Equal(t, http.StatusOK, up.Code)
	preview := up.Body.String()
	assert.NotContains(t, preview, "Nothing was imported", preview)
	assert.NotContains(t, preview, "Worth a look", "an export produces no warnings")
	assert.Contains(t, preview, "<strong>0</strong> to create")
	assert.Contains(t, preview, "<strong>4</strong> to update")
	assert.Contains(t, preview, "<strong>0</strong> holders to assign", "everyone already holds what they hold")
	assert.Contains(t, preview, "<strong>0</strong> new custom fields")

	res := f.confirmRaw(t, admin, previewPayload(t, preview))
	require.Equal(t, http.StatusOK, res.Code)
	assert.Contains(t, res.Body.String(), "<strong>4</strong> updated")

	assert.Equal(t, before, export(), "nothing changed, including the formula-looking name")
	got, err := store.GetInventoryItemByCode(ctx, f.d.Pool, "FRM222")
	require.NoError(t, err)
	assert.Equal(t, "=SUM(A1)", got.Name, "the apostrophe was removed on the way back in")
}

func (f *gridFixture) confirmRaw(t *testing.T, user, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return invDo(t, f.d.InventoryImportConfirm(), invReq{userID: user, method: "POST", target: "/x", form: url.Values{"csv_b64": {payload}}})
}

func TestInventoryImportEntryPoints(t *testing.T) {
	f := newImportFixture(t)
	items := invDo(t, f.d.InventoryPage(), invReq{userID: f.manager, method: "GET", target: "/admin/inventory"}).Body.String()
	assert.Contains(t, items, `href="/admin/inventory/import"`)
	grid := invDo(t, f.d.InventoryGridPage(), invReq{userID: f.manager, method: "GET", target: "/admin/inventory/grid"}).Body.String()
	assert.Contains(t, grid, `href="/admin/inventory/import"`)

	items = invDo(t, f.d.InventoryPage(), invReq{userID: f.viewer, method: "GET", target: "/admin/inventory"}).Body.String()
	assert.NotContains(t, items, "/admin/inventory/import", "viewers can't import")
	grid = invDo(t, f.d.InventoryGridPage(), invReq{userID: f.viewer, method: "GET", target: "/admin/inventory/grid"}).Body.String()
	assert.NotContains(t, grid, "/admin/inventory/import")
}

var importTokenField = regexp.MustCompile(`name="token" value="([^"]+)"`)

func previewToken(t *testing.T, body string) string {
	t.Helper()
	m := importTokenField.FindStringSubmatch(body)
	require.NotNil(t, m, "the preview should carry a one-time token")
	return m[1]
}

func (f *importFixture) confirmToken(t *testing.T, user, payload, token string) *httptest.ResponseRecorder {
	t.Helper()
	return invDo(t, f.d.InventoryImportConfirm(), invReq{userID: user, method: "POST", target: "/x", form: url.Values{"csv_b64": {payload}, "token": {token}}})
}

func (f *importFixture) resultPage(t *testing.T, user, token string) *httptest.ResponseRecorder {
	t.Helper()
	return invDo(t, f.d.InventoryImportResult(), invReq{userID: user, method: "GET", target: "/admin/inventory/import/result?t=" + url.QueryEscape(token)})
}

func TestInventoryImportCanOnlyBeConfirmedOnce(t *testing.T) {
	f := newImportFixture(t)
	f.d.Imports = NewImportState()
	ctx := t.Context()
	count := func() int { items, _ := store.ListInventoryItems(ctx, f.d.Pool, true); return len(items) }

	const file = "Name\nProjector\nCamera\n"
	up := invUpload(t, f.d.InventoryImportUpload(), f.admin, []byte(file))
	require.Equal(t, http.StatusOK, up.Code)
	payload, token := previewPayload(t, up.Body.String()), previewToken(t, up.Body.String())

	t.Run("a confirmation without a token is refused", func(t *testing.T) {
		w := f.confirm(t, f.admin, payload)
		assert.Contains(t, w.Body.String(), "Upload the file again")
		assert.Equal(t, 0, count())
	})

	t.Run("the first confirmation imports and redirects to a result page", func(t *testing.T) {
		w := f.confirmToken(t, f.admin, payload, token)
		require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
		assert.Equal(t, "/admin/inventory/import/result?t="+token, w.Header().Get("Location"))
		assert.Equal(t, 2, count())
	})

	t.Run("the result page can be reloaded", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			w := f.resultPage(t, f.admin, token)
			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			assert.Contains(t, body, "Import complete")
			assert.Contains(t, body, "<strong>2</strong> created")
			assert.Contains(t, body, "Projector", "with the generated codes")
		}
		assert.Equal(t, 2, count(), "reloading imported nothing more")
	})

	t.Run("confirming the same preview again imports nothing", func(t *testing.T) {
		w := f.confirmToken(t, f.admin, payload, token)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "That import already ran")
		assert.Equal(t, 2, count())
	})

	t.Run("each preview has its own token, so importing a file again on purpose works", func(t *testing.T) {
		again := invUpload(t, f.d.InventoryImportUpload(), f.admin, []byte(file))
		newToken := previewToken(t, again.Body.String())
		assert.NotEqual(t, token, newToken)
		w := f.confirmToken(t, f.admin, previewPayload(t, again.Body.String()), newToken)
		assert.Equal(t, http.StatusSeeOther, w.Code)
		assert.Equal(t, 4, count())
	})

	t.Run("result pages need permission, and unknown or missing ones say so", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, f.resultPage(t, f.viewer, token).Code)
		assert.Equal(t, http.StatusForbidden, f.resultPage(t, f.none, token).Code)
		assert.Contains(t, f.resultPage(t, f.admin, "nope").Body.String(), "no longer available")
		assert.Contains(t, f.resultPage(t, f.admin, "").Body.String(), "no longer available")
	})
}

func TestInventoryImportTokenIsSpentOnlyByAnImport(t *testing.T) {
	f := newImportFixture(t)
	f.d.Imports = NewImportState()
	ctx := t.Context()
	count := func() int { items, _ := store.ListInventoryItems(ctx, f.d.Pool, true); return len(items) }

	up := invUpload(t, f.d.InventoryImportUpload(), f.admin, []byte("Name\nProjector\n"))
	payload, token := previewPayload(t, up.Body.String()), previewToken(t, up.Body.String())

	// A confirmation whose file turns out to be invalid saves nothing and leaves the token usable.
	bad := base64.StdEncoding.EncodeToString([]byte("Name,Code\n,K7M4QX\n"))
	w := f.confirmToken(t, f.admin, bad, token)
	assert.Contains(t, w.Body.String(), "Nothing was imported")
	assert.Equal(t, 0, count())

	w = f.confirmToken(t, f.admin, payload, token)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, 1, count())
}

func TestInventoryImportDoubleSubmitRace(t *testing.T) {
	f := newImportFixture(t)
	f.d.Imports = NewImportState()
	ctx := t.Context()

	up := invUpload(t, f.d.InventoryImportUpload(), f.admin, []byte("Name\nA\nB\nC\n"))
	payload, token := previewPayload(t, up.Body.String()), previewToken(t, up.Body.String())

	const submits = 8
	codes := make(chan int, submits)
	for i := 0; i < submits; i++ {
		go func() {
			w := invDo(t, f.d.InventoryImportConfirm(), invReq{userID: f.admin, method: "POST", target: "/x",
				form: url.Values{"csv_b64": {payload}, "token": {token}}})
			codes <- w.Code
		}()
	}
	redirects := 0
	for i := 0; i < submits; i++ {
		if <-codes == http.StatusSeeOther {
			redirects++
		}
	}
	assert.Equal(t, 1, redirects, "exactly one of the simultaneous submissions imports")
	items, _ := store.ListInventoryItems(ctx, f.d.Pool, true)
	assert.Len(t, items, 3, "the file's three items exist once")
}

func TestImportState(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewImportState()
	s.now = func() time.Time { return now }

	assert.True(t, s.Use("a"), "the first use of a token")
	assert.False(t, s.Use("a"), "and never again")
	assert.True(t, s.Use("b"), "other tokens are independent")

	s.StoreResult("a", store.InventoryImportResult{Created: 3})
	got, ok := s.Result("a")
	require.True(t, ok)
	assert.Equal(t, 3, got.Created)
	_, ok = s.Result("missing")
	assert.False(t, ok)

	now = now.Add(importResultTTL + time.Minute)
	_, ok = s.Result("a")
	assert.False(t, ok, "results expire")
	assert.False(t, s.Use("a"), "but a spent token stays spent for much longer")

	now = now.Add(importTokenTTL)
	assert.True(t, s.Use("a"), "until it is forgotten")
	assert.Empty(t, s.results)
}
