package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "session"
	sessionDuration   = 24 * time.Hour
)

// secureCookies controls the Secure flag on session cookies.
// Default true (production HTTPS). Set to false via SetSecureCookies for local HTTP dev.
var secureCookies = true

// SetSecureCookies configures whether session cookies require HTTPS.
// Call this once at startup before serving requests.
func SetSecureCookies(v bool) { secureCookies = v }

type sessionPayload struct {
	UserID string `json:"uid"`
	Exp    int64  `json:"exp"`
}

type contextKey int

const (
	userIDKey     contextKey = iota // 0 — do not change this value
	appearanceKey                   // 1
)

// UserIDFromContext returns the authenticated user's ID from the request context.
// Returns an empty string if not set.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

func contextWithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// ContextWithUserID returns a context carrying the given user ID.
// Intended for use in tests that exercise handlers requiring an authenticated user.
func ContextWithUserID(ctx context.Context, userID string) context.Context {
	return contextWithUserID(ctx, userID)
}

// CreateSession sets a signed session cookie containing the user's DB ID.
func CreateSession(w http.ResponseWriter, secret, adminID string) {
	payload := sessionPayload{UserID: adminID, Exp: time.Now().Add(sessionDuration).Unix()}
	b, _ := json.Marshal(payload)
	encoded := base64.URLEncoding.EncodeToString(b)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encoded))
	sig := base64.URLEncoding.EncodeToString(mac.Sum(nil))

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    encoded + "." + sig,
		HttpOnly: true,
		Secure:   secureCookies,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   int(sessionDuration.Seconds()),
	})
}

// ValidateSession returns true if the request has a valid, unexpired session cookie.
func ValidateSession(r *http.Request, secret string) bool {
	_, ok := decodeSession(r, secret)
	return ok
}

// decodeSession validates the session cookie signature and expiry, returning the payload on success.
func decodeSession(r *http.Request, secret string) (*sessionPayload, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}

	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	encoded, sig := parts[0], parts[1]

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encoded))
	expectedSig := base64.URLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return nil, false
	}

	b, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	var payload sessionPayload
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, false
	}
	if time.Now().Unix() >= payload.Exp {
		return nil, false
	}
	return &payload, true
}

// ClearSession removes the session cookie.
func ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		HttpOnly: true,
		Secure:   secureCookies,
		MaxAge:   -1,
		Path:     "/",
	})
}
