package nextcloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

// fakeWebDAV captures WebDAV requests issued by the Nextcloud client.
type fakeWebDAV struct {
	mu        sync.Mutex
	mkcols    []string          // URL path of each MKCOL call
	puts      map[string][]byte // filename (last path segment) → body
	mkcolResp int               // status to return for MKCOL; 0 defaults to 201
}

func newFakeWebDAVServer(t *testing.T) (*httptest.Server, *fakeWebDAV) {
	t.Helper()
	fwd := &fakeWebDAV{puts: map[string][]byte{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/remote.php/dav/files/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "MKCOL":
			fwd.mu.Lock()
			fwd.mkcols = append(fwd.mkcols, r.URL.Path)
			code := fwd.mkcolResp
			fwd.mu.Unlock()
			if code == 0 {
				code = http.StatusCreated
			}
			w.WriteHeader(code)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			parts := strings.Split(strings.TrimRight(r.URL.Path, "/"), "/")
			name := parts[len(parts)-1]
			fwd.mu.Lock()
			fwd.puts[name] = body
			fwd.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fwd
}

// seedCreds writes server_url, username, and app_password into config_store for tests.
func seedCreds(t *testing.T, ctx context.Context, pool *pgxpool.Pool, encKey []byte, serverURL string) {
	t.Helper()
	require.NoError(t, store.SetServiceConfig(ctx, pool, "nextcloud", "server_url", serverURL, false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "nextcloud", "username", "testuser", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "nextcloud", "app_password", "secret", true, nil, encKey))
}

func TestEnsureFolderCreatesMKCOLPerSegment(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fwd := newFakeWebDAVServer(t)
	seedCreds(t, ctx, pool, encKey, srv.URL)

	err := EnsureFolder(ctx, pool, encKey, "Recordings/DSA/2024")
	require.NoError(t, err)

	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	assert.Len(t, fwd.mkcols, 3, "one MKCOL per path segment")
}

func TestEnsureFolderAccepts405AlreadyExists(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fwd := newFakeWebDAVServer(t)
	fwd.mkcolResp = http.StatusMethodNotAllowed
	seedCreds(t, ctx, pool, encKey, srv.URL)

	err := EnsureFolder(ctx, pool, encKey, "Recordings")
	require.NoError(t, err, "405 means folder already exists — should not be an error")
}

func TestUploadFileSuccess(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fwd := newFakeWebDAVServer(t)
	seedCreds(t, ctx, pool, encKey, srv.URL)

	content := "fake video data"
	err := UploadFile(ctx, pool, encKey, "Recordings/meeting", "video.mp4", strings.NewReader(content), int64(len(content)))
	require.NoError(t, err)

	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	assert.Equal(t, []byte(content), fwd.puts["video.mp4"])
}

func TestUploadFileUnexpectedStatus(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	seedCreds(t, ctx, pool, encKey, srv.URL)

	err := UploadFile(ctx, pool, encKey, "Recordings", "video.mp4", strings.NewReader("data"), 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}
