package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// CheckAuth handles GET /api/auth/check.
// Returns 200 with {"forcePasswordReset": bool, "isAdmin": bool} if the session is valid.
// The RequireAPIAuth middleware handles the 401 side.
func CheckAuth(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)

		// A missing credentials row (e.g. a Slack-only user) means no forced reset.
		forceReset := false
		if creds, err := store.GetUserCredentialsByID(ctx, pool, userID); err == nil {
			forceReset = creds.ForcePasswordReset
		}

		isAdmin, _ := store.IsWebAdmin(ctx, pool, userID)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"forcePasswordReset": forceReset, "isAdmin": isAdmin})
	}
}

// Register handles POST /api/auth/register — public endpoint for self-registration.
// Only functional when config_store(auth, allow_registration) = 'true'.
func Register(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		allowed, _ := store.GetServiceConfig(ctx, pool, "auth", "allow_registration", encKey)
		if strings.TrimSpace(allowed) != "true" {
			httpx.WriteError(w, http.StatusForbidden, "Account registration is not open. Contact your organization admin.")
			return
		}

		var body struct {
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
			Password    string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Email = strings.TrimSpace(strings.ToLower(body.Email))
		body.DisplayName = strings.TrimSpace(body.DisplayName)

		if body.DisplayName == "" {
			httpx.WriteError(w, http.StatusBadRequest, "display name is required")
			return
		}
		if !emailRE.MatchString(body.Email) {
			httpx.WriteError(w, http.StatusBadRequest, "a valid email address is required")
			return
		}
		if len(body.Password) < 8 {
			httpx.WriteError(w, http.StatusBadRequest, "password must be at least 8 characters")
			return
		}

		// Check for existing web identity with this email.
		exists, err := store.WebIdentityExists(ctx, pool, body.Email)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create account")
			return
		}
		if exists {
			httpx.WriteError(w, http.StatusConflict, "an account with that email already exists")
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create account")
			return
		}
		defer tx.Rollback(ctx) //nolint:errcheck

		var userID string
		if err := tx.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ($1) RETURNING id`, body.DisplayName).Scan(&userID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create account")
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_identities (user_id, provider, external_id, username, display_name)
			VALUES ($1, 'web', $2, $3, $4)
			ON CONFLICT (provider, external_id) DO NOTHING
		`, userID, body.Email, body.Email, body.DisplayName); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create account")
			return
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_credentials (user_id, password_hash) VALUES ($1, $2)
			ON CONFLICT (user_id) DO NOTHING
		`, userID, string(hash)); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create account")
			return
		}

		// Assign default role group if configured.
		defaultGroupID, _ := store.GetServiceConfig(ctx, pool, "auth", "default_group_id", encKey)
		if strings.TrimSpace(defaultGroupID) != "" {
			tx.Exec(ctx, `INSERT INTO user_role_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT (user_id) DO UPDATE SET group_id = EXCLUDED.group_id`, userID, defaultGroupID) //nolint:errcheck
		}

		if err := tx.Commit(ctx); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create account")
			return
		}

		w.WriteHeader(http.StatusCreated)
	}
}
