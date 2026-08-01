//go:build ignore

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
	"commons/web"
)

// roundTripFunc is an http.RoundTripper that delegates to a function, used to
// intercept external HTTP calls (LibraryThing, Open Library) in tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListLibraryBooksEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListLibraryBooks(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/library/books", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, float64(0), out["total"])
	assert.Empty(t, out["books"])
}

func TestListLibraryBooksSearch(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-admin@test.com", "hash", false, nil)
	require.NoError(t, err)

	_, err = store.CreateBook(ctx, pool, store.Book{Title: "The Dispossessed", Author: "Ursula K. Le Guin"}, admin.ID)
	require.NoError(t, err)
	_, err = store.CreateBook(ctx, pool, store.Book{Title: "Neuromancer", Author: "William Gibson"}, admin.ID)
	require.NoError(t, err)

	h := ListLibraryBooks(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/library/books?search=dispossessed", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, float64(1), out["total"])
	books := out["books"].([]any)
	require.Len(t, books, 1)
	assert.Equal(t, "The Dispossessed", books[0].(map[string]any)["title"])
}

func TestCreateLibraryBookHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-create@test.com", "hash", false, nil)
	require.NoError(t, err)

	h := CreateLibraryBook(pool, testhelpers.EncKey())

	body := `{"title":"Parable of the Sower","author":"Octavia Butler","copies":2}`
	req := httptest.NewRequest(http.MethodPost, "/api/library/books", strings.NewReader(body))
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	bookID, ok := out["id"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, bookID)

	// Verify 2 copies were created.
	copies, err := store.ListCopiesForBook(ctx, pool, bookID)
	require.NoError(t, err)
	assert.Len(t, copies, 2)
}

func TestCreateLibraryBookMissingTitleAndAuthor(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateLibraryBook(pool, testhelpers.EncKey())
	// No isbn, no title, no author → immediate 400.
	req := httptest.NewRequest(http.MethodPost, "/api/library/books", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestApproveLibraryCheckoutHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-approve@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := store.CreateBook(ctx, pool, store.Book{Title: "Kindred", Author: "Octavia Butler"}, admin.ID)
	require.NoError(t, err)
	_, err = store.CreateCopy(ctx, pool, store.Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_API_LIB_APP", "appuser", "App User")
	require.NoError(t, err)

	co, err := store.RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	h := ApproveLibraryCheckout(pool)
	body := `{"dueDate":"2026-05-01"}`
	req := httptest.NewRequest(http.MethodPut, "/api/library/checkouts/"+co.ID+"/approve", strings.NewReader(body))
	req.SetPathValue("id", co.ID)
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	checkouts, _, err := store.ListCheckouts(ctx, pool, "active", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	assert.Equal(t, co.ID, checkouts[0].ID)
	assert.Equal(t, "2026-05-01", checkouts[0].DueDate.Format("2006-01-02"))
}

func TestReturnLibraryCheckoutHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-return@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := store.CreateBook(ctx, pool, store.Book{Title: "Left Hand of Darkness", Author: "Le Guin"}, admin.ID)
	require.NoError(t, err)
	_, err = store.CreateCopy(ctx, pool, store.Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_API_LIB_RET", "retuser", "Return User")
	require.NoError(t, err)

	co, err := store.RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	require.NoError(t, store.ApproveCheckout(ctx, pool, co.ID, admin.ID, time.Time{}))

	h := ReturnLibraryCheckout(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/library/checkouts/"+co.ID+"/return", nil)
	req.SetPathValue("id", co.ID)
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	checkouts, _, err := store.ListCheckouts(ctx, pool, "returned", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	assert.Equal(t, co.ID, checkouts[0].ID)
}

func TestCSVImportHappyPath(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-csv@test.com", "hash", false, nil)
	require.NoError(t, err)

	csvData := "Title,Primary Author,ISBN,Book Id,Comments\n" +
		"\"The Dispossessed\",\"Ursula K. Le Guin\",9780061054884,12345,A classic\n" +
		"\"Neuromancer\",\"William Gibson\",9780441569595,67890,\n"

	body, contentType := buildMultipartCSV(t, csvData)

	h := ImportLibraryBooks(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/library/books/import", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, float64(2), out["imported"])
	assert.Equal(t, float64(0), out["skipped"])

	_, total, err := store.ListBooks(ctx, pool, "", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
}

func TestCSVImportDuplicateSkipped(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-csvdup@test.com", "hash", false, nil)
	require.NoError(t, err)

	isbn := "9780061054884"
	_, err = store.CreateBook(ctx, pool, store.Book{Title: "The Dispossessed", Author: "Le Guin", ISBN: &isbn}, admin.ID)
	require.NoError(t, err)

	// CSV contains the same ISBN — should be skipped due to unique constraint.
	csvData := "Title,Primary Author,ISBN,Book Id,Comments\n" +
		"\"The Dispossessed\",\"Ursula K. Le Guin\",9780061054884,12345,\n"

	body, contentType := buildMultipartCSV(t, csvData)

	h := ImportLibraryBooks(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/library/books/import", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, float64(0), out["imported"])
	assert.Equal(t, float64(1), out["skipped"])
}

// buildMultipartCSV constructs a multipart/form-data body with the CSV content.
func buildMultipartCSV(t *testing.T, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "export.csv")
	require.NoError(t, err)
	_, err = fw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

func TestGetLibraryBookHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-get@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := store.CreateBook(ctx, pool, store.Book{Title: "Solaris", Author: "Stanisław Lem"}, admin.ID)
	require.NoError(t, err)
	_, err = store.CreateCopy(ctx, pool, store.Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	h := GetLibraryBook(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/library/books/"+book.ID, nil)
	req.SetPathValue("id", book.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Solaris", out["title"])
	copies := out["copies"].([]any)
	assert.Len(t, copies, 1)
}

func TestApproveCheckoutNotFound(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-approve-nf@test.com", "hash", false, nil)
	require.NoError(t, err)

	h := ApproveLibraryCheckout(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/library/checkouts/00000000-0000-0000-0000-000000000000/approve",
		strings.NewReader(`{"dueDate":"2026-05-01"}`))
	req.SetPathValue("id", "00000000-0000-0000-0000-000000000000")
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDenyLibraryCheckoutHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-deny@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := store.CreateBook(ctx, pool, store.Book{Title: "Deny API Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = store.CreateCopy(ctx, pool, store.Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_API_DENY", "denyu", "Deny User")
	require.NoError(t, err)

	co, err := store.RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	h := DenyLibraryCheckout(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/library/checkouts/"+co.ID+"/deny",
		strings.NewReader(`{"notes":"not available right now"}`))
	req.SetPathValue("id", co.ID)
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	checkouts, _, err := store.ListCheckouts(ctx, pool, "denied", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	assert.Equal(t, "denied", checkouts[0].Status)
	require.NotNil(t, checkouts[0].Notes)
	assert.Equal(t, "not available right now", *checkouts[0].Notes)
}

func TestCSVImportMissingRequiredFields(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-csvmiss@test.com", "hash", false, nil)
	require.NoError(t, err)

	csvData := "Title,Primary Author,ISBN,Book Id,Comments\n" +
		"\"\",\"Ursula K. Le Guin\",9780061054884,12345,\n" +
		"\"Neuromancer\",\"\",9780441569595,67890,\n"

	body, contentType := buildMultipartCSV(t, csvData)

	h := ImportLibraryBooks(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/library/books/import", body)
	req.Header.Set("Content-Type", contentType)
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, float64(0), out["imported"])
	assert.Equal(t, float64(2), out["skipped"])
	errs := out["errors"].([]any)
	assert.Len(t, errs, 2)
}

func TestCancelLibraryHoldHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-api-cancel-hold@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := store.CreateBook(ctx, pool, store.Book{Title: "Hold Cancel Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_API_CANCELHOLD", "chold", "Cancel Hold")
	require.NoError(t, err)

	hold, err := store.PlaceHold(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	h := CancelLibraryHold(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/library/holds/"+hold.ID+"/cancel", nil)
	req.SetPathValue("id", hold.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	holds, err := store.ListHoldsForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Empty(t, holds)

	_ = admin
}

func TestCreateLibraryBook_ISBNLookup(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-isbn-lt@test.com", "hash", false, nil)
	require.NoError(t, err)

	// Store a fake LT API key so the handler takes the LT path.
	require.NoError(t, store.SetServiceConfig(ctx, pool, "librarything", "api_key", "fake-lt-key", false, nil, encKey))

	ltJSON := `{"stat":"ok","result":{"works":[{"work_id":"99","title":"The Dispossessed","author_fl":"Ursula K. Le Guin","img":"","description":""}]}}`

	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(ltJSON)),
			Header:     make(http.Header),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = orig })

	h := CreateLibraryBook(pool, encKey)
	req := httptest.NewRequest(http.MethodPost, "/api/library/books", strings.NewReader(`{"isbn":"9780061054884","copies":1}`))
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	books, _, err := store.ListBooks(ctx, pool, "Dispossessed", 1, 10)
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, "The Dispossessed", books[0].Title)
	assert.Equal(t, "Ursula K. Le Guin", books[0].Author)
}

func TestCreateLibraryBook_ISBNFallback(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	admin, err := store.CreateWebAdmin(ctx, pool, "lib-isbn-ol@test.com", "hash", false, nil)
	require.NoError(t, err)

	// No API key — handler falls through to Open Library.
	olJSON := `{"ISBN:9780441569595":{"title":"Neuromancer","authors":[{"name":"William Gibson"}],"cover":{"medium":""},"excerpts":[]}}`

	orig := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(olJSON)),
			Header:     make(http.Header),
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = orig })

	h := CreateLibraryBook(pool, encKey)
	req := httptest.NewRequest(http.MethodPost, "/api/library/books", strings.NewReader(`{"isbn":"9780441569595","copies":1}`))
	req = req.WithContext(web.ContextWithUserID(req.Context(), admin.ID))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	books, _, err := store.ListBooks(ctx, pool, "Neuromancer", 1, 10)
	require.NoError(t, err)
	require.Len(t, books, 1)
	assert.Equal(t, "Neuromancer", books[0].Title)
	assert.Equal(t, "William Gibson", books[0].Author)
}
