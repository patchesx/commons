package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	slacklib "github.com/slack-go/slack"

	"commons/internal/testhelpers"
)

func TestIsRetryableSendError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rate limited with retry-after", &slacklib.RateLimitedError{RetryAfter: 5 * time.Second}, true},
		{"429 without retry-after", slacklib.StatusCodeError{Code: 429, Status: "429 Too Many Requests"}, true},
		{"500 server error", slacklib.StatusCodeError{Code: 500, Status: "500 Internal Server Error"}, true},
		{"404 not found", slacklib.StatusCodeError{Code: 404, Status: "404 Not Found"}, false},
		{"arbitrary error", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableSendError(tc.err); got != tc.want {
				t.Fatalf("isRetryableSendError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryBackoff(t *testing.T) {
	prevBase := retryBaseBackoff
	prevMax := retryMaxBackoff
	retryBaseBackoff = 10 * time.Second
	retryMaxBackoff = 100 * time.Second
	t.Cleanup(func() {
		retryBaseBackoff = prevBase
		retryMaxBackoff = prevMax
	})

	// Exponential: 10s, 20s, 40s, 80s, then capped at 100s.
	other := errors.New("some failure")
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{5, 100 * time.Second}, // capped
		{6, 100 * time.Second}, // still capped
	}
	for _, c := range cases {
		if got := retryBackoff(c.attempt, other); got != c.want {
			t.Fatalf("retryBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}

	// Retry-After overrides exponential backoff.
	rl := &slacklib.RateLimitedError{RetryAfter: 42 * time.Second}
	if got := retryBackoff(1, rl); got != 42*time.Second {
		t.Fatalf("retryBackoff with Retry-After = %v, want 42s", got)
	}
}

func TestDecodeBlocks(t *testing.T) {
	if got := decodeBlocks(nil); got != nil {
		t.Fatalf("decodeBlocks(nil) = %v, want nil", got)
	}
	if got := decodeBlocks([]byte{}); got != nil {
		t.Fatalf("decodeBlocks(empty) = %v, want nil", got)
	}
	if got := decodeBlocks([]byte("not json")); got != nil {
		t.Fatalf("decodeBlocks(invalid) = %v, want nil", got)
	}
	raw := []byte(`[{"type":"section","text":{"type":"plain_text","text":"hi"}}]`)
	got := decodeBlocks(raw)
	if len(got) != 1 {
		t.Fatalf("decodeBlocks(valid) returned %d blocks, want 1", len(got))
	}
	if got[0].BlockType() != slacklib.MBTSection {
		t.Fatalf("decodeBlocks(valid) block type = %v, want %v", got[0].BlockType(), slacklib.MBTSection)
	}
}

func TestRetryQueueStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Enqueue two messages: one due now, one deferred into the future.
	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_DUE", IsDM: false, Text: "due now", MaxAttempts: 5,
	}))
	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_LATER", IsDM: true, Text: "later",
		Blocks: []byte(`[{"type":"divider"}]`), MaxAttempts: 5,
	}))
	_, err := pool.Exec(ctx, `UPDATE slack.retry_queue SET next_attempt_at = NOW() + INTERVAL '1 hour' WHERE destination = 'C_LATER'`)
	require.NoError(t, err)

	due, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "C_DUE", due[0].Destination)
	assert.False(t, due[0].IsDM)

	// Mark failed -> attempt_count bumps, next_attempt_at moves into the future.
	require.NoError(t, MarkRetryFailed(ctx, pool, due[0].ID, time.Now().Add(30*time.Second), "rate limited"))
	due2, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	assert.Empty(t, due2, "message should not be due after MarkRetryFailed")

	// Delete -> row gone.
	require.NoError(t, DeleteRetryMessage(ctx, pool, due[0].ID))
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM slack.retry_queue WHERE destination = 'C_DUE'`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestRetryOneSuccess(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_RETRY", IsDM: false, Text: "hello retry", MaxAttempts: 5,
	}))
	due, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)

	retryOne(ctx, pool, client, due[0])

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.posts, 1)
	assert.Equal(t, "C_RETRY", fake.posts[0].Channel)
	assert.Equal(t, "hello retry", fake.posts[0].Text)

	// Row should be deleted on success.
	due2, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	assert.Empty(t, due2)
}

func TestRetryOneFailureBumpsAttempt(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Fake server that returns 429 with Retry-After on chat.postMessage.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := newTestSlackClient(srv.URL)

	prev := sendInterval
	sendInterval = 0
	t.Cleanup(func() { sendInterval = prev })

	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_FAIL", IsDM: false, Text: "will fail", MaxAttempts: 5,
	}))
	due, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)

	retryOne(ctx, pool, client, due[0])

	// Row should still exist with attempt_count=1 and next_attempt_at in the future.
	var (
		attemptCount  int
		nextAttemptAt time.Time
	)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempt_count, next_attempt_at FROM slack.retry_queue WHERE destination = 'C_FAIL'`,
	).Scan(&attemptCount, &nextAttemptAt))
	assert.Equal(t, 1, attemptCount)
	assert.True(t, nextAttemptAt.After(time.Now()), "next_attempt_at should be in the future")
}
