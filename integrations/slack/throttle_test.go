package slack

import (
	"context"
	"errors"
	"testing"
	"time"

	slacklib "github.com/slack-go/slack"
)

// resetThrottle saves the throttle globals, resets them, and registers cleanup
// so tests stay independent.
func resetThrottle(t *testing.T, interval time.Duration) {
	t.Helper()
	prevInterval := sendInterval
	prevDefault := defaultRetryAfter
	prevThrottles := throttles
	sendInterval = interval
	defaultRetryAfter = interval // keep the default backoff small in tests too
	throttles = map[string]*sendThrottle{}
	t.Cleanup(func() {
		sendInterval = prevInterval
		defaultRetryAfter = prevDefault
		throttles = prevThrottles
	})
}

// TestThrottleSlackSendSpacesCallsApart verifies that two consecutive sends to
// the same destination are spaced at least sendInterval apart.
func TestThrottleSlackSendSpacesCallsApart(t *testing.T) {
	const interval = 60 * time.Millisecond
	resetThrottle(t, interval)

	start := time.Now()
	if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
		t.Fatalf("first throttleSlackSend: %v", err)
	}
	if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
		t.Fatalf("second throttleSlackSend: %v", err)
	}
	elapsed := time.Since(start)

	// The second call must wait ~interval; allow a small tolerance for timers.
	if elapsed < interval-10*time.Millisecond {
		t.Fatalf("expected >= %v between sends, got %v", interval-10*time.Millisecond, elapsed)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("throttle waited too long: %v", elapsed)
	}
}

// TestThrottleSlackSendPerChannelIndependent verifies that sends to different
// destinations are not throttled against each other: two posts to different
// channels both return immediately, while a follow-up to the first channel
// still waits the full interval.
func TestThrottleSlackSendPerChannelIndependent(t *testing.T) {
	const interval = 80 * time.Millisecond
	resetThrottle(t, interval)

	start := time.Now()
	if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
		t.Fatalf("send to chan-A: %v", err)
	}
	if err := throttleSlackSend(context.Background(), "chan-B"); err != nil {
		t.Fatalf("send to chan-B: %v", err)
	}
	elapsedAB := time.Since(start)

	// Different channels => no cross-throttling; both should be near-instant.
	if elapsedAB > 20*time.Millisecond {
		t.Fatalf("expected independent channels to be fast, took %v", elapsedAB)
	}

	if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
		t.Fatalf("second send to chan-A: %v", err)
	}
	elapsedA2 := time.Since(start)

	// Same channel as the first send => must wait ~interval from it.
	if elapsedA2 < interval-10*time.Millisecond {
		t.Fatalf("expected same-channel spacing >= %v, got %v", interval-10*time.Millisecond, elapsedA2)
	}
}

// TestThrottleSlackSendDisabledWhenIntervalZero verifies that a zero interval
// disables throttling so calls return immediately.
func TestThrottleSlackSendDisabledWhenIntervalZero(t *testing.T) {
	resetThrottle(t, 0)

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
			t.Fatalf("throttleSlackSend %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	if elapsed > 20*time.Millisecond {
		t.Fatalf("expected no throttling with zero interval, took %v", elapsed)
	}
}

// TestThrottleSlackSendRespectsContextCancellation verifies that a cancelled
// context aborts the wait and returns ctx.Err() without updating lastTime.
func TestThrottleSlackSendRespectsContextCancellation(t *testing.T) {
	resetThrottle(t, 1*time.Second)

	th := throttleFor("chan-A")
	// First call sets lastTime so the next must wait the full interval.
	if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
		t.Fatalf("first throttleSlackSend: %v", err)
	}
	lastBefore := th.lastTime

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := throttleSlackSend(ctx, "chan-A")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected quick cancellation, took %v", elapsed)
	}
	// A cancelled send must not count as a send.
	if th.lastTime != lastBefore {
		t.Fatalf("lastTime changed after cancelled send")
	}
}

// TestRecordRateLimitIfAnyRateLimitedError verifies that a *RateLimitedError
// records the Retry-After backoff for the destination and passes the error
// through unchanged.
func TestRecordRateLimitIfAnyRateLimitedError(t *testing.T) {
	resetThrottle(t, 50*time.Millisecond)

	original := &slacklib.RateLimitedError{RetryAfter: 200 * time.Millisecond}
	before := time.Now()
	returned := recordRateLimitIfAny("chan-A", original)

	if !errors.Is(returned, original) {
		t.Fatal("expected original error returned unchanged")
	}
	got := throttleFor("chan-A").rateLimitedUntil
	if got.Before(before.Add(200 * time.Millisecond)) {
		t.Fatalf("rateLimitedUntil %v should be >= %v", got, before.Add(200*time.Millisecond))
	}
}

// TestRecordRateLimitIfAnyStatusCode429 verifies that a 429 without a
// Retry-After header records the default backoff.
func TestRecordRateLimitIfAnyStatusCode429(t *testing.T) {
	resetThrottle(t, 50*time.Millisecond)

	original := slacklib.StatusCodeError{Code: 429, Status: "429 Too Many Requests"}
	before := time.Now()
	returned := recordRateLimitIfAny("chan-A", original)

	if !errors.Is(returned, original) {
		t.Fatal("expected original error returned unchanged")
	}
	got := throttleFor("chan-A").rateLimitedUntil
	if got.Before(before.Add(50 * time.Millisecond)) {
		t.Fatalf("rateLimitedUntil %v should be >= default backoff", got)
	}
}

// TestRecordRateLimitIfAnyNoOpForNonRateLimitErrors verifies that nil, non-429
// errors, and arbitrary errors do not record a backoff.
func TestRecordRateLimitIfAnyNoOpForNonRateLimitErrors(t *testing.T) {
	resetThrottle(t, 50*time.Millisecond)

	// nil: no-op, returns nil.
	if err := recordRateLimitIfAny("chan-A", nil); err != nil {
		t.Fatalf("expected nil for nil input, got %v", err)
	}
	// Non-429 status error: no backoff recorded.
	sce := slacklib.StatusCodeError{Code: 500, Status: "500 Internal Server Error"}
	if returned := recordRateLimitIfAny("chan-A", sce); !errors.Is(returned, sce) {
		t.Fatal("expected original error returned unchanged")
	}
	// Arbitrary error: no backoff recorded.
	other := errors.New("boom")
	if returned := recordRateLimitIfAny("chan-A", other); !errors.Is(returned, other) {
		t.Fatal("expected original error returned unchanged")
	}
	if got := throttleFor("chan-A").rateLimitedUntil; !got.IsZero() {
		t.Fatalf("expected no backoff recorded, got rateLimitedUntil %v", got)
	}
}

// TestThrottleSlackSendHonorsRateLimitBackoff verifies that after a rate limit
// is recorded, the next send waits for the backoff rather than just sendInterval.
func TestThrottleSlackSendHonorsRateLimitBackoff(t *testing.T) {
	resetThrottle(t, 50*time.Millisecond)

	// Record a backoff longer than sendInterval.
	markRateLimited("chan-A", 200*time.Millisecond)

	start := time.Now()
	if err := throttleSlackSend(context.Background(), "chan-A"); err != nil {
		t.Fatalf("throttleSlackSend: %v", err)
	}
	elapsed := time.Since(start)

	// Must wait ~200ms (the backoff), not 50ms (the spacing).
	if elapsed < 190*time.Millisecond {
		t.Fatalf("expected backoff of ~200ms, waited only %v", elapsed)
	}
	if elapsed > 600*time.Millisecond {
		t.Fatalf("waited too long: %v", elapsed)
	}
}

// TestThrottleSlackSendBackoffDoesNotBlockOtherChannels verifies that a
// rate-limit backoff on one destination does not delay sends to another.
func TestThrottleSlackSendBackoffDoesNotBlockOtherChannels(t *testing.T) {
	resetThrottle(t, 50*time.Millisecond)

	// chan-A is backed off for a long time; chan-B should be unaffected.
	markRateLimited("chan-A", 500*time.Millisecond)

	start := time.Now()
	if err := throttleSlackSend(context.Background(), "chan-B"); err != nil {
		t.Fatalf("send to chan-B: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 20*time.Millisecond {
		t.Fatalf("chan-B should not be delayed by chan-A's backoff, took %v", elapsed)
	}
}
