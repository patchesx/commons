package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/platform"
	"commons/store"
)

// fakeMatrixDM represents a single DM captured by the fake Matrix server.
type fakeMatrixDM struct {
	RecipientID string
	Content     string
}

// fakeMatrix captures Matrix API calls during a test.
type fakeMatrix struct {
	mu     sync.Mutex
	dms    []*fakeMatrixDM
	byRoom map[string]*fakeMatrixDM
}

func newFakeMatrixServer(t *testing.T) (*httptest.Server, *fakeMatrix) {
	t.Helper()
	fm := &fakeMatrix{byRoom: map[string]*fakeMatrixDM{}}
	mux := http.NewServeMux()

	// CreateRoom — opens a DM room with a recipient.
	mux.HandleFunc("/_matrix/client/v3/createRoom", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Invite []string `json:"invite"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var recipientID string
		if len(body.Invite) > 0 {
			recipientID = body.Invite[0]
		}
		dm := &fakeMatrixDM{RecipientID: recipientID}
		roomID := "!dm_" + strings.NewReplacer("@", "", ":", "_").Replace(recipientID)
		fm.mu.Lock()
		fm.dms = append(fm.dms, dm)
		fm.byRoom[roomID] = dm
		fm.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"room_id": roomID})
	})

	// SendEvent — sends a message to a room (PUT /_matrix/client/v3/rooms/{roomID}/send/...).
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		// Path: /_matrix/client/v3/rooms/{roomID}/send/{eventType}/{txnId}
		// roomID may be URL-encoded; extract the segment after /rooms/
		trimmed := strings.TrimPrefix(r.URL.Path, "/_matrix/client/v3/rooms/")
		// Everything up to the next "/" is the (possibly encoded) room ID.
		slashIdx := strings.Index(trimmed, "/")
		var roomID string
		if slashIdx >= 0 {
			roomID, _ = url.PathUnescape(trimmed[:slashIdx])
		}
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		fm.mu.Lock()
		if dm, ok := fm.byRoom[roomID]; ok {
			dm.Content = body.Body
		}
		fm.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"event_id": "$event1"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fm
}

// wireMatrixFakeClient injects a mautrix.Client pointing at srvURL into the
// package-level cache, and seeds the config_store rows that getClient checks
// before consulting the client cache (matrix/enabled, credentials, and an
// access_token equal to the cached fake token). Restores the cache on test
// cleanup.
func wireMatrixFakeClient(t *testing.T, pool *pgxpool.Pool, srvURL string) {
	t.Helper()
	ctx := context.Background()
	for k, v := range map[string]string{
		"enabled":      "true",
		"access_token": "fake-test-token",
		"homeserver":   srvURL,
		"user_id":      "@bot:matrix.test",
	} {
		require.NoError(t, store.SetServiceConfig(ctx, pool, "matrix", k, v, false, nil, testhelpers.EncKey()))
	}
	c, err := mautrix.NewClient(srvURL, id.UserID("@bot:matrix.test"), "fake-test-token")
	require.NoError(t, err)
	mu.Lock()
	cachedToken = "fake-test-token"
	cachedClient = c
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		cachedToken = ""
		cachedClient = nil
		mu.Unlock()
	})
}

func TestMatrixNotifyUserNoIdentity(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	srv, fm := newFakeMatrixServer(t)
	wireMatrixFakeClient(t, pool, srv.URL)
	pkgPool = pool
	pkgEncKey = testhelpers.EncKey()

	// Create a user with only a Slack identity — no Matrix identity.
	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALICE", "alice", "Alice")
	require.NoError(t, err)

	n := NewNotifier(pool)
	err = n.NotifyUser(ctx, u.ID, platform.Message{Text: "hello"})
	require.NoError(t, err, "missing Matrix identity should return nil (skip silently)")

	fm.mu.Lock()
	defer fm.mu.Unlock()
	assert.Empty(t, fm.dms, "no DM should be opened when user has no Matrix identity")
}
