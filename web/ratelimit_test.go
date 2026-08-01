package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoginLimiterAllowsUpToMax(t *testing.T) {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}

	for i := 0; i < maxLoginAttempts; i++ {
		assert.True(t, l.Allow("1.2.3.4"), "attempt %d should be allowed", i+1)
	}
}

func TestLoginLimiterBlocksOnExceed(t *testing.T) {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}

	for i := 0; i < maxLoginAttempts; i++ {
		l.Allow("1.2.3.4")
	}
	assert.False(t, l.Allow("1.2.3.4"), "6th attempt should be blocked")
}

func TestLoginLimiterDifferentIPsAreIndependent(t *testing.T) {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}

	for i := 0; i < maxLoginAttempts+1; i++ {
		l.Allow("10.0.0.1")
	}
	// Different IP should still be allowed.
	assert.True(t, l.Allow("10.0.0.2"))
}

func TestLoginLimiterResetClearsBucket(t *testing.T) {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}

	for i := 0; i < maxLoginAttempts; i++ {
		l.Allow("5.5.5.5")
	}
	l.reset("5.5.5.5")
	// After reset, first attempt starts a fresh window.
	assert.True(t, l.Allow("5.5.5.5"))
}

func TestLimitLoginMiddleware429OnExceed(t *testing.T) {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}

	// Inner handler simulates a failed login (302 redirect to /login?error=1).
	// Must NOT return 303 — that would trigger a reset and prevent accumulation.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	})
	handler := l.LimitLogin(inner)

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "9.9.9.9:1234"

	// Exhaust the limit with failed attempts.
	for i := 0; i < maxLoginAttempts; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusFound, w.Code)
	}

	// Next request should be blocked.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestLimitLoginMiddlewareResetsOnSuccess(t *testing.T) {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}

	// Inner handler simulates a successful login (303 redirect).
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSeeOther)
	})
	handler := l.LimitLogin(inner)

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "8.8.8.8:5678"

	// Make maxLoginAttempts-1 attempts (all succeed with 303).
	for i := 0; i < maxLoginAttempts-1; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// One final successful login — should reset the counter.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)

	// After reset, should be allowed again.
	assert.True(t, l.Allow("8.8.8.8"))
}
