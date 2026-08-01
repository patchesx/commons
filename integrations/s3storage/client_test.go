package s3storage

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

// fakeS3 captures PutObject calls against a fake S3-compatible endpoint.
type fakeS3 struct {
	mu   sync.Mutex
	puts map[string][]byte // key → body
}

func newFakeS3Server(t *testing.T) (*httptest.Server, *fakeS3) {
	t.Helper()
	fs := &fakeS3{puts: map[string][]byte{}}
	// TLS matters: over plain HTTP the SDK must SHA256-hash the payload for
	// SigV4, which fails on unseekable (streaming) bodies. Over TLS it uses
	// UNSIGNED-PAYLOAD, matching real S3-compatible endpoints.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		// Path format for path-style addressing: /{bucket}/{key}
		// Strip leading slash and bucket prefix to get the object key.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if idx := strings.Index(path, "/"); idx >= 0 {
			path = path[idx+1:]
		}
		fs.mu.Lock()
		fs.puts[path] = body
		fs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	testHTTPClient = srv.Client()
	t.Cleanup(func() {
		testHTTPClient = nil
		srv.Close()
	})
	return srv, fs
}

// seedS3Creds writes S3 credentials into config_store for tests.
func seedS3Creds(t *testing.T, ctx context.Context, pool *pgxpool.Pool, encKey []byte, endpointURL string) {
	t.Helper()
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "endpoint_url", endpointURL, false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "bucket", "test-bucket", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "region", "us-east-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "access_key_id", "testkey", true, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "secret_access_key", "testsecret", true, nil, encKey))
}

func TestUploadObjectSuccess(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, fs := newFakeS3Server(t)
	seedS3Creds(t, ctx, pool, encKey, srv.URL)

	content := "fake video data"
	err := UploadObject(ctx, pool, encKey, "recordings/meeting/video.mp4", strings.NewReader(content), int64(len(content)))
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	assert.Equal(t, []byte(content), fs.puts["recordings/meeting/video.mp4"])
}

func TestUploadObjectDefaultsRegionToUsEast1(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, _ := newFakeS3Server(t)
	// Seed without a region to exercise the default.
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "endpoint_url", srv.URL, false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "bucket", "test-bucket", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "region", "", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "access_key_id", "testkey", true, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "s3", "secret_access_key", "testsecret", true, nil, encKey))

	// Should not error — region defaults to us-east-1.
	err := UploadObject(ctx, pool, encKey, "test/file.mp4", strings.NewReader("x"), 1)
	require.NoError(t, err)
}
