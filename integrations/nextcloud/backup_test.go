package nextcloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestBackupCreatesDatedSubfolder(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	uploadSrv, fwd := newFakeWebDAVServer(t)
	seedCreds(t, ctx, pool, encKey, uploadSrv.URL)

	meetingDate := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	folderPath, err := Backup(ctx, pool, encKey, "Recordings/DSA", nil, "", "General Meeting", meetingDate, plainStreamer())
	require.NoError(t, err)

	assert.Contains(t, folderPath, "2024-06-15")
	assert.Contains(t, folderPath, "General Meeting")

	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	assert.NotEmpty(t, fwd.mkcols)
}

func TestBackupUploadsAllFiles(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	dlSrv := newFakeDownloadServer(t, "abc")
	uploadSrv, fwd := newFakeWebDAVServer(t)
	seedCreds(t, ctx, pool, encKey, uploadSrv.URL)

	files := []platform.RecordingFile{
		{FileName: "recording.mp4", DownloadURL: dlSrv.URL + "/recording.mp4", FileSize: 3},
		{FileName: "transcript.vtt", DownloadURL: dlSrv.URL + "/transcript.vtt", FileSize: 3},
	}

	_, err := Backup(ctx, pool, encKey, "Recordings", files, "", "Weekly Sync",
		time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), plainStreamer())
	require.NoError(t, err)

	fwd.mu.Lock()
	defer fwd.mu.Unlock()
	assert.Contains(t, fwd.puts, "recording.mp4")
	assert.Contains(t, fwd.puts, "transcript.vtt")
}

func TestBackupEmptyParentPathErrors(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	_, err := Backup(ctx, pool, encKey, "", nil, "", "Topic", time.Now(), plainStreamer())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent path not provided")
}
