package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

// ---- legislative bodies ----

func TestListLegislativeBodiesReturnsList(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListLegislativeBodies(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/legislation/bodies", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []store.LegislativeBody
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	// Migrations seed several bodies; just verify the response is a well-formed list.
	assert.NotNil(t, out)
}

func TestCreateBodyHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateBody(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/bodies",
		strings.NewReader(`{"Name":"Test Body","Level":"state","State":"MO","DataSource":"openstates"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.LegislativeBody
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Test Body", out.Name)
	assert.NotEmpty(t, out.ID)
	assert.True(t, out.Active)
}

func TestCreateBodyHandlerMissingFields(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateBody(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/bodies",
		strings.NewReader(`{"Name":"Missing Level","State":"MO","DataSource":"openstates"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- bills ----

func TestListBillsEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListBills(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/legislation/bills", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []store.Bill
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Empty(t, out)
}

func TestCreateBillHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	h := CreateBill(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/bills",
		strings.NewReader(`{"BodyID":"`+body.ID+`","Identifier":"HB 1","Title":"Test Bill","Following":true}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.Bill
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "HB 1", out.Identifier)
	assert.NotEmpty(t, out.ID)
}

func TestCreateBillHandlerMissingFields(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateBill(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/bills",
		strings.NewReader(`{"body_id":"some-id","identifier":"HB 1"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGetBillHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))
	b := &store.Bill{BodyID: body.ID, Identifier: "HB 2", Title: "Get Me", Following: true}
	require.NoError(t, store.CreateBill(ctx, pool, b))

	h := GetBill(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/legislation/bills/"+b.ID, nil)
	req.SetPathValue("id", b.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out store.Bill
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Get Me", out.Title)
}

func TestGetBillHandlerNotFound(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := GetBill(pool)
	// Use a well-formed UUID that simply doesn't exist in the DB.
	missingID := "00000000-0000-0000-0000-000000000000"
	req := httptest.NewRequest(http.MethodGet, "/api/legislation/bills/"+missingID, nil)
	req.SetPathValue("id", missingID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDismissBillHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))
	b := &store.Bill{BodyID: body.ID, Identifier: "HB 3", Title: "Dismiss Me", Following: true}
	require.NoError(t, store.CreateBill(ctx, pool, b))

	h := DismissBill(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/legislation/bills/"+b.ID, nil)
	req.SetPathValue("id", b.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	got, err := store.GetBill(ctx, pool, b.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.False(t, got.Following)
}

// ---- filters ----

func TestCreateBodyFilterHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	body := store.LegislativeBody{Name: "Test House", Level: "state", State: "MO", DataSource: "openstates", Active: true}
	require.NoError(t, store.CreateLegislativeBody(ctx, pool, &body))

	h := CreateBodyFilter(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/bodies/"+body.ID+"/filters",
		strings.NewReader(`{"filterType":"subject","value":"Housing"}`))
	req.SetPathValue("id", body.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.ImportFilter
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Housing", out.Value)
}

func TestCreateBodyFilterHandlerMissingFields(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateBodyFilter(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/bodies/some-id/filters",
		strings.NewReader(`{"filterType":"subject"}`))
	req.SetPathValue("id", "some-id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---- tags ----

func TestCreateTagHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateTag(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/tags",
		strings.NewReader(`{"name":"Housing"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.Tag
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Housing", out.Name)
}

func TestCreateTagHandlerDuplicateReturns409(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateTag(pool)
	body := strings.NewReader(`{"name":"Duplicate"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/legislation/tags", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	req2 := httptest.NewRequest(http.MethodPost, "/api/legislation/tags",
		strings.NewReader(`{"name":"Duplicate"}`))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusConflict, w2.Code)
}

// ---- trigger sync ----

func TestTriggerSyncHandler(t *testing.T) {
	called := make(chan struct{}, 1)
	h := TriggerSync(func() { called <- struct{}{} })

	req := httptest.NewRequest(http.MethodPost, "/api/legislation/sync", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "sync started", out["status"])

	// Goroutine should fire quickly; give it a generous window.
	select {
	case <-called:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("sync function was not called within timeout")
	}
}
