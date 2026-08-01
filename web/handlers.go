package web

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"commons/config"
	"commons/store"
)

// dummyHash is a pre-computed bcrypt hash used to perform a constant-time comparison
// when the submitted email does not match any account, preventing timing-based enumeration.
var dummyHash []byte

func init() {
	var err error
	dummyHash, err = bcrypt.GenerateFromPassword([]byte("timing_safe_dummy_not_used"), 12)
	if err != nil {
		panic("web: failed to generate dummy bcrypt hash: " + err.Error())
	}
}

// LoginSubmit handles POST /login: validates credentials and creates a session.
func LoginSubmit(cfg *config.Config, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.FormValue("username")
		password := r.FormValue("password")

		if username == "" || password == "" {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		// All logins go through the DB — including the bootstrap admin, which gets a
		// user_credentials row on first startup. Once credentials exist, env vars are ignored.
		user, creds, err := store.GetUserByWebCredentials(r.Context(), pool, username)
		if err != nil {
			// User not found — still run bcrypt to prevent timing-based account enumeration.
			bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) //nolint:errcheck
			log.Printf("web/login: failed attempt")
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		if creds.PasswordHash == nil {
			// Account not yet activated — use dummy hash to maintain constant timing.
			bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) //nolint:errcheck
			log.Printf("web/login: failed attempt (no password set)")
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*creds.PasswordHash), []byte(password)); err != nil {
			log.Printf("web/login: failed attempt")
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		CreateSession(w, cfg.SessionSecret, user.ID)
		// Clear legacy cookie name during transition
		http.SetCookie(w, &http.Cookie{
			Name:   "admin_session",
			Value:  "",
			MaxAge: -1,
			Path:   "/",
		})
		if creds.ForcePasswordReset {
			http.Redirect(w, r, "/change-password", http.StatusSeeOther)
			return
		}
		portalEnabled, _ := store.GetServiceConfig(r.Context(), pool, "portal", "enabled", cfg.EncryptionKey)
		if portalEnabled == "true" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/admin/docs", http.StatusSeeOther)
		}
	}
}

// Logout handles POST /logout: clears the session cookie and redirects to /login.
func Logout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ClearSession(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}
