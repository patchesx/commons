package s3storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/platform"
)

// newFakeDownloadServer returns a server that serves the given content for any GET request.
func newFakeDownloadServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// plainStreamer returns a StreamerFunc that fetches URLs without authentication.
func plainStreamer() platform.StreamerFunc {
	return func(ctx context.Context, downloadURL, _ string) (io.ReadCloser, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
}

func TestBackupBuildsDateKeyPrefix(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	srv, _ := newFakeS3Server(t)
	seedS3Creds(t, ctx, pool, encKey, srv.URL)

	meetingDate := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	prefix, err := Backup(ctx, pool, encKey, "recordings/dsa", nil, "", "General Meeting", meetingDate, plainStreamer())
	require.NoError(t, err)

	assert.Contains(t, prefix, "2024-06-15")
	assert.Contains(t, prefix, "General Meeting")
}

func TestBackupUploadsAllFiles(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	dlSrv := newFakeDownloadServer(t, "abc")
	s3Srv, fs := newFakeS3Server(t)
	seedS3Creds(t, ctx, pool, encKey, s3Srv.URL)

	files := []platform.RecordingFile{
		{FileName: "recording.mp4", DownloadURL: dlSrv.URL + "/recording.mp4", FileSize: 3},
		{FileName: "transcript.vtt", DownloadURL: dlSrv.URL + "/transcript.vtt", FileSize: 3},
	}

	_, err := Backup(ctx, pool, encKey, "recordings", files, "", "Weekly Sync",
		time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), plainStreamer())
	require.NoError(t, err)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	var keys []string
	for k := range fs.puts {
		keys = append(keys, k)
	}
	assert.True(t, containsSuffix(keys, "recording.mp4"))
	assert.True(t, containsSuffix(keys, "transcript.vtt"))
}

func TestBackupEmptyPrefixErrors(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	_, err := Backup(ctx, pool, encKey, "", nil, "", "Topic", time.Now(), plainStreamer())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key prefix not provided")
}

func containsSuffix(ss []string, suffix string) bool {
	for _, s := range ss {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}
