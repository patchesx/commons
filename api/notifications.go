package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

type notificationResponse struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Body      *string `json:"body"`
	ActionURL *string `json:"actionUrl"`
	Read      bool    `json:"read"`
	CreatedAt string  `json:"createdAt"`
}

func toNotificationResponse(n store.Notification) notificationResponse {
	return notificationResponse{
		ID:        n.ID,
		Type:      n.Type,
		Title:     n.Title,
		Body:      n.Body,
		ActionURL: n.ActionURL,
		Read:      n.Read,
		CreatedAt: n.CreatedAt.Format(time.RFC3339),
	}
}

// ListNotifications handles GET /api/notifications.
// Query params: unread=true, limit (default 50), offset (default 0).
func ListNotifications(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		unreadOnly := r.URL.Query().Get("unread") == "true"
		limit := 50
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		notifs, err := store.ListNotifications(r.Context(), pool, userID, unreadOnly, limit, offset)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list notifications")
			return
		}

		out := make([]notificationResponse, len(notifs))
		for i, n := range notifs {
			out[i] = toNotificationResponse(n)
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// CountUnreadNotifications handles GET /api/notifications/count.
// Returns {"unread": N}.
func CountUnreadNotifications(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		count, err := store.CountUnread(r.Context(), pool, userID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to count notifications")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]int{"unread": count})
	}
}

// MarkAllNotificationsRead handles POST /api/notifications/read-all.
func MarkAllNotificationsRead(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		if err := store.MarkAllRead(r.Context(), pool, userID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to mark notifications read")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// MarkNotificationRead handles PATCH /api/notifications/{id}/read.
func MarkNotificationRead(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		id := r.PathValue("id")
		if err := store.MarkRead(r.Context(), pool, id, userID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to mark notification read")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
