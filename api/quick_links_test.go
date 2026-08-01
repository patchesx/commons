package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

func TestListQuickLinksEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListQuickLinks(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/quick-links", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []store.QuickLink
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Empty(t, out)
}

func TestCreateQuickLinkHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateQuickLink(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/quick-links",
		strings.NewReader(`{"label":"DSA Website","url":"https://dsastl.org"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.QuickLink
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "DSA Website", out.Label)
	assert.True(t, out.Active)
	assert.NotEmpty(t, out.ID)
}

func TestCreateQuickLinkHandlerMissingFields(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateQuickLink(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/quick-links",
		strings.NewReader(`{"label":"No URL"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUpdateQuickLinkHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	l := &store.QuickLink{Label: "Old Label", URL: "https://old.example.com", Active: true}
	require.NoError(t, store.CreateQuickLink(ctx, pool, l))

	h := UpdateQuickLink(pool)
	req := httptest.NewRequest(http.MethodPut, "/api/quick-links/"+l.ID,
		strings.NewReader(`{"label":"New Label","url":"https://new.example.com","active":true}`))
	req.SetPathValue("id", l.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestDeleteQuickLinkHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	l := &store.QuickLink{Label: "Delete Me", URL: "https://delete.example.com", Active: true}
	require.NoError(t, store.CreateQuickLink(ctx, pool, l))

	h := DeleteQuickLink(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/quick-links/"+l.ID, nil)
	req.SetPathValue("id", l.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	links, err := store.ListQuickLinks(ctx, pool)
	require.NoError(t, err)
	for _, link := range links {
		assert.NotEqual(t, l.ID, link.ID)
	}
}
