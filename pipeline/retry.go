package pipeline

import (
	"context"
	"fmt"
	"log"
	"time"

	"commons/plugin"
	"commons/store"
)

// executeWithRetry wraps action execution with retry and timeout.
// If cfg is nil or MaxAttempts <= 1, executes once (no retry).
// PauseSignal is propagated immediately without retry.
// Retry delays use time.After (in-memory); on crash, the run resumes from
// current_step and the first attempt re-runs (acceptable for v1).
func executeWithRetry(ctx context.Context, at plugin.ActionType, params map[string]any, ac plugin.ActionContext, cfg *store.RetryConfig, timeoutSeconds *int) (map[string]any, error) {
	attempts := 1
	if cfg != nil && cfg.MaxAttempts > 1 {
		attempts = cfg.MaxAttempts
	}

	var timeout time.Duration
	if timeoutSeconds != nil && *timeoutSeconds > 0 {
		timeout = time.Duration(*timeoutSeconds) * time.Second
	}

	// Warn if retry is configured for a non-idempotent action.
	if attempts > 1 && !plugin.IsIdempotent(at) {
		log.Printf("pipeline: warning — retry configured for non-idempotent action %s", at.ID())
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		execCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			execCtx, cancel = context.WithTimeout(ctx, timeout)
		}

		output, err := at.Execute(execCtx, params, ac)
		if cancel != nil {
			cancel()
		}

		if err == nil {
			return output, nil
		}

		// Propagate PauseSignal without retry.
		if _, ok := err.(plugin.PauseSignal); ok {
			return nil, err
		}

		lastErr = err
		log.Printf("pipeline: action=%s attempt %d/%d failed: %v", at.ID(), attempt, attempts, err)

		if attempt < attempts {
			delay := computeBackoff(cfg, attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, fmt.Errorf("action %s failed after %d attempts: %w", at.ID(), attempts, lastErr)
}

// computeBackoff returns the delay before the next retry attempt.
func computeBackoff(cfg *store.RetryConfig, attempt int) time.Duration {
	if cfg == nil {
		return 0
	}
	initial, _ := time.ParseDuration(cfg.InitialDelay)
	if initial == 0 {
		initial = 5 * time.Second
	}
	var delay time.Duration
	switch cfg.Backoff {
	case "exponential":
		delay = initial * time.Duration(1<<(attempt-1)) // 5s, 10s, 20s, 40s...
	default: // "fixed"
		delay = initial
	}
	if cfg.MaxDelay != "" {
		if maxD, err := time.ParseDuration(cfg.MaxDelay); err == nil && maxD > 0 && delay > maxD {
			delay = maxD
		}
	}
	return delay
}
