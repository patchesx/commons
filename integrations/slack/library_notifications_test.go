//go:build ignore

package slack

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/permissions"
	"commons/store"
)

// TestNotifyNewCheckoutRequestRouting verifies that NotifyNewCheckoutRequest DMs all
// users with the library.manage permission (and only them).
func TestNotifyNewCheckoutRequestRouting(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	// Wire getClient() to our test server.
	err := store.SetServiceConfig(ctx, pool, "slack", "bot_token", "test-lib-token", false, nil, encKey)
	require.NoError(t, err)
	mu.Lock()
	cachedToken = "test-lib-token"
	cachedClient = client
	mu.Unlock()
	pkgPool = pool
	pkgEncKey = encKey

	// manager — has library.manage permission, should receive a DM.
	mgr, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_MGR", "mgr", "Manager")
	require.NoError(t, err)
	mgrRole, err := store.CreateRole(ctx, pool, "lib-manage-r", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(ctx, pool, mgrRole.ID, permissions.LibraryManage))
	mgrGroup, err := store.CreateRoleGroup(ctx, pool, "lib-manage-g", "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(ctx, pool, mgrGroup.ID, mgrRole.ID))
	require.NoError(t, store.AssignGroupToUser(ctx, pool, mgr.ID, mgrGroup.ID))

	// viewer — library.view only, should NOT receive a DM.
	viewer, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_LIB_VIEW", "viewer", "Viewer")
	require.NoError(t, err)
	viewRole, err := store.CreateRole(ctx, pool, "lib-view-r", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRolePermission(ctx, pool, viewRole.ID, permissions.LibraryView))
	viewGroup, err := store.CreateRoleGroup(ctx, pool, "lib-view-g", "", "")
	require.NoError(t, err)
	require.NoError(t, store.AddRoleToGroup(ctx, pool, viewGroup.ID, viewRole.ID))
	require.NoError(t, store.AssignGroupToUser(ctx, pool, viewer.ID, viewGroup.ID))

	NotifyNewCheckoutRequest(ctx, pool, "Alice", "The Dispossessed")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.dmOpens, 1, "should DM exactly one manager")
	assert.Equal(t, "U_LIB_MGR", fake.dmOpens[0])
	require.Len(t, fake.posts, 1)
	assert.Contains(t, fake.posts[0].Text, "Alice")
	assert.Contains(t, fake.posts[0].Text, "The Dispossessed")
}

// TestSendOverdueRemindersDedup verifies that the 23-hour window prevents duplicate DMs.
func TestSendOverdueRemindersDedup(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	err := store.SetServiceConfig(ctx, pool, "slack", "bot_token", "test-overdue-token", false, nil, encKey)
	require.NoError(t, err)
	mu.Lock()
	cachedToken = "test-overdue-token"
	cachedClient = client
	mu.Unlock()
	pkgPool = pool
	pkgEncKey = encKey

	borrower, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_BORROW", "borrower", "Borrower")
	require.NoError(t, err)

	var bookID, copyID, checkoutID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO library.books (title, author) VALUES ('Overdue Book', 'Author') RETURNING id`,
	).Scan(&bookID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO library.copies (book_id) VALUES ($1) RETURNING id`, bookID,
	).Scan(&copyID))

	pastDue := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO library.checkouts (copy_id, user_id, status, due_date, checked_out_at)
		VALUES ($1, $2, 'active', $3, NOW()) RETURNING id
	`, copyID, borrower.ID, pastDue).Scan(&checkoutID))

	// First call — should send a DM.
	require.NoError(t, SendOverdueReminders(ctx, pool))

	fake.mu.Lock()
	firstDMs := len(fake.dmOpens)
	fake.mu.Unlock()
	assert.Equal(t, 1, firstDMs, "first call should send one DM")

	// Second call immediately — de-dup should suppress it.
	require.NoError(t, SendOverdueReminders(ctx, pool))

	fake.mu.Lock()
	secondDMs := len(fake.dmOpens)
	fake.mu.Unlock()
	assert.Equal(t, 1, secondDMs, "second call within 23h should not send another DM")
}

// TestSendOverdueRemindersSkipsReturned verifies that returned checkouts are not notified.
func TestSendOverdueRemindersSkipsReturned(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	err := store.SetServiceConfig(ctx, pool, "slack", "bot_token", "test-returned-token", false, nil, encKey)
	require.NoError(t, err)
	mu.Lock()
	cachedToken = "test-returned-token"
	cachedClient = client
	mu.Unlock()
	pkgPool = pool
	pkgEncKey = encKey

	borrower, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_RET_USER", "ret", "Returned")
	require.NoError(t, err)

	var bookID, copyID string
	pool.QueryRow(ctx, `INSERT INTO library.books (title, author) VALUES ('Past Book', 'Author') RETURNING id`).Scan(&bookID)
	pool.QueryRow(ctx, `INSERT INTO library.copies (book_id) VALUES ($1) RETURNING id`, bookID).Scan(&copyID)

	pastDue := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	_, err = pool.Exec(ctx, `
		INSERT INTO library.checkouts (copy_id, user_id, status, due_date, checked_out_at, returned_at)
		VALUES ($1, $2, 'returned', $3, NOW(), NOW())
	`, copyID, borrower.ID, pastDue)
	require.NoError(t, err)

	require.NoError(t, SendOverdueReminders(ctx, pool))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.dmOpens, "returned checkouts should not trigger overdue reminders")
}

// TestNotifyLibraryManagers_NoManagers verifies that calling the notification
// function when no users have library.manage returns nil without panicking.
func TestNotifyLibraryManagers_NoManagers(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, _ := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	err := store.SetServiceConfig(ctx, pool, "slack", "bot_token", "test-nomanager-token", false, nil, encKey)
	require.NoError(t, err)
	mu.Lock()
	cachedToken = "test-nomanager-token"
	cachedClient = client
	mu.Unlock()
	pkgPool = pool
	pkgEncKey = encKey

	// No users with library.manage — should return nil and not panic.
	NotifyNewCheckoutRequest(ctx, pool, "Alice", "The Dispossessed")
}

// TestSendOverdueReminders_ResendsAfter23Hours verifies that a checkout whose
// overdue_notified_at is older than 23 hours triggers a second DM.
func TestSendOverdueReminders_ResendsAfter23Hours(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	err := store.SetServiceConfig(ctx, pool, "slack", "bot_token", "test-resend-token", false, nil, encKey)
	require.NoError(t, err)
	mu.Lock()
	cachedToken = "test-resend-token"
	cachedClient = client
	mu.Unlock()
	pkgPool = pool
	pkgEncKey = encKey

	borrower, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_RESEND", "resend", "Resend User")
	require.NoError(t, err)

	var bookID, copyID, checkoutID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO library.books (title, author) VALUES ('Resend Book', 'Author') RETURNING id`,
	).Scan(&bookID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO library.copies (book_id) VALUES ($1) RETURNING id`, bookID,
	).Scan(&copyID))

	pastDue := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO library.checkouts (copy_id, user_id, status, due_date, checked_out_at)
		VALUES ($1, $2, 'active', $3, NOW()) RETURNING id
	`, copyID, borrower.ID, pastDue).Scan(&checkoutID))

	// First call — should DM.
	require.NoError(t, SendOverdueReminders(ctx, pool))

	fake.mu.Lock()
	afterFirst := len(fake.dmOpens)
	fake.mu.Unlock()
	assert.Equal(t, 1, afterFirst)

	// Backdate overdue_notified_at to > 23 hours ago — simulates the window expiring.
	_, err = pool.Exec(ctx, `
		UPDATE library.checkouts
		SET overdue_notified_at = NOW() - INTERVAL '25 hours'
		WHERE id = $1
	`, checkoutID)
	require.NoError(t, err)

	// Second call after window — should send again.
	require.NoError(t, SendOverdueReminders(ctx, pool))

	fake.mu.Lock()
	afterSecond := len(fake.dmOpens)
	fake.mu.Unlock()
	assert.Equal(t, 2, afterSecond, "resend after 23h window should send another DM")
}
