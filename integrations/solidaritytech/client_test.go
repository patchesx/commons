package solidaritytech

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		apiKey:  "test-key",
		baseURL: srv.URL,
		http:    srv.Client(),
	}
}

func TestGetUserByEmail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		if email := r.URL.Query().Get("email"); email != "jane@example.com" {
			t.Errorf("email query = %q, want jane@example.com", email)
		}
		_, _ = io.WriteString(w, `{"data":[{"id":42,"email":"jane@example.com","first_name":"Jane","last_name":"Doe","hash_id":"abc"}],"meta":{"total_count":1}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	u, err := c.GetUserByEmail(t.Context(), "jane@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if u.ID != 42 || u.FirstName != "Jane" || u.HashID != "abc" {
		t.Errorf("user = %+v", u)
	}
}

func TestGetUserByEmailNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"meta":{"total_count":0}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetUserByEmail(t.Context(), "nobody@example.com")
	if err != ErrUserNotFound {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestGetUserByID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/users/42") {
			t.Errorf("path = %q, want .../users/42", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":42,"email":"jane@example.com","first_name":"Jane"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	u, err := c.GetUserByID(t.Context(), 42)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if u.ID != 42 || u.FirstName != "Jane" {
		t.Errorf("user = %+v", u)
	}
}

func TestUpdateUserCustomProperties(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.UpdateUserCustomProperties(t.Context(), 42, map[string]any{"slack_user_id": "U123"}, false); err != nil {
		t.Fatalf("UpdateUserCustomProperties: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/users/42" {
		t.Errorf("path = %q, want /users/42", gotPath)
	}
	if props, _ := gotBody["custom_user_properties"].(map[string]any); props["slack_user_id"] != "U123" {
		t.Errorf("custom_user_properties = %#v, want slack_user_id=U123", gotBody["custom_user_properties"])
	}
	if appendVal, _ := gotBody["append_custom_user_properties"].(bool); appendVal {
		t.Errorf("append_custom_user_properties = %v, want false", appendVal)
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetUserByID(t.Context(), 1)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want a rate-limit error", err)
	}
}

func TestNon2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetUserByID(t.Context(), 1)
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("err = %v, want a 422 error", err)
	}
}
