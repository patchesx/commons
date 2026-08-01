package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

// ChangePassword handles POST /api/admins/me/password — allows an admin to change
// their own password. Requires current password for verification.
func ChangePassword(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminID := web.UserIDFromContext(r.Context())

		var body struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CurrentPassword == "" || body.NewPassword == "" {
			httpx.WriteError(w, http.StatusBadRequest, "currentPassword and newPassword required")
			return
		}

		creds, err := store.GetUserCredentialsByID(r.Context(), pool, adminID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "admin not found")
			return
		}

		if creds.PasswordHash == nil || bcrypt.CompareHashAndPassword([]byte(*creds.PasswordHash), []byte(body.CurrentPassword)) != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "current password incorrect")
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}

		if err := store.SetUserPassword(r.Context(), pool, adminID, string(hash)); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update password")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
