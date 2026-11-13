package web

import (
	"sync"
	"time"
)

type rateBucket struct {
	count     int
	windowEnd time.Time
}

// RateLimiter is a small in-memory fixed-window limiter keyed by any string (an IP address, an item code, ...).
// Each call to Allow names its own limit and window, so one limiter can serve several kinds of key.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	now     func() time.Time
}

// NewRateLimiter returns a limiter and starts a background goroutine that drops expired buckets.
func NewRateLimiter() *RateLimiter {
	l := &RateLimiter{buckets: make(map[string]*rateBucket), now: time.Now}
	go func() {
		for range time.Tick(10 * time.Minute) {
			l.prune()
		}
	}()
	return l
}

func (l *RateLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if now.After(b.windowEnd) {
			delete(l.buckets, k)
		}
	}
}

// Allow counts one attempt for key and reports whether it is within limit attempts per window.
// Attempts over the limit still count, but the window is not extended.
func (l *RateLimiter) Allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.windowEnd) {
		l.buckets[key] = &rateBucket{count: 1, windowEnd: now.Add(window)}
		return limit >= 1
	}
	b.count++
	return b.count <= limit
}
