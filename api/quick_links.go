package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// ListQuickLinks handles GET /api/quick-links.
// Returns all quick links (active and inactive) for the admin page.
func ListQuickLinks(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		links, err := store.ListQuickLinks(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list quick links")
			return
		}
		if links == nil {
			links = []store.QuickLink{}
		}
		httpx.WriteJSON(w, http.StatusOK, links)
	}
}

// CreateQuickLink handles POST /api/quick-links.
// Body: {label, url, position}
func CreateQuickLink(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var l store.QuickLink
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		l.Label = strings.TrimSpace(l.Label)
		l.URL = strings.TrimSpace(l.URL)
		if l.Label == "" || l.URL == "" {
			httpx.WriteError(w, http.StatusBadRequest, "label and url are required")
			return
		}
		l.Active = true

		if err := store.CreateQuickLink(ctx, pool, &l); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create quick link")
			return
		}

		store.LogAction(ctx, pool, nil, "quick_link.created", l.ID, map[string]string{"label": l.Label})
		httpx.WriteJSON(w, http.StatusCreated, l)
	}
}

// UpdateQuickLink handles PUT /api/quick-links/{id}.
// Body: {label, url, position, active}
func UpdateQuickLink(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		var l store.QuickLink
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		l.ID = id
		l.Label = strings.TrimSpace(l.Label)
		l.URL = strings.TrimSpace(l.URL)
		if l.Label == "" || l.URL == "" {
			httpx.WriteError(w, http.StatusBadRequest, "label and url are required")
			return
		}

		if err := store.UpdateQuickLink(ctx, pool, &l); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update quick link")
			return
		}

		store.LogAction(ctx, pool, nil, "quick_link.updated", id, map[string]string{"label": l.Label})
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteQuickLink handles DELETE /api/quick-links/{id}.
func DeleteQuickLink(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.DeleteQuickLink(ctx, pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete quick link")
			return
		}

		store.LogAction(ctx, pool, nil, "quick_link.deleted", id, nil)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
