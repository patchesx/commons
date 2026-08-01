package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	maxLoginAttempts = 5
	loginWindow      = 15 * time.Minute
)

type loginAttempts struct {
	count     int
	windowEnd time.Time
}

// statusResponseWriter wraps http.ResponseWriter to capture the status code for login result detection.
type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *statusResponseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// LoginLimiter is a simple in-memory per-IP fixed-window rate limiter for the login endpoint.
type LoginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginAttempts
}

// NewLoginLimiter returns a new LoginLimiter and starts a background pruning goroutine.
func NewLoginLimiter() *LoginLimiter {
	l := &LoginLimiter{buckets: make(map[string]*loginAttempts)}
	go l.prune()
	return l
}

// prune removes expired buckets every loginWindow to prevent unbounded memory growth.
func (l *LoginLimiter) prune() {
	for range time.Tick(loginWindow) {
		l.mu.Lock()
		now := time.Now()
		for ip, b := range l.buckets {
			if now.After(b.windowEnd) {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}

// Allow returns true if the IP is within its rate limit window, false if the limit is exceeded.
func (l *LoginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok || now.After(b.windowEnd) {
		l.buckets[ip] = &loginAttempts{count: 1, windowEnd: now.Add(loginWindow)}
		return true
	}
	b.count++
	return b.count <= maxLoginAttempts
}

// reset clears the bucket for an IP, used after a successful login so the window
// doesn't count against the user on their next visit.
func (l *LoginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, ip)
}

// LimitLogin wraps a handler and returns HTTP 429 when the per-IP limit is exceeded.
// On a successful login (303 redirect to the admin area), the counter is reset.
func (l *LoginLimiter) LimitLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if !l.Allow(host) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		srw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(srw, r)
		if srw.status == http.StatusSeeOther {
			l.reset(host)
		}
	})
}
