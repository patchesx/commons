//go:build ignore

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

// createTestBook is a helper that inserts a book and one active copy.
func createTestBook(t *testing.T, ctx interface{ Deadline() (time.Time, bool) }, pool interface{}) (*Book, *Copy) {
	t.Helper()
	return nil, nil // not used directly; see inline helpers below
}

func TestLibraryCreateBook(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin@test.com", "hash", false, nil)
	require.NoError(t, err)

	isbn := "978-3-16-148410-0"
	b, err := CreateBook(ctx, pool, Book{
		Title:  "The Dispossessed",
		Author: "Ursula K. Le Guin",
		ISBN:   &isbn,
	}, admin.ID)
	require.NoError(t, err)
	require.NotNil(t, b)

	assert.NotEmpty(t, b.ID)
	assert.Equal(t, "The Dispossessed", b.Title)
	assert.Equal(t, "Ursula K. Le Guin", b.Author)
	require.NotNil(t, b.ISBN)
	assert.Equal(t, isbn, *b.ISBN)
	assert.NotNil(t, b.AddedBy)
	assert.Equal(t, admin.ID, *b.AddedBy)
}

func TestLibraryRequestCheckoutHappyPath(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin2@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Parable of the Sower", Author: "Octavia Butler"}, admin.ID)
	require.NoError(t, err)

	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_CO1", "member1", "Member One")
	require.NoError(t, err)

	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	require.NotNil(t, co)

	assert.NotEmpty(t, co.ID)
	assert.Equal(t, user.ID, co.UserID)
	assert.Equal(t, "pending", co.Status)
	assert.Equal(t, book.ID, co.BookID)
}

func TestLibraryRequestCheckoutNoAvailableCopies(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin3@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Solaris", Author: "Stanisław Lem"}, admin.ID)
	require.NoError(t, err)

	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	u1, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_NOAV1", "m1", "Member 1")
	require.NoError(t, err)
	u2, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_NOAV2", "m2", "Member 2")
	require.NoError(t, err)

	// First checkout claims the only copy.
	_, err = RequestCheckout(ctx, pool, book.ID, u1.ID)
	require.NoError(t, err)

	// Second user should get ErrNoAvailableCopies.
	_, err = RequestCheckout(ctx, pool, book.ID, u2.ID)
	assert.ErrorIs(t, err, ErrNoAvailableCopies)
}

func TestLibraryRequestCheckoutMaxCheckoutsLimit(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	// Set max_checkouts to 1 in config_store.
	_, err := pool.Exec(ctx, `
		INSERT INTO config_store (service, key, value, sensitive)
		VALUES ('library', 'max_checkouts', '1', FALSE)
		ON CONFLICT (service, key) DO UPDATE SET value = '1'
	`)
	require.NoError(t, err)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin4@test.com", "hash", false, nil)
	require.NoError(t, err)

	b1, err := CreateBook(ctx, pool, Book{Title: "Book One", Author: "Author A"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: b1.ID, Condition: "good"})
	require.NoError(t, err)

	b2, err := CreateBook(ctx, pool, Book{Title: "Book Two", Author: "Author B"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: b2.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_MAX", "maxuser", "Max User")
	require.NoError(t, err)

	// First checkout succeeds.
	_, err = RequestCheckout(ctx, pool, b1.ID, user.ID)
	require.NoError(t, err)

	// Second checkout hits the limit.
	_, err = RequestCheckout(ctx, pool, b2.ID, user.ID)
	assert.ErrorIs(t, err, ErrMaxCheckoutsReached)
}

func TestLibraryApproveCheckout(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin5@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Kindred", Author: "Octavia Butler"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_APP", "appuser", "App User")
	require.NoError(t, err)

	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	dueDate := time.Now().AddDate(0, 0, 14)
	require.NoError(t, ApproveCheckout(ctx, pool, co.ID, admin.ID, dueDate))

	// Verify status changed to active.
	checkouts, total, err := ListCheckouts(ctx, pool, "active", 1, 10)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, checkouts, 1)
	assert.Equal(t, co.ID, checkouts[0].ID)
	assert.Equal(t, "active", checkouts[0].Status)
	assert.NotNil(t, checkouts[0].CheckedOutAt)
}

func TestLibraryMarkReturned(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin6@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "The Left Hand of Darkness", Author: "Ursula K. Le Guin"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_RET", "retuser", "Return User")
	require.NoError(t, err)

	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	require.NoError(t, ApproveCheckout(ctx, pool, co.ID, admin.ID, time.Now().AddDate(0, 0, 14)))
	require.NoError(t, MarkReturned(ctx, pool, co.ID, admin.ID))

	// Copy should now be available again.
	available, err := AvailableCopiesForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Len(t, available, 1)

	// Status should be returned.
	checkouts, _, err := ListCheckouts(ctx, pool, "returned", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	assert.Equal(t, co.ID, checkouts[0].ID)
	assert.NotNil(t, checkouts[0].ReturnedAt)
}

func TestLibraryPlaceHold(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin7@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Neuromancer", Author: "William Gibson"}, admin.ID)
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_HOLD1", "holduser1", "Hold User 1")
	require.NoError(t, err)

	hold, err := PlaceHold(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	require.NotNil(t, hold)

	assert.NotEmpty(t, hold.ID)
	assert.Equal(t, book.ID, hold.BookID)
	assert.Equal(t, user.ID, hold.UserID)
	assert.Equal(t, 1, hold.Position)
	assert.Nil(t, hold.NotifiedAt)
	assert.Nil(t, hold.FulfilledAt)
	assert.Nil(t, hold.CancelledAt)
}

func TestLibraryPlaceHoldDuplicate(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin8@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Count Zero", Author: "William Gibson"}, admin.ID)
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_DUPHOLD", "duphold", "Dup Hold")
	require.NoError(t, err)

	_, err = PlaceHold(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	_, err = PlaceHold(ctx, pool, book.ID, user.ID)
	assert.ErrorIs(t, err, ErrAlreadyHasHold)
}

func TestLibraryCancelHold(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin9@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Mona Lisa Overdrive", Author: "William Gibson"}, admin.ID)
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_CANCEL", "cancelhold", "Cancel Hold")
	require.NoError(t, err)

	hold, err := PlaceHold(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	require.NoError(t, CancelHold(ctx, pool, hold.ID))

	// Hold should not appear in active list.
	holds, err := ListHoldsForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Empty(t, holds)
}

func TestLibraryHoldQueueOrdering(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-admin10@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Burning Chrome", Author: "William Gibson"}, admin.ID)
	require.NoError(t, err)

	u1, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_Q1", "q1", "Queue One")
	require.NoError(t, err)
	u2, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_Q2", "q2", "Queue Two")
	require.NoError(t, err)
	u3, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_Q3", "q3", "Queue Three")
	require.NoError(t, err)

	h1, err := PlaceHold(ctx, pool, book.ID, u1.ID)
	require.NoError(t, err)
	h2, err := PlaceHold(ctx, pool, book.ID, u2.ID)
	require.NoError(t, err)
	h3, err := PlaceHold(ctx, pool, book.ID, u3.ID)
	require.NoError(t, err)

	holds, err := ListHoldsForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	require.Len(t, holds, 3)

	assert.Equal(t, h1.ID, holds[0].ID)
	assert.Equal(t, 1, holds[0].Position)
	assert.Equal(t, h2.ID, holds[1].ID)
	assert.Equal(t, 2, holds[1].Position)
	assert.Equal(t, h3.ID, holds[2].ID)
	assert.Equal(t, 3, holds[2].Position)

	// NextHoldForBook should return the first in queue.
	next, err := NextHoldForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, h1.ID, next.ID)

	// Cancel first hold; next should advance to u2.
	require.NoError(t, CancelHold(ctx, pool, h1.ID))
	next, err = NextHoldForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.Equal(t, h2.ID, next.ID)
}

func TestLibraryCreateBook_DuplicateISBN(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-dup-isbn@test.com", "hash", false, nil)
	require.NoError(t, err)

	isbn := "978-0-06-105488-4"
	_, err = CreateBook(ctx, pool, Book{Title: "Book One", Author: "Author A", ISBN: &isbn}, admin.ID)
	require.NoError(t, err)

	_, err = CreateBook(ctx, pool, Book{Title: "Book Two", Author: "Author B", ISBN: &isbn}, admin.ID)
	assert.Error(t, err, "second insert with same ISBN should fail")
}

func TestLibraryListBooks_Search(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-search@test.com", "hash", false, nil)
	require.NoError(t, err)

	_, err = CreateBook(ctx, pool, Book{Title: "The Dispossessed", Author: "Ursula K. Le Guin"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateBook(ctx, pool, Book{Title: "Neuromancer", Author: "William Gibson"}, admin.ID)
	require.NoError(t, err)

	// Title search.
	books, total, err := ListBooks(ctx, pool, "dispossessed", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, books, 1)
	assert.Equal(t, "The Dispossessed", books[0].Title)

	// Author search.
	books, total, err = ListBooks(ctx, pool, "gibson", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, books, 1)
	assert.Equal(t, "Neuromancer", books[0].Title)

	// Non-matching search returns empty.
	books, total, err = ListBooks(ctx, pool, "nonexistent", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, books)
}

func TestLibraryDeleteBook_BlockedByActiveCheckout(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-del-block@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Blocked Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_DEL_BLK", "delblk", "Del Blk")
	require.NoError(t, err)

	// Create a pending checkout.
	_, err = RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	err = DeleteBook(ctx, pool, book.ID)
	assert.Error(t, err, "delete should fail with active/pending checkout")
}

func TestLibraryDeleteBook_AllowedWithNoCheckouts(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-del-ok@test.com", "hash", false, nil)
	require.NoError(t, err)

	// Book with a copy that was never checked out can be deleted.
	book, err := CreateBook(ctx, pool, Book{Title: "Deletable Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	require.NoError(t, DeleteBook(ctx, pool, book.ID))

	books, total, err := ListBooks(ctx, pool, "Deletable Book", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, books)
}

func TestLibraryCreateCopy_WithBarcode(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-barcode@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Barcode Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)

	barcode := "LIB-0001"
	cp, err := CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good", Barcode: &barcode})
	require.NoError(t, err)
	require.NotNil(t, cp.Barcode)
	assert.Equal(t, barcode, *cp.Barcode)

	copies, err := ListCopiesForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	require.Len(t, copies, 1)
	require.NotNil(t, copies[0].Barcode)
	assert.Equal(t, barcode, *copies[0].Barcode)
}

func TestLibraryCreateCopy_DuplicateBarcode(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-dupbc@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Dup Barcode Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)

	barcode := "LIB-DUP-001"
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good", Barcode: &barcode})
	require.NoError(t, err)

	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good", Barcode: &barcode})
	assert.Error(t, err, "second copy with same barcode should fail")
}

func TestLibraryAvailableCopiesForBook(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-avail@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Avail Test Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)

	cp1, err := CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	// Both copies available initially.
	avail, err := AvailableCopiesForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Len(t, avail, 2)

	// Check out copy 1 — only copy 2 should remain available.
	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_AVAIL1", "avail1", "Avail User 1")
	require.NoError(t, err)
	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	assert.Equal(t, cp1.ID, co.CopyID, "should pick first available copy")

	avail, err = AvailableCopiesForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Len(t, avail, 1)
}

func TestLibraryDeactivateCopy_BlockedIfCheckedOut(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-deact@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Deact Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	cp, err := CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_DEACT", "deact", "Deact User")
	require.NoError(t, err)

	_, err = RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	err = DeactivateCopy(ctx, pool, cp.ID)
	assert.Error(t, err, "deactivate should fail with pending checkout")
}

func TestLibraryRequestCheckout_UserAlreadyHasBook(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-dup-co@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Dup Checkout Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_DUPCO", "dupco", "Dup Co User")
	require.NoError(t, err)

	// First checkout succeeds.
	_, err = RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	// Second checkout for the same book should fail.
	_, err = RequestCheckout(ctx, pool, book.ID, user.ID)
	assert.ErrorIs(t, err, ErrAlreadyHasCheckout)
}

func TestLibraryDenyCheckout(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-deny@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Denied Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_DENY", "denyuser", "Deny User")
	require.NoError(t, err)

	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	require.NoError(t, DenyCheckout(ctx, pool, co.ID, admin.ID, "not available"))

	checkouts, _, err := ListCheckouts(ctx, pool, "denied", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	assert.Equal(t, co.ID, checkouts[0].ID)
	assert.Equal(t, "denied", checkouts[0].Status)
	require.NotNil(t, checkouts[0].Notes)
	assert.Equal(t, "not available", *checkouts[0].Notes)
}

func TestLibraryMarkReturned_NoOpIfAlreadyReturned(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-ret2@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Double Return Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_RET2", "ret2user", "Ret2 User")
	require.NoError(t, err)

	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	require.NoError(t, ApproveCheckout(ctx, pool, co.ID, admin.ID, time.Now().AddDate(0, 0, 14)))
	require.NoError(t, MarkReturned(ctx, pool, co.ID, admin.ID))

	// Second call should not error.
	require.NoError(t, MarkReturned(ctx, pool, co.ID, admin.ID))

	checkouts, _, err := ListCheckouts(ctx, pool, "returned", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	assert.Equal(t, "returned", checkouts[0].Status)
}

func TestLibraryExtendDueDate(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-ext@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Extend Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_EXT", "extuser", "Ext User")
	require.NoError(t, err)

	co, err := RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)
	origDue := time.Now().AddDate(0, 0, 14)
	require.NoError(t, ApproveCheckout(ctx, pool, co.ID, admin.ID, origDue))

	newDue := time.Now().AddDate(0, 0, 28)
	require.NoError(t, ExtendDueDate(ctx, pool, co.ID, newDue))

	checkouts, _, err := ListCheckouts(ctx, pool, "active", 1, 10)
	require.NoError(t, err)
	require.Len(t, checkouts, 1)
	require.NotNil(t, checkouts[0].DueDate)
	assert.Equal(t, newDue.Format("2006-01-02"), checkouts[0].DueDate.Format("2006-01-02"))
}

func TestLibraryListOverdueCheckouts(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-overdue@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Overdue Test Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	u1, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_OD1", "od1", "Overdue 1")
	require.NoError(t, err)
	u2, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_OD2", "od2", "Overdue 2")
	require.NoError(t, err)

	// Overdue checkout: approved with past due date.
	co1, err := RequestCheckout(ctx, pool, book.ID, u1.ID)
	require.NoError(t, err)
	pastDue := time.Now().AddDate(0, 0, -5)
	require.NoError(t, ApproveCheckout(ctx, pool, co1.ID, admin.ID, pastDue))

	// Non-overdue checkout: future due date.
	co2, err := RequestCheckout(ctx, pool, book.ID, u2.ID)
	require.NoError(t, err)
	futureDue := time.Now().AddDate(0, 0, 14)
	require.NoError(t, ApproveCheckout(ctx, pool, co2.ID, admin.ID, futureDue))

	overdue, err := ListOverdueCheckouts(ctx, pool)
	require.NoError(t, err)
	require.Len(t, overdue, 1)
	assert.Equal(t, co1.ID, overdue[0].ID)
	assert.Equal(t, "active", overdue[0].Status)
}

func TestLibraryPlaceHold_BlockedIfActiveCheckout(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-holdblock@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Hold Block Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: book.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_HOLDBLK", "holdblk", "Hold Blk")
	require.NoError(t, err)

	// Request checkout — user now has pending checkout.
	_, err = RequestCheckout(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	// Placing a hold on the same book should fail.
	_, err = PlaceHold(ctx, pool, book.ID, user.ID)
	assert.ErrorIs(t, err, ErrAlreadyHasCheckout)
}

func TestLibraryFulfillHold(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-fulfill@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Fulfill Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_FULFILL", "fulfill", "Fulfill User")
	require.NoError(t, err)

	hold, err := PlaceHold(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	require.NoError(t, FulfillHold(ctx, pool, hold.ID, ""))

	// Hold should no longer appear in active list.
	holds, err := ListHoldsForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Empty(t, holds)

	// NextHoldForBook should return nil.
	next, err := NextHoldForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Nil(t, next)

	_ = admin
}

func TestLibraryMarkHoldNotified(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-notifhold@test.com", "hash", false, nil)
	require.NoError(t, err)

	book, err := CreateBook(ctx, pool, Book{Title: "Notify Hold Book", Author: "Author"}, admin.ID)
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_NOTIFHOLD", "notifhold", "Notif Hold")
	require.NoError(t, err)

	hold, err := PlaceHold(ctx, pool, book.ID, user.ID)
	require.NoError(t, err)

	require.NoError(t, MarkHoldNotified(ctx, pool, hold.ID))

	// NextHoldForBook filters on notified_at IS NULL — should return nil.
	next, err := NextHoldForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	assert.Nil(t, next)

	// ListHoldsForBook still includes notified holds.
	holds, err := ListHoldsForBook(ctx, pool, book.ID)
	require.NoError(t, err)
	require.Len(t, holds, 1)
	assert.NotNil(t, holds[0].NotifiedAt)

	_ = admin
}

func TestLibraryUserActiveCheckoutCount(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	admin, err := CreateWebAdmin(ctx, pool, "lib-count@test.com", "hash", false, nil)
	require.NoError(t, err)

	b1, err := CreateBook(ctx, pool, Book{Title: "Count Book 1", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: b1.ID, Condition: "good"})
	require.NoError(t, err)

	b2, err := CreateBook(ctx, pool, Book{Title: "Count Book 2", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: b2.ID, Condition: "good"})
	require.NoError(t, err)

	b3, err := CreateBook(ctx, pool, Book{Title: "Count Book 3", Author: "Author"}, admin.ID)
	require.NoError(t, err)
	_, err = CreateCopy(ctx, pool, Copy{BookID: b3.ID, Condition: "good"})
	require.NoError(t, err)

	user, err := GetOrCreateUserByIdentity(ctx, pool, "slack", "U_COUNT", "countuser", "Count User")
	require.NoError(t, err)

	count, err := UserActiveCheckoutCount(ctx, pool, user.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Pending counts.
	co1, err := RequestCheckout(ctx, pool, b1.ID, user.ID)
	require.NoError(t, err)
	count, err = UserActiveCheckoutCount(ctx, pool, user.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Active also counts.
	co2, err := RequestCheckout(ctx, pool, b2.ID, user.ID)
	require.NoError(t, err)
	require.NoError(t, ApproveCheckout(ctx, pool, co2.ID, admin.ID, time.Now().AddDate(0, 0, 14)))
	count, err = UserActiveCheckoutCount(ctx, pool, user.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Returned does not count.
	require.NoError(t, ApproveCheckout(ctx, pool, co1.ID, admin.ID, time.Now().AddDate(0, 0, 14)))
	require.NoError(t, MarkReturned(ctx, pool, co1.ID, admin.ID))
	count, err = UserActiveCheckoutCount(ctx, pool, user.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	_ = b3
}

func TestLibraryHasLibraryBooks(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	// Empty catalog.
	has, err := HasLibraryBooks(ctx, pool)
	require.NoError(t, err)
	assert.False(t, has)

	admin, err := CreateWebAdmin(ctx, pool, "lib-has@test.com", "hash", false, nil)
	require.NoError(t, err)

	_, err = CreateBook(ctx, pool, Book{Title: "A Book", Author: "An Author"}, admin.ID)
	require.NoError(t, err)

	has, err = HasLibraryBooks(ctx, pool)
	require.NoError(t, err)
	assert.True(t, has)
}
