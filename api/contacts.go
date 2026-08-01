package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// ListContacts handles GET /api/contacts.
func ListContacts(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contacts, err := store.ListContacts(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list contacts")
			return
		}
		if contacts == nil {
			contacts = []store.Contact{}
		}
		httpx.WriteJSON(w, http.StatusOK, contacts)
	}
}

// CreateContact handles POST /api/contacts.
// Body: {userId, title, position}
func CreateContact(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var body struct {
			UserID   string `json:"userId"`
			Title    string `json:"title"`
			Position int    `json:"position"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Title = strings.TrimSpace(body.Title)
		if body.UserID == "" || body.Title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "user_id and title are required")
			return
		}

		c := &store.Contact{
			UserID:   body.UserID,
			Title:    body.Title,
			Position: body.Position,
		}
		if err := store.CreateContact(ctx, pool, c); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create contact")
			return
		}

		// Re-fetch to get SlackID and SlackName via the JOIN.
		contacts, err := store.ListContacts(ctx, pool)
		if err == nil {
			for _, ct := range contacts {
				if ct.ID == c.ID {
					*c = ct
					break
				}
			}
		}

		store.LogAction(ctx, pool, nil, "contact.created", c.ID, map[string]string{"title": c.Title})
		httpx.WriteJSON(w, http.StatusCreated, c)
	}
}

// UpdateContact handles PUT /api/contacts/{id}.
// Body: {title, position}
func UpdateContact(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		var body struct {
			Title    string `json:"title"`
			Position int    `json:"position"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.Title = strings.TrimSpace(body.Title)
		if body.Title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title is required")
			return
		}

		c := &store.Contact{ID: id, Title: body.Title, Position: body.Position}
		if err := store.UpdateContact(ctx, pool, c); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update contact")
			return
		}

		store.LogAction(ctx, pool, nil, "contact.updated", id, map[string]string{"title": body.Title})
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteContact handles DELETE /api/contacts/{id}.
func DeleteContact(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.DeleteContact(ctx, pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete contact")
			return
		}

		store.LogAction(ctx, pool, nil, "contact.deleted", id, nil)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
