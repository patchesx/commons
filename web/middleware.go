package web

import (
	"encoding/json"
	"net/http"
)

// RequireWebAuth protects admin pages — redirects to /login if no valid session.
// On success, injects the authenticated user's ID into the request context.
// HTMX requests (HX-Request: true) receive an HX-Redirect header instead of a
// 303 redirect, so HTMX performs a full navigation rather than swapping the
// login page HTML into the current target element.
func RequireWebAuth(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeSession(r, secret)
		if !ok {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		r = r.WithContext(contextWithUserID(r.Context(), payload.UserID))
		next.ServeHTTP(w, r)
	})
}

// RequireAPIAuth protects API endpoints — returns 401 JSON if no valid session.
// On success, injects the authenticated admin's ID into the request context.
func RequireAPIAuth(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := decodeSession(r, secret)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		r = r.WithContext(contextWithUserID(r.Context(), payload.UserID))
		next.ServeHTTP(w, r)
	})
}
