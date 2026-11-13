package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiterAllow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := &RateLimiter{buckets: map[string]*rateBucket{}, now: func() time.Time { return now }}

	for i := 1; i <= 3; i++ {
		assert.True(t, l.Allow("ip:1.2.3.4", 3, time.Hour), "attempt %d", i)
	}
	assert.False(t, l.Allow("ip:1.2.3.4", 3, time.Hour), "fourth attempt in the window is refused")
	assert.True(t, l.Allow("ip:5.6.7.8", 3, time.Hour), "other keys have their own count")
	assert.True(t, l.Allow("code:ABC234", 3, time.Hour), "and kinds of key do not share buckets")

	now = now.Add(30 * time.Minute)
	assert.False(t, l.Allow("ip:1.2.3.4", 3, time.Hour), "still inside the window")
	now = now.Add(31 * time.Minute)
	assert.True(t, l.Allow("ip:1.2.3.4", 3, time.Hour), "a new window starts after it expires")
}

func TestRateLimiterLimitsAreSetPerCall(t *testing.T) {
	now := time.Now()
	l := &RateLimiter{buckets: map[string]*rateBucket{}, now: func() time.Time { return now }}

	assert.True(t, l.Allow("k", 1, time.Hour))
	assert.False(t, l.Allow("k", 1, time.Hour))
	assert.False(t, (&RateLimiter{buckets: map[string]*rateBucket{}, now: time.Now}).Allow("z", 0, time.Hour), "a limit of zero allows nothing")
}

func TestRateLimiterPrune(t *testing.T) {
	now := time.Now()
	l := &RateLimiter{buckets: map[string]*rateBucket{}, now: func() time.Time { return now }}
	l.Allow("old", 5, time.Minute)
	l.Allow("fresh", 5, time.Hour)

	now = now.Add(2 * time.Minute)
	l.prune()
	assert.NotContains(t, l.buckets, "old")
	assert.Contains(t, l.buckets, "fresh")
}
