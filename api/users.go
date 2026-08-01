package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"commons/internal/httpx"
	"commons/platform"
	"commons/store"
	"commons/web"
)

// GetMe handles GET /api/users/me — returns basic info and permissions for the logged-in user.
func GetMe(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		user, err := store.GetUserByID(ctx, pool, userID)
		if err != nil || user == nil {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, userID)
		if err != nil {
			perms = []string{}
		}
		if perms == nil {
			perms = []string{}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"id":          user.ID,
			"displayName": user.DisplayName,
			"email":       user.Email,
			"permissions": perms,
		})
	}
}

// ListUsers handles GET /api/users — returns all users with their current roles.
func ListUsers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		users, err := store.ListUsers(ctx, pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list users")
			return
		}

		// Build a set of user_ids with admin access for O(1) lookup below.
		adminIDs, err := store.ListWebAdminUserIDs(ctx, pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to check admin status")
			return
		}
		webAdminSet := map[string]bool{}
		for _, id := range adminIDs {
			webAdminSet[id] = true
		}

		statusFilter := r.URL.Query().Get("status")

		type userOut struct {
			ID             string  `json:"id"`
			DisplayName    string  `json:"displayName"`
			Email          *string `json:"email"`
			Group          string  `json:"group"`
			GroupID        string  `json:"groupId"`
			CreatedAt      string  `json:"createdAt"`
			IsWebAdmin     bool    `json:"isWebAdmin"`
			PlatformStatus string  `json:"platformStatus"`
			SlackID        string  `json:"slackId"`
		}

		out := make([]userOut, 0, len(users))
		for _, u := range users {
			if statusFilter != "" && u.PlatformStatus != statusFilter {
				continue
			}
			var groupName, groupID string
			group, err := store.GetUserGroup(ctx, pool, u.ID)
			if err == nil && group != nil {
				groupID = group.ID
				if group.DisplayName != "" {
					groupName = group.DisplayName
				} else {
					groupName = group.Name
				}
			}
			out = append(out, userOut{
				ID:             u.ID,
				DisplayName:    u.DisplayName,
				Email:          u.Email,
				Group:          groupName,
				GroupID:        groupID,
				CreatedAt:      u.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
				IsWebAdmin:     webAdminSet[u.ID],
				PlatformStatus: u.PlatformStatus,
				SlackID:        u.SlackID,
			})
		}

		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// SearchUsers handles GET /api/users/search?q=<string> — returns matching users with Slack identities.
// Returns an empty array if q is fewer than 2 characters.
func SearchUsers(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if len(q) < 2 {
			httpx.WriteJSON(w, http.StatusOK, []struct{}{})
			return
		}

		users, err := store.SearchUsers(r.Context(), pool, q)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to search users")
			return
		}

		type userOut struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}
		out := make([]userOut, len(users))
		for i, u := range users {
			out[i] = userOut{ID: u.ID, DisplayName: u.DisplayName}
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// AssignGroup handles PUT /api/users/{id}/group
// Body: {"groupId": "..."}
func AssignGroup(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("id")

		var body struct {
			GroupID string `json:"groupId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.GroupID == "" {
			httpx.WriteError(w, http.StatusBadRequest, "groupId is required")
			return
		}

		if err := store.AssignGroupToUser(ctx, pool, userID, body.GroupID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to assign group")
			return
		}

		store.LogAction(ctx, pool, nil, "user.group_assigned", userID, map[string]string{
			"user_id":  userID,
			"group_id": body.GroupID,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// RemoveGroup handles DELETE /api/users/{id}/group.
func RemoveGroup(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("id")

		if err := store.RemoveGroupFromUser(ctx, pool, userID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to remove group")
			return
		}

		store.LogAction(ctx, pool, nil, "user.group_removed", userID, map[string]string{
			"user_id": userID,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// PromoteUser handles POST /api/users/{id}/promote — creates a web_admin account linked
// to an existing Slack user. Accepts an optional email override in the request body;
// falls back to the email stored on the user record. DMs the user their login link via Slack.
func PromoteUser(pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("id")

		var body struct {
			Email *string `json:"email"`
		}
		// body is optional — ignore decode errors
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck

		already, err := store.IsWebAdmin(ctx, pool, userID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to check admin status")
			return
		}
		if already {
			httpx.WriteError(w, http.StatusConflict, "user already has admin access")
			return
		}

		user, err := store.GetUserByID(ctx, pool, userID)
		if err != nil || user == nil {
			httpx.WriteError(w, http.StatusNotFound, "user not found")
			return
		}

		// Resolve email: body override → user record → error
		var email string
		if body.Email != nil && *body.Email != "" {
			email = *body.Email
		} else if user.Email != nil && *user.Email != "" {
			email = *user.Email
		} else {
			httpx.WriteError(w, http.StatusBadRequest, "email required — user has no email on record")
			return
		}

		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to generate password")
			return
		}
		tempPassword := base64.URLEncoding.EncodeToString(raw)

		hash, err := bcrypt.GenerateFromPassword([]byte(tempPassword), bcrypt.DefaultCost)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}

		if err := store.PromoteUserToWebAdmin(ctx, pool, userID, email, string(hash)); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create admin account")
			return
		}

		if notifier != nil {
			adminURL, _ := store.GetServiceConfig(ctx, pool, "bot", "admin_url", encKey)
			var text string
			if adminURL != "" {
				text = fmt.Sprintf("You've been added as an admin.\nLogin at: %s\nTemporary password: `%s`\nYou'll be prompted to set a new password on first login.", adminURL, tempPassword)
			} else {
				text = fmt.Sprintf("You've been added as an admin.\nTemporary password: `%s`\nYou'll be prompted to set a new password on first login.", tempPassword)
			}
			if err := notifier.NotifyUser(ctx, userID, platform.Message{Text: text}); err != nil {
				log.Printf("api: promote user: notify user %s: %v", userID, err)
			}
		}

		store.LogAction(ctx, pool, nil, "user.promoted_to_admin", userID, map[string]string{
			"user_id": userID,
			"email":   email,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"email": email})
	}
}

// PatchMe handles PATCH /api/users/me — updates the authenticated user's display name.
func PatchMe(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)

		var body struct {
			DisplayName string `json:"displayName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DisplayName == "" {
			httpx.WriteError(w, http.StatusBadRequest, "displayName required")
			return
		}

		if err := store.UpdateUserDisplayName(ctx, pool, userID, body.DisplayName); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update display name")
			return
		}

		user, err := store.GetUserByID(ctx, pool, userID)
		if err != nil || user == nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to fetch updated user")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"id":          user.ID,
			"displayName": user.DisplayName,
			"email":       user.Email,
		})
	}
}

// ChangePasswordMember handles POST /api/users/me/password — lets any authenticated user
// change their own password (requires current password verification).
func ChangePasswordMember(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)

		var body struct {
			CurrentPassword string `json:"currentPassword"`
			NewPassword     string `json:"newPassword"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CurrentPassword == "" || body.NewPassword == "" {
			httpx.WriteError(w, http.StatusBadRequest, "currentPassword and newPassword required")
			return
		}
		if len(body.NewPassword) < 8 {
			httpx.WriteError(w, http.StatusBadRequest, "new password must be at least 8 characters")
			return
		}

		creds, err := store.GetUserCredentialsByID(ctx, pool, userID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "credentials not found")
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
		if err := store.SetUserPassword(ctx, pool, userID, string(hash)); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update password")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
