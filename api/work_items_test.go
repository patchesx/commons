package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/platform"
	"commons/store"
)

const (
	timeout      = 2 * time.Second
	shortTimeout = 200 * time.Millisecond
	tick         = 10 * time.Millisecond
)

// mockNotifier records NotifyUser calls for assertions.
type mockNotifier struct {
	calls []mockNotifyCall
}

type mockNotifyCall struct {
	userID string
	msg    platform.Message
}

func (m *mockNotifier) NotifyUser(_ context.Context, userID string, msg platform.Message) error {
	m.calls = append(m.calls, mockNotifyCall{userID: userID, msg: msg})
	return nil
}

func TestListWorkItemsHandlerEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListWorkItems(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/work-items", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Empty(t, out)
}

func TestListWorkItemsHandlerStatusFilter(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_H_LIST", "lister", "Lister")
	require.NoError(t, err)

	open, err := store.CreateWorkItem(ctx, pool, u.ID, "issue", "Open one", nil)
	require.NoError(t, err)

	closed, err := store.CreateWorkItem(ctx, pool, u.ID, "issue", "Closed one", nil)
	require.NoError(t, err)
	require.NoError(t, store.UpdateWorkItemStatus(ctx, pool, closed.ID, "closed", nil, nil))

	h := ListWorkItems(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/work-items?status=open", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out, 1)
	assert.Equal(t, open.ID, out[0]["id"])
}

func TestGetWorkItemHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_H_GET", "getter", "Getter")
	require.NoError(t, err)

	item, err := store.CreateWorkItem(ctx, pool, u.ID, "feature_request", "Get me", nil)
	require.NoError(t, err)

	h := GetWorkItem(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/work-items/"+item.ID, nil)
	req.SetPathValue("id", item.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, item.ID, out["id"])
	assert.Equal(t, "feature_request", out["type"])
	assert.Equal(t, "Get me", out["title"])
}

func TestGetWorkItemHandlerNotFound(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := GetWorkItem(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/work-items/00000000-0000-0000-0000-000000000000", nil)
	req.SetPathValue("id", "00000000-0000-0000-0000-000000000000")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateWorkItemHandlerInvalidStatus(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_H_BADST", "badst", "Bad Status")
	require.NoError(t, err)

	item, err := store.CreateWorkItem(ctx, pool, u.ID, "issue", "Bad status test", nil)
	require.NoError(t, err)

	notifier := &mockNotifier{}
	h := UpdateWorkItem(pool, testhelpers.EncKey(), notifier)
	req := httptest.NewRequest(http.MethodPut, "/api/work-items/"+item.ID,
		strings.NewReader(`{"status":"banana"}`))
	req.SetPathValue("id", item.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, notifier.calls)
}

func TestUpdateWorkItemHandlerStatusChange(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_H_CHG", "changer", "Changer")
	require.NoError(t, err)

	item, err := store.CreateWorkItem(ctx, pool, u.ID, "issue", "Status change test", nil)
	require.NoError(t, err)

	notifier := &mockNotifier{}
	h := UpdateWorkItem(pool, testhelpers.EncKey(), notifier)
	req := httptest.NewRequest(http.MethodPut, "/api/work-items/"+item.ID,
		strings.NewReader(`{"status":"acknowledged"}`))
	req.SetPathValue("id", item.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// Verify persisted.
	updated, err := store.GetWorkItem(ctx, pool, item.ID)
	require.NoError(t, err)
	assert.Equal(t, "acknowledged", updated.Status)

	// Notification fires asynchronously — allow it to complete.
	require.Eventually(t, func() bool { return len(notifier.calls) == 1 }, timeout, tick)
	assert.Equal(t, u.ID, notifier.calls[0].userID)
	assert.Contains(t, notifier.calls[0].msg.Text, "Open → Acknowledged")
}

func TestUpdateWorkItemHandlerNoNotificationWhenStatusUnchanged(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_WI_H_SAME", "samest", "Same Status")
	require.NoError(t, err)

	item, err := store.CreateWorkItem(ctx, pool, u.ID, "issue", "Same status", nil)
	require.NoError(t, err)

	notifier := &mockNotifier{}
	h := UpdateWorkItem(pool, testhelpers.EncKey(), notifier)
	// Submit same status ("open") with updated notes only.
	req := httptest.NewRequest(http.MethodPut, "/api/work-items/"+item.ID,
		strings.NewReader(`{"status":"open","adminNotes":"just a note"}`))
	req.SetPathValue("id", item.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// No notification — give the goroutine a moment to NOT fire.
	assert.Never(t, func() bool { return len(notifier.calls) > 0 }, shortTimeout, tick)
}
