package slack

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
)

// Retry queue tuning. Package vars so tests can speed them up.
var (
	defaultMaxAttempts = 5
	retryPollInterval  = 5 * time.Second
	retryBatchSize     = 20
	retryBaseBackoff   = 30 * time.Second
	retryMaxBackoff    = 10 * time.Minute
)

// StartRetryDrainer launches a background goroutine that periodically retries
// messages in the Slack retry queue. Messages are persisted, so a process
// restart resumes retrying from the DB rather than losing them. Runs for the
// lifetime of the process.
func StartRetryDrainer(pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(retryPollInterval)
		defer ticker.Stop()
		for {
			drainRetryQueue(context.Background(), pool)
			<-ticker.C
		}
	}()
}

// drainRetryQueue claims due messages and attempts delivery.
func drainRetryQueue(ctx context.Context, pool *pgxpool.Pool) {
	client := getClient(ctx)
	if client == nil {
		return
	}
	msgs, err := ListDueRetryMessages(ctx, pool, retryBatchSize)
	if err != nil {
		log.Printf("slack/retry: list due messages: %v", err)
		return
	}
	for _, m := range msgs {
		retryOne(ctx, pool, client, m)
	}
}

// retryOne attempts to deliver a single queued message. On success the row is
// deleted; on failure the attempt count is bumped and the next attempt is
// scheduled with backoff. It calls the unexported send helpers directly so a
// failed retry updates the existing row rather than enqueuing a duplicate.
func retryOne(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, m RetryMessage) {
	var err error
	if m.IsDM {
		_, _, err = sendDM(ctx, client, m.Destination, m.Text, decodeBlocks(m.Blocks)...)
	} else {
		err = postChannelMessage(ctx, client, m.Destination, m.Text)
	}
	if err == nil {
		if derr := DeleteRetryMessage(ctx, pool, m.ID); derr != nil {
			log.Printf("slack/retry: delete message %s: %v", m.ID, derr)
		}
		return
	}
	nextAt := time.Now().Add(retryBackoff(m.AttemptCount+1, err))
	if merr := MarkRetryFailed(ctx, pool, m.ID, nextAt, err.Error()); merr != nil {
		log.Printf("slack/retry: mark failed message %s: %v", m.ID, merr)
	}
}

// retryBackoff returns the delay before the next attempt of a failed message.
// It honors Slack's Retry-After when present; otherwise exponential backoff
// (30s, 60s, 120s, 240s, 480s), capped at retryMaxBackoff.
func retryBackoff(attempt int, err error) time.Duration {
	var rl *slacklib.RateLimitedError
	if errors.As(err, &rl) && rl.RetryAfter > 0 {
		return rl.RetryAfter
	}
	backoff := retryBaseBackoff << (attempt - 1)
	if backoff > retryMaxBackoff || backoff <= 0 {
		backoff = retryMaxBackoff
	}
	return backoff
}

// decodeBlocks parses stored Block Kit JSON back into blocks. Returns nil on
// error or when empty, so the message is retried as plain text as a fallback.
func decodeBlocks(raw []byte) []slacklib.Block {
	if len(raw) == 0 {
		return nil
	}
	var b slacklib.Blocks
	if err := json.Unmarshal(raw, &b); err != nil {
		log.Printf("slack/retry: decode blocks: %v", err)
		return nil
	}
	return b.BlockSet
}

// isRetryableSendError reports whether err represents a transient Slack failure
// (rate limit / 429, or a 5xx server error) that warrants retrying later.
func isRetryableSendError(err error) bool {
	if err == nil {
		return false
	}
	var rl *slacklib.RateLimitedError
	if errors.As(err, &rl) {
		return true
	}
	var sce slacklib.StatusCodeError
	if errors.As(err, &sce) && sce.Retryable() {
		return true
	}
	return false
}

// enqueueRetryableIfFailed persists a message for later retry if err is a
// transient Slack failure. Returns err unchanged. Blocks (if any) are
// JSON-encoded for storage.
func enqueueRetryableIfFailed(ctx context.Context, pool *pgxpool.Pool, destination string, isDM bool, text string, blocks []slacklib.Block, err error) error {
	if pool == nil || !isRetryableSendError(err) {
		return err
	}
	var blocksJSON []byte
	if len(blocks) > 0 {
		if b, merr := json.Marshal(blocks); merr == nil {
			blocksJSON = b
		}
	}
	if qerr := EnqueueRetryMessage(ctx, pool, &RetryMessage{
		Destination: destination,
		IsDM:        isDM,
		Text:        text,
		Blocks:      blocksJSON,
		MaxAttempts: defaultMaxAttempts,
	}); qerr != nil {
		log.Printf("slack: failed to enqueue retry for %s: %v", destination, qerr)
	}
	return err
}
