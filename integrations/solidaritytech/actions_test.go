package solidaritytech

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/testhelpers"
	"commons/plugin"
	"commons/store"
)

// withBaseURL swaps the package-level API base URL for the duration of the test.
func withBaseURL(t *testing.T, url string) {
	t.Helper()
	orig := baseURL
	baseURL = url
	t.Cleanup(func() { baseURL = orig })
}

func seedAPIKey(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := store.SetServiceConfig(t.Context(), pool, "solidaritytech", "api_key", "test-key", true, nil, testhelpers.EncKey()); err != nil {
		t.Fatalf("seed api_key: %v", err)
	}
}

func seedMember(t *testing.T, pool *pgxpool.Pool, provider, externalID, name, email string) *store.User {
	t.Helper()
	u, err := store.GetOrCreateUserByIdentity(t.Context(), pool, provider, externalID, name, name)
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if email != "" {
		if err := store.UpdateUserEmail(t.Context(), pool, u.ID, email); err != nil {
			t.Fatalf("update email: %v", err)
		}
	}
	u, err = store.GetUserByID(t.Context(), pool, u.ID)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	return u
}

func TestLookupUserAction_EmailLookupAndCache(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	seedAPIKey(t, pool)
	member := seedMember(t, pool, "slack", "U123", "Jane Doe", "jane@example.com")

	emailHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("email") {
			emailHits++
			_, _ = io.WriteString(w, `{"data":[{"id":42,"email":"jane@example.com","first_name":"Jane","last_name":"Doe","hash_id":"abc"}],"meta":{"total_count":1}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	action := &LookupUserAction{pool: pool, encKey: testhelpers.EncKey()}
	out, err := action.Execute(t.Context(), map[string]any{
		"provider":    "slack",
		"external_id": "U123",
	}, plugin.NoopActionContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out["solidaritytech_user_id"] != "42" {
		t.Errorf("solidaritytech_user_id = %v, want 42", out["solidaritytech_user_id"])
	}
	if out["solidaritytech_email"] != "jane@example.com" {
		t.Errorf("solidaritytech_email = %v", out["solidaritytech_email"])
	}
	if out["member_id"] != member.ID {
		t.Errorf("member_id = %v, want %s", out["member_id"], member.ID)
	}
	if out["member_email"] != "jane@example.com" {
		t.Errorf("member_email = %v", out["member_email"])
	}
	if emailHits != 1 {
		t.Errorf("email lookup hits = %d, want 1", emailHits)
	}

	// The SolidarityTech profile id should be cached on the member.
	cached, err := store.GetUserExternalID(t.Context(), pool, member.ID, "solidaritytech")
	if err != nil {
		t.Fatalf("cached identity: %v", err)
	}
	if cached != "42" {
		t.Errorf("cached solidaritytech id = %q, want 42", cached)
	}
}

func TestLookupUserAction_CachedFastPath(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	seedAPIKey(t, pool)
	member := seedMember(t, pool, "slack", "U456", "Jane Doe", "jane@example.com")
	// Pre-seed the cached SolidarityTech identity.
	if err := store.LinkIdentity(t.Context(), pool, member.ID, "solidaritytech", "42", "Jane Doe", "jane@example.com"); err != nil {
		t.Fatalf("link identity: %v", err)
	}

	emailHits := 0
	idHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("email") {
			emailHits++
			http.NotFound(w, r)
			return
		}
		idHits++
		_, _ = io.WriteString(w, `{"id":42,"email":"jane@example.com","first_name":"Jane","last_name":"Doe"}`)
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	action := &LookupUserAction{pool: pool, encKey: testhelpers.EncKey()}
	out, err := action.Execute(t.Context(), map[string]any{
		"provider":    "slack",
		"external_id": "U456",
	}, plugin.NoopActionContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if out["solidaritytech_user_id"] != "42" {
		t.Errorf("solidaritytech_user_id = %v, want 42", out["solidaritytech_user_id"])
	}
	if emailHits != 0 {
		t.Errorf("email lookup hits = %d, want 0 (cached)", emailHits)
	}
	if idHits != 1 {
		t.Errorf("id lookup hits = %d, want 1", idHits)
	}
}

func TestLookupUserAction_Errors(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	member := seedMember(t, pool, "slack", "U789", "Jane Doe", "jane@example.com")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"meta":{"total_count":0}}`)
	}))
	defer srv.Close()

	action := &LookupUserAction{pool: pool, encKey: testhelpers.EncKey()}

	// No api_key configured.
	_, err := action.Execute(t.Context(), map[string]any{"provider": "slack", "external_id": "U789"}, plugin.NoopActionContext{})
	if err != ErrNotConfigured {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}

	seedAPIKey(t, pool)
	withBaseURL(t, srv.URL)

	// Unknown member identity.
	_, err = action.Execute(t.Context(), map[string]any{"provider": "slack", "external_id": "NOPE"}, plugin.NoopActionContext{})
	if err == nil {
		t.Fatal("expected error for unknown member, got nil")
	}

	// Member has no SolidarityTech profile for their email.
	_, err = action.Execute(t.Context(), map[string]any{"provider": "slack", "external_id": "U789"}, plugin.NoopActionContext{})
	if err == nil || err == ErrNotConfigured {
		t.Fatalf("err = %v, want a not-found error", err)
	}

	// Member without email and without cached identity cannot be looked up.
	_ = member
	seedMember(t, pool, "slack", "U000", "No Email", "")
	_, err = action.Execute(t.Context(), map[string]any{"provider": "slack", "external_id": "U000"}, plugin.NoopActionContext{})
	if err == nil {
		t.Fatal("expected error for member without email, got nil")
	}
}

func TestSetCustomPropertyAction(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	seedAPIKey(t, pool)

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
	withBaseURL(t, srv.URL)

	action := &SetCustomPropertyAction{pool: pool, encKey: testhelpers.EncKey()}
	out, err := action.Execute(t.Context(), map[string]any{
		"user_id":      "42",
		"property_key": "slack_user_id",
		"value":        "U123",
		"append":       false,
	}, plugin.NoopActionContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
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
		t.Errorf("append = %v, want false", appendVal)
	}
	if out["solidaritytech_user_id"] != "42" {
		t.Errorf("output solidaritytech_user_id = %v, want 42", out["solidaritytech_user_id"])
	}
}

func TestSetCustomPropertyAction_InvalidID(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	seedAPIKey(t, pool)

	action := &SetCustomPropertyAction{pool: pool, encKey: testhelpers.EncKey()}
	_, err := action.Execute(t.Context(), map[string]any{
		"user_id":      "not-a-number",
		"property_key": "slack_user_id",
		"value":        "U123",
	}, plugin.NoopActionContext{})
	if err == nil {
		t.Fatal("expected error for non-numeric user_id, got nil")
	}
}
