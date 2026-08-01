package zoom

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

// --- verifySignature tests ---

func makeZoomSignature(secret, timestamp, body string) string {
	message := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignatureValid(t *testing.T) {
	secret := "test-secret"
	timestamp := "1234567890"
	body := `{"event":"recording.completed"}`
	sig := makeZoomSignature(secret, timestamp, body)
	assert.True(t, verifySignature(secret, timestamp, body, sig))
}

func TestVerifySignatureTamperedBody(t *testing.T) {
	secret := "test-secret"
	timestamp := "1234567890"
	body := `{"event":"recording.completed"}`
	sig := makeZoomSignature(secret, timestamp, body)
	assert.False(t, verifySignature(secret, timestamp, `{"event":"tampered"}`, sig))
}

func TestVerifySignatureWrongSecret(t *testing.T) {
	timestamp := "1234567890"
	body := `{"event":"recording.completed"}`
	sig := makeZoomSignature("correct-secret", timestamp, body)
	assert.False(t, verifySignature("wrong-secret", timestamp, body, sig))
}

func TestVerifySignatureEmptyTimestamp(t *testing.T) {
	assert.False(t, verifySignature("secret", "", "body", "v0=abc"))
}

func TestVerifySignatureEmptySigHeader(t *testing.T) {
	assert.False(t, verifySignature("secret", "1234567890", "body", ""))
}

func TestVerifySignatureGarbageSig(t *testing.T) {
	secret := "test-secret"
	timestamp := "1234567890"
	body := `{"event":"recording.completed"}`
	assert.False(t, verifySignature(secret, timestamp, body, "v0=deadbeef"))
}

// --- Handler integration tests ---

const testSecret = "test-webhook-secret"

func freshTS() string { return strconv.FormatInt(time.Now().Unix(), 10) }
func staleTS() string { return strconv.FormatInt(time.Now().Unix()-400, 10) }

func seedIntegration(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `INSERT INTO integrations (type, name) VALUES ('zoom', 'Test Zoom') RETURNING id`).Scan(&id)
	require.NoError(t, err)
	return id
}

func seedSecret(t *testing.T, ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	t.Helper()
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "webhook_secret", testSecret, false, nil, encKey))
}

// signedRequest builds a POST with x-zm-request-timestamp and x-zm-signature headers.
func signedRequest(body, timestamp, secret string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/zoom/webhook", strings.NewReader(body))
	req.Header.Set("x-zm-request-timestamp", timestamp)
	req.Header.Set("x-zm-signature", makeZoomSignature(secret, timestamp, body))
	return req
}

func TestHandlerInvalidJSON(t *testing.T) {
	// Fails before touching the DB — pool can be nil.
	h := Handler(nil, nil, "", func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json")))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlerMissingSecret(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	// webhook_secret intentionally not seeded.

	body := `{"event":"recording.completed","payload":{"object":{}}}`
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(body, freshTS(), testSecret))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestHandlerURLValidation(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	body := `{"event":"endpoint.url_validation","payload":{"plainToken":"testtoken123"}}`
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {})
	w := httptest.NewRecorder()
	// URL validation has no timestamp or signature.
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))

	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		PlainToken     string `json:"plainToken"`
		EncryptedToken string `json:"encryptedToken"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "testtoken123", resp.PlainToken)

	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte("testtoken123"))
	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), resp.EncryptedToken)
}

func TestHandlerStaleTimestamp(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	body := `{"event":"recording.completed","payload":{"object":{}}}`
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(body, staleTS(), testSecret))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandlerInvalidSignature(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	body := `{"event":"recording.completed","payload":{"object":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("x-zm-request-timestamp", freshTS())
	req.Header.Set("x-zm-signature", "v0=deadbeef")

	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandlerNonRecordingEvent(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	body := `{"event":"meeting.started","payload":{"object":{}}}`
	processorCalled := false
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {
		processorCalled = true
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(body, freshTS(), testSecret))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, processorCalled)
}

func TestHandlerDuplicateUUID(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	// Pre-seed a recording_data row so the dedup check fires.
	job := &store.Job{
		Type:    store.JobTypeRecordingUpload,
		Feature: store.JobFeatureRecordingPipeline,
		Trigger: store.JobTriggerWebhook,
		Status:  store.JobStatusPending,
	}
	require.NoError(t, store.CreateJob(ctx, pool, job))
	require.NoError(t, CreateRecordingData(ctx, pool, &RecordingData{
		JobID:         job.ID,
		IntegrationID: intID,
		MeetingUUID:   "dup-uuid-abc",
		MeetingTopic:  "Existing Meeting",
		MeetingDate:   time.Now(),
	}))

	body := `{"event":"recording.completed","payload":{"object":{"uuid":"dup-uuid-abc"}}}`
	processorCalled := false
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {
		processorCalled = true
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(body, freshTS(), testSecret))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, processorCalled, "processor must not be called for a duplicate UUID")
}

func TestHandlerNoMP4(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	body := `{"event":"recording.completed","payload":{"object":{
		"uuid":"no-mp4-uuid",
		"recording_files":[{"recording_type":"audio_only","file_type":"M4A","status":"completed","file_size":1000}]
	}}}`
	processorCalled := false
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, _ RecordingJob) {
		processorCalled = true
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(body, freshTS(), testSecret))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, processorCalled)
}

func TestHandlerHappyPath(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	intID := seedIntegration(t, ctx, pool)
	seedSecret(t, ctx, pool, encKey)

	body := `{
		"event":"recording.completed",
		"download_token":"dl-token",
		"payload":{"object":{
			"uuid":"happy-path-uuid",
			"id":99999,
			"topic":"Test Meeting",
			"start_time":"2024-03-15T10:00:00Z",
			"duration":60,
			"host_email":"host@example.com",
			"recording_files":[{
				"recording_type":"shared_screen_with_speaker_view",
				"file_type":"MP4",
				"download_url":"https://zoom.us/rec/download/test.mp4",
				"file_size":123456789,
				"status":"completed"
			}]
		}}
	}`

	done := make(chan RecordingJob, 1)
	h := Handler(pool, encKey, intID, func(_ context.Context, _ *pgxpool.Pool, rj RecordingJob) {
		done <- rj
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, signedRequest(body, freshTS(), testSecret))
	require.Equal(t, http.StatusOK, w.Code)

	select {
	case rj := <-done:
		assert.Equal(t, "Test Meeting", rj.Recording.MeetingTopic)
		assert.Equal(t, "happy-path-uuid", rj.Recording.MeetingUUID)
		assert.Equal(t, "dl-token", rj.DownloadToken)
		assert.Equal(t, intID, rj.Recording.IntegrationID)

		rd, err := GetByMeetingUUID(ctx, pool, "happy-path-uuid")
		require.NoError(t, err)
		require.NotNil(t, rd, "recording_data row should be persisted")
		assert.Equal(t, intID, rd.IntegrationID)
	case <-time.After(3 * time.Second):
		t.Fatal("processor was not called within timeout")
	}
}
