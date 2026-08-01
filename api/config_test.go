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

// listConfig calls ListConfig and decodes the response into a slice.
func doListConfig(t *testing.T, h http.HandlerFunc, service, reveal string) (int, []configEntry) {
	t.Helper()
	url := "/api/config?service=" + service
	if reveal != "" {
		url += "&reveal=" + reveal
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var out []configEntry
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	return w.Code, out
}

func TestListConfigSensitiveHiddenByDefault(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	ctx := t.Context()

	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "webhook_secret", "mysecret", true, nil, encKey))

	h := ListConfig(pool, encKey)
	_, entries := doListConfig(t, h, "zoom", "")

	var found bool
	for _, e := range entries {
		if e.Key == "webhook_secret" {
			found = true
			assert.True(t, e.Configured)
			assert.Equal(t, "***", e.Value)
		}
	}
	assert.True(t, found, "webhook_secret entry not found in response")
}

func TestListConfigSensitiveRevealedWithFlag(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	ctx := t.Context()

	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "webhook_secret", "mysecret", true, nil, encKey))

	h := ListConfig(pool, encKey)
	_, entries := doListConfig(t, h, "zoom", "true")

	for _, e := range entries {
		if e.Key == "webhook_secret" {
			assert.Equal(t, "mysecret", e.Value)
			return
		}
	}
	t.Fatal("webhook_secret entry not found")
}

func TestListConfigNotConfiguredKeyShowsEmpty(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	h := ListConfig(pool, encKey)
	_, entries := doListConfig(t, h, "zoom", "")

	for _, e := range entries {
		if e.Key == "webhook_secret" {
			assert.False(t, e.Configured)
			assert.Equal(t, "", e.Value)
			return
		}
	}
	t.Fatal("webhook_secret entry not found")
}

func TestSetConfigUpdatesValue(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	ctx := t.Context()

	h := SetConfig(pool, encKey)
	req := httptest.NewRequest(http.MethodPut, "/api/config/recordings/upload_notification_channel",
		strings.NewReader(`{"value":"C_GENERAL"}`))
	req.SetPathValue("service", "recordings")
	req.SetPathValue("key", "upload_notification_channel")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	got, err := store.GetServiceConfig(ctx, pool, "recordings", "upload_notification_channel", encKey)
	require.NoError(t, err)
	assert.Equal(t, "C_GENERAL", got)
}

func TestSetConfigUnknownKeyReturns400(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	h := SetConfig(pool, encKey)
	req := httptest.NewRequest(http.MethodPut, "/api/config/zoom/does_not_exist",
		strings.NewReader(`{"value":"anything"}`))
	req.SetPathValue("service", "zoom")
	req.SetPathValue("key", "does_not_exist")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSetConfigClearsOptionalValue(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	ctx := t.Context()

	require.NoError(t, store.SetServiceConfig(ctx, pool, "recordings", "upload_notification_channel", "C_OLD", false, nil, encKey))

	h := SetConfig(pool, encKey)
	req := httptest.NewRequest(http.MethodPut, "/api/config/recordings/upload_notification_channel",
		strings.NewReader(`{"value":""}`))
	req.SetPathValue("service", "recordings")
	req.SetPathValue("key", "upload_notification_channel")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	_, err := store.GetServiceConfig(ctx, pool, "recordings", "upload_notification_channel", encKey)
	assert.ErrorIs(t, err, store.ErrNotFound, "key should be deleted after clearing")
}

func TestSetConfigRequiredKeyCannotBeCleared(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	h := SetConfig(pool, encKey)
	req := httptest.NewRequest(http.MethodPut, "/api/config/zoom/webhook_secret",
		strings.NewReader(`{"value":""}`))
	req.SetPathValue("service", "zoom")
	req.SetPathValue("key", "webhook_secret")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
