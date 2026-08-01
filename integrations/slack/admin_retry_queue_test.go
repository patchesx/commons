package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestListAllRetryMessagesIncludesExhausted(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Enqueue a message and exhaust it (attempt_count >= max_attempts).
	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_DEAD", IsDM: false, Text: "gave up", MaxAttempts: 2,
	}))
	_, err := pool.Exec(ctx, `UPDATE slack.retry_queue SET attempt_count = 2, next_attempt_at = NOW() + INTERVAL '1 hour' WHERE destination = 'C_DEAD'`)
	require.NoError(t, err)

	// ListDue excludes exhausted rows; ListAll includes them.
	due, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	assert.Empty(t, due)

	all, err := ListAllRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "C_DEAD", all[0].Destination)
	assert.Equal(t, 2, all[0].AttemptCount)
	assert.True(t, all[0].AttemptCount >= all[0].MaxAttempts)
}

func TestResetRetryMessage(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_RESET", IsDM: true, Text: "retry me", MaxAttempts: 3,
	}))
	_, err := pool.Exec(ctx, `UPDATE slack.retry_queue SET attempt_count = 3, next_attempt_at = NOW() + INTERVAL '1 hour', last_error = 'boom' WHERE destination = 'C_RESET'`)
	require.NoError(t, err)

	var id string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM slack.retry_queue WHERE destination = 'C_RESET'`).Scan(&id))

	require.NoError(t, ResetRetryMessage(ctx, pool, id))

	var (
		attemptCount  int
		nextAttemptAt time.Time
		lastError     *string
	)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempt_count, next_attempt_at, last_error FROM slack.retry_queue WHERE id = $1`, id,
	).Scan(&attemptCount, &nextAttemptAt, &lastError))
	assert.Equal(t, 0, attemptCount)
	assert.True(t, nextAttemptAt.Before(time.Now().Add(time.Minute)), "next_attempt_at should be ~now")
	assert.Nil(t, lastError)

	// It should now be eligible for the drainer again.
	due, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "C_RESET", due[0].Destination)
}

func TestHandleRetryQueuePage(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "U_PAGE", IsDM: true, Text: "page test message", MaxAttempts: 5,
	}))

	req := httptest.NewRequest(http.MethodGet, "/admin/slack/retry-queue", nil)
	rec := httptest.NewRecorder()
	HandleRetryQueuePage(pool).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Slack Retry Queue")
	assert.Contains(t, body, "U_PAGE")
	assert.Contains(t, body, "page test message")
}

func TestHandleRetryQueueRetry(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_RETRYBTN", IsDM: false, Text: "retry via ui", MaxAttempts: 2,
	}))
	// Exhaust it so it's not due.
	_, err := pool.Exec(ctx, `UPDATE slack.retry_queue SET attempt_count = 2, next_attempt_at = NOW() + INTERVAL '1 hour' WHERE destination = 'C_RETRYBTN'`)
	require.NoError(t, err)
	var id string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM slack.retry_queue WHERE destination = 'C_RETRYBTN'`).Scan(&id))

	req := httptest.NewRequest(http.MethodPost, "/admin/slack/retry-queue/"+id+"/retry", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	HandleRetryQueueRetry(pool).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	// The message should now be reset and due.
	due, err := ListDueRetryMessages(ctx, pool, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "C_RETRYBTN", due[0].Destination)
	assert.Equal(t, 0, due[0].AttemptCount)
}

func TestHandleRetryQueueDelete(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	require.NoError(t, EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: "C_DELBTN", IsDM: false, Text: "delete me", MaxAttempts: 5,
	}))
	var id string
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM slack.retry_queue WHERE destination = 'C_DELBTN'`).Scan(&id))

	req := httptest.NewRequest(http.MethodDelete, "/admin/slack/retry-queue/"+id, nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	HandleRetryQueueDelete(pool).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM slack.retry_queue WHERE id = $1`, id).Scan(&n))
	assert.Equal(t, 0, n)
}
