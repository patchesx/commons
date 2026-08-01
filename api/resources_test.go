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

func TestListResourcesEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListResources(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/resources", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, float64(0), out["total"])
	assert.Empty(t, out["resources"])
}

func TestCreateResourceHandler(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	cat, err := store.CreateResourceCategory(ctx, pool, "Docs")
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{
		"title":    "Guide",
		"url":      "https://example.com",
		"category": cat.Name,
	})
	h := CreateResource(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/resources", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.Resource
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Guide", out.Title)
	assert.NotEmpty(t, out.ID)
}

func TestCreateResourceHandlerMissingFields(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateResource(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/resources",
		strings.NewReader(`{"title":"No URL"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateResourceCategoryHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateResourceCategory(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/resource-categories",
		strings.NewReader(`{"name":"Meeting Recordings"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)
	var out store.ResourceCategory
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.Equal(t, "Meeting Recordings", out.Name)
	assert.NotEmpty(t, out.ID)
}

func TestCreateResourceCategoryHandlerMissingName(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := CreateResourceCategory(pool)
	req := httptest.NewRequest(http.MethodPost, "/api/resource-categories",
		strings.NewReader(`{"name":"   "}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeleteResourceCategoryConflict(t *testing.T) {
	ctx := t.Context()
	pool := testhelpers.SetupTestDB(t)

	cat, err := store.CreateResourceCategory(ctx, pool, "Active Category")
	require.NoError(t, err)

	res := &store.Resource{Title: "Doc", URL: "https://example.com", Category: cat.Name}
	require.NoError(t, store.CreateResource(ctx, pool, res))

	h := DeleteResourceCategory(pool)
	req := httptest.NewRequest(http.MethodDelete, "/api/resource-categories/"+cat.ID, nil)
	req.SetPathValue("id", cat.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestListPermissionsHandler(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	h := ListPermissions(pool)
	req := httptest.NewRequest(http.MethodGet, "/api/permissions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var out []store.Permission
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	assert.NotEmpty(t, out, "migrations seed permissions")
}
