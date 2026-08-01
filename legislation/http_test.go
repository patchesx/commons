package legislation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/platform"
	"commons/store"
)

// ---- mock notifier ----

type mockNotifier struct {
	mu       sync.Mutex
	messages []notifierCall
}

type notifierCall struct {
	UserID string
	Text   string
}

func (n *mockNotifier) NotifyUser(_ context.Context, userID string, msg platform.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, notifierCall{userID, msg.Text})
	return nil
}

func (n *mockNotifier) calls() []notifierCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notifierCall(nil), n.messages...)
}

// ---- HTTP redirect transport ----

// redirectTransport rewrites every outgoing request to hit the given test server instead.
type redirectTransport struct {
	serverURL string
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.serverURL + req.URL.Path + "?" + req.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, target.String(), req.Body)
	if err != nil {
		return nil, err
	}
	for k, vs := range req.Header {
		newReq.Header[k] = vs
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

func withTestHTTPClient(srv *httptest.Server, f func()) {
	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &redirectTransport{serverURL: srv.URL}}
	defer func() { http.DefaultClient = orig }()
	f()
}

// ---- fetchOpenStatesBillsWithParams tests ----

func TestFetchOpenStatesBillsSinglePage(t *testing.T) {
	bills := []openStatesBill{
		{ID: "OS-1", Identifier: "HB 1", Title: "Test Bill"},
		{ID: "OS-2", Identifier: "HB 2", Title: "Other Bill"},
	}
	resp := openStatesResponse{
		Results: bills,
		Pagination: struct {
			TotalItems int `json:"total_items"`
			PerPage    int `json:"per_page"`
			Page       int `json:"page"`
			MaxPage    int `json:"max_page"`
		}{TotalItems: 2, PerPage: 200, Page: 1, MaxPage: 1},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	var got []openStatesBill
	withTestHTTPClient(srv, func() {
		var err error
		got, err = fetchOpenStatesBillsWithParams(context.Background(), "test-key", url.Values{"jurisdiction": {"ocd-1"}})
		require.NoError(t, err)
	})

	assert.Len(t, got, 2)
	assert.Equal(t, "OS-1", got[0].ID)
}

func TestFetchOpenStatesBillsMultiPage(t *testing.T) {
	page1 := openStatesResponse{
		Results: []openStatesBill{{ID: "OS-1", Identifier: "HB 1", Title: "Bill 1"}},
		Pagination: struct {
			TotalItems int `json:"total_items"`
			PerPage    int `json:"per_page"`
			Page       int `json:"page"`
			MaxPage    int `json:"max_page"`
		}{MaxPage: 2},
	}
	page2 := openStatesResponse{
		Results: []openStatesBill{{ID: "OS-2", Identifier: "HB 2", Title: "Bill 2"}},
		Pagination: struct {
			TotalItems int `json:"total_items"`
			PerPage    int `json:"per_page"`
			Page       int `json:"page"`
			MaxPage    int `json:"max_page"`
		}{MaxPage: 2},
	}

	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			json.NewEncoder(w).Encode(page1)
		} else {
			json.NewEncoder(w).Encode(page2)
		}
	}))
	defer srv.Close()

	var got []openStatesBill
	withTestHTTPClient(srv, func() {
		var err error
		got, err = fetchOpenStatesBillsWithParams(context.Background(), "test-key", url.Values{"jurisdiction": {"ocd-1"}})
		require.NoError(t, err)
	})

	assert.Len(t, got, 2)
	assert.Equal(t, 2, requestCount)
}

func TestFetchOpenStatesBillsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	withTestHTTPClient(srv, func() {
		_, err := fetchOpenStatesBillsWithParams(context.Background(), "bad-key", url.Values{"jurisdiction": {"ocd-1"}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}

// ---- fetchLegistarMatters tests ----

func TestFetchLegistarMattersSinglePage(t *testing.T) {
	matters := []legistarMatter{
		{MatterID: 1, MatterFile: "O-001", MatterTitle: "Test Ordinance", MatterTypeName: "Ordinance"},
		{MatterID: 2, MatterFile: "O-002", MatterTitle: "Other Matter", MatterTypeName: "Resolution"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(matters)
	}))
	defer srv.Close()

	var got []legistarMatter
	withTestHTTPClient(srv, func() {
		var err error
		got, err = fetchLegistarMatters(context.Background(), "testclient", nil, time.Now().AddDate(0, 0, -30))
		require.NoError(t, err)
	})

	assert.Len(t, got, 2)
	assert.Equal(t, 1, got[0].MatterID)
}

func TestFetchLegistarMattersPaginationStopsWhenShortPage(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// First page: full (200 items). Second page: short (< 200) → stop.
		if callCount == 1 {
			matters := make([]legistarMatter, 200)
			for i := range matters {
				matters[i] = legistarMatter{MatterID: i + 1}
			}
			json.NewEncoder(w).Encode(matters)
		} else {
			json.NewEncoder(w).Encode([]legistarMatter{{MatterID: 201}})
		}
	}))
	defer srv.Close()

	var got []legistarMatter
	withTestHTTPClient(srv, func() {
		var err error
		got, err = fetchLegistarMatters(context.Background(), "testclient", nil, time.Now().AddDate(0, 0, -30))
		require.NoError(t, err)
	})

	assert.Equal(t, 2, callCount, "should stop after page with fewer than 200 results")
	assert.Len(t, got, 201)
}

// ---- processOpenStatesBillWithCounts tests ----

func TestProcessOpenStatesBillImportsNew(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	filters := []store.ImportFilter{{FilterType: "subject", Value: "Housing"}}
	ab := openStatesBill{
		ID: "OS-NEW", Identifier: "HB 100", Title: "Housing Bill",
		Subjects:                []string{"Housing"},
		LatestActionDescription: "Introduced",
	}

	var imported, updated, skipped int
	processed := map[string]bool{}
	notifier := &mockNotifier{}

	processOpenStatesBillWithCounts(ctx, pool, testhelpers.EncKey(), notifier, body, ab, nil, filters, processed, &imported, &updated, &skipped)

	assert.Equal(t, 1, imported)
	assert.Equal(t, 0, updated)
	assert.Equal(t, 0, skipped)
}

func TestProcessOpenStatesBillUpdatesExistingAndRecordsBillUpdate(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	extID := "OS-EXIST"
	existing := &store.Bill{
		BodyID: body.ID, ExternalID: &extID, Identifier: "HB 200",
		Title: "Old Title", AutoImported: true, Following: true,
	}
	require.NoError(t, store.CreateBill(ctx, pool, existing))

	oldStatus := "Introduced"
	existing.Status = &oldStatus

	ab := openStatesBill{
		ID: extID, Identifier: "HB 200", Title: "Old Title",
		LatestActionDescription: "Passed Committee",
	}

	var imported, updated, skipped int
	processed := map[string]bool{}
	notifier := &mockNotifier{}
	filters := []store.ImportFilter{}

	processOpenStatesBillWithCounts(ctx, pool, testhelpers.EncKey(), notifier, body, ab, existing, filters, processed, &imported, &updated, &skipped)

	assert.Equal(t, 0, imported)
	assert.Equal(t, 1, updated)

	// Status changed — subscriber should be notified if any exist.
	// No subscribers seeded here, just verify bill_update was recorded.
	var updateCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM bill_updates WHERE bill_id = $1`, existing.ID,
	).Scan(&updateCount))
	assert.Greater(t, updateCount, 0, "at least one bill_update row should be recorded")
}

func TestProcessOpenStatesBillSkipsDismissed(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	extID := "OS-DISMISSED"
	dismissed := &store.Bill{
		BodyID: body.ID, ExternalID: &extID, Identifier: "HB 300",
		Title: "Dismissed Bill", AutoImported: true, Following: false,
	}
	require.NoError(t, store.CreateBill(ctx, pool, dismissed))

	ab := openStatesBill{
		ID: extID, Identifier: "HB 300", Title: "Dismissed Bill",
		Subjects:                []string{"Housing"},
		LatestActionDescription: "Updated",
	}
	filters := []store.ImportFilter{{FilterType: "subject", Value: "Housing"}}

	var imported, updated, skipped int
	processed := map[string]bool{}

	processOpenStatesBillWithCounts(ctx, pool, testhelpers.EncKey(), &mockNotifier{}, body, ab, nil, filters, processed, &imported, &updated, &skipped)

	assert.Equal(t, 0, imported)
	assert.Equal(t, 1, skipped)
}

func TestProcessOpenStatesBillSkipsNonMatchingUntracked(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	ab := openStatesBill{
		ID: "OS-NOMATCH", Identifier: "HB 999", Title: "Irrelevant Bill",
		Subjects: []string{"Agriculture"},
	}
	filters := []store.ImportFilter{{FilterType: "subject", Value: "Housing"}}

	var imported, updated, skipped int
	processed := map[string]bool{}

	processOpenStatesBillWithCounts(ctx, pool, testhelpers.EncKey(), &mockNotifier{}, body, ab, nil, filters, processed, &imported, &updated, &skipped)

	assert.Equal(t, 0, imported)
	assert.Equal(t, 0, updated)
	assert.Equal(t, 0, skipped)
}

// ---- processLegistarMatterWithCounts tests ----

func TestProcessLegistarMatterImportsNew(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	legistarClient := "kc"
	body := store.LegislativeBody{Name: "KC Council", Level: "local", State: "MO", DataSource: "legistar", Active: true, LegistarClient: &legistarClient}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	m := legistarMatter{
		MatterID: 101, MatterFile: "O-101", MatterTitle: "New Ordinance",
		MatterTypeName: "Ordinance", MatterStatusName: "Active",
	}

	var imported, updated int
	processLegistarMatterWithCounts(ctx, pool, testhelpers.EncKey(), &mockNotifier{}, body, legistarClient, m, nil, true, &imported, &updated)

	assert.Equal(t, 1, imported)
	assert.Equal(t, 0, updated)
}

func TestProcessLegistarMatterUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	legistarClient := "kc"
	body := store.LegislativeBody{Name: "KC Council", Level: "local", State: "MO", DataSource: "legistar", Active: true, LegistarClient: &legistarClient}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	extID := "202"
	existing := &store.Bill{
		BodyID: body.ID, ExternalID: &extID, Identifier: "O-202",
		Title: "Existing Ordinance", AutoImported: true, Following: true,
	}
	require.NoError(t, store.CreateBill(ctx, pool, existing))
	oldStatus := "Active"
	existing.Status = &oldStatus

	m := legistarMatter{
		MatterID: 202, MatterFile: "O-202", MatterTitle: "Existing Ordinance",
		MatterTypeName: "Ordinance", MatterStatusName: "Passed",
	}

	var imported, updated int
	processLegistarMatterWithCounts(ctx, pool, testhelpers.EncKey(), &mockNotifier{}, body, legistarClient, m, existing, false, &imported, &updated)

	assert.Equal(t, 0, imported)
	assert.Equal(t, 1, updated)
}

// ---- notifyIfChanged tests ----

func TestNotifyIfChangedSameValue(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))
	extID := "X-1"
	b := &store.Bill{BodyID: body.ID, ExternalID: &extID, Identifier: "HB 1", Title: "Bill", Following: true}
	require.NoError(t, store.CreateBill(ctx, pool, b))

	notifier := &mockNotifier{}
	same := "Active"
	notifyIfChanged(ctx, pool, testhelpers.EncKey(), notifier, b.ID, "status", &same, &same, "test")

	assert.Empty(t, notifier.calls(), "no notification when value unchanged")

	var count int
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM bill_updates WHERE bill_id = $1`, b.ID).Scan(&count)
	assert.Equal(t, 0, count, "no bill_update when value unchanged")
}

func TestNotifyIfChangedNonStatusField(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))
	extID := "X-2"
	b := &store.Bill{BodyID: body.ID, ExternalID: &extID, Identifier: "HB 2", Title: "Bill", Following: true}
	require.NoError(t, store.CreateBill(ctx, pool, b))

	notifier := &mockNotifier{}
	old, newVal := "First Reading", "Second Reading"
	notifyIfChanged(ctx, pool, testhelpers.EncKey(), notifier, b.ID, "latest_action", &old, &newVal, "test")

	assert.Empty(t, notifier.calls(), "no notification for non-status field change")

	var count int
	pool.QueryRow(ctx, `SELECT COUNT(*) FROM bill_updates WHERE bill_id = $1`, b.ID).Scan(&count)
	assert.Equal(t, 1, count, "bill_update should be recorded even for non-status fields")
}

func TestNotifyIfChangedStatusFieldNotifiesSubscribers(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))
	extID := "X-3"
	b := &store.Bill{BodyID: body.ID, ExternalID: &extID, Identifier: "HB 3", Title: "Important Bill", Following: true}
	require.NoError(t, store.CreateBill(ctx, pool, b))

	// Subscribe a user.
	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_SUB", "sub", "Subscriber")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO bill_subscriptions (bill_id, user_id) VALUES ($1, $2)`, b.ID, u.ID)
	require.NoError(t, err)

	notifier := &mockNotifier{}
	old, newVal := "Active", "Passed"
	notifyIfChanged(ctx, pool, testhelpers.EncKey(), notifier, b.ID, "status", &old, &newVal, "test")

	calls := notifier.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, u.ID, calls[0].UserID)
	assert.Contains(t, calls[0].Text, "Active")
	assert.Contains(t, calls[0].Text, "Passed")
}

func TestNotifyIfChangedStatusNoSubscribers(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))
	extID := "X-4"
	b := &store.Bill{BodyID: body.ID, ExternalID: &extID, Identifier: "HB 4", Title: "Bill", Following: true}
	require.NoError(t, store.CreateBill(ctx, pool, b))

	notifier := &mockNotifier{}
	old, newVal := "Active", "Passed"
	notifyIfChanged(ctx, pool, testhelpers.EncKey(), notifier, b.ID, "status", &old, &newVal, "test")

	assert.Empty(t, notifier.calls(), "no notifications when bill has no subscribers")
}
