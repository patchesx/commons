package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/platform"
	"commons/store"
	"commons/web"
)

type workItemResponse struct {
	ID             string  `json:"id"`
	RequesterID    string  `json:"requesterId"`
	RequesterName  string  `json:"requesterName"`
	Type           string  `json:"type"`
	Title          string  `json:"title"`
	Description    *string `json:"description"`
	Status         string  `json:"status"`
	AdminNotes     *string `json:"adminNotes"`
	ResolvedBy     *string `json:"resolvedBy"`
	ResolvedByName *string `json:"resolvedByName"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func toWorkItemResponse(item *store.WorkItem) workItemResponse {
	return workItemResponse{
		ID:             item.ID,
		RequesterID:    item.RequesterID,
		RequesterName:  item.RequesterName,
		Type:           item.Type,
		Title:          item.Title,
		Description:    item.Description,
		Status:         item.Status,
		AdminNotes:     item.AdminNotes,
		ResolvedBy:     item.ResolvedBy,
		ResolvedByName: item.ResolvedByName,
		CreatedAt:      item.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      item.UpdatedAt.Format(time.RFC3339),
	}
}

// ListWorkItems handles GET /api/work-items.
// Accepts optional ?status=, ?type=, and ?createdBy=me query parameters.
// When ?createdBy=me is set, returns only the current user's work items.
func ListWorkItems(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		statusFilter := r.URL.Query().Get("status")
		typeFilter := r.URL.Query().Get("type")
		createdBy := r.URL.Query().Get("createdBy")

		var items []store.WorkItem
		var err error
		if createdBy == "me" {
			userID := web.UserIDFromContext(ctx)
			items, err = store.ListWorkItemsByRequester(ctx, pool, userID, statusFilter, typeFilter)
		} else {
			items, err = store.ListWorkItems(ctx, pool, statusFilter, typeFilter)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list work items")
			return
		}

		out := make([]workItemResponse, len(items))
		for i := range items {
			out[i] = toWorkItemResponse(&items[i])
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// CreateWorkItemMember handles POST /api/work-items.
// Creates a work item with the current user as requester. Requires work_items.create permission.
// Body: {"type": "issue"|"feature_request", "title": "...", "description": "..."}
func CreateWorkItemMember(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)

		var body struct {
			Type        string  `json:"type"`
			Title       string  `json:"title"`
			Description *string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Type != "issue" && body.Type != "feature_request" {
			httpx.WriteError(w, http.StatusBadRequest, "type must be 'issue' or 'feature_request'")
			return
		}
		if body.Title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title is required")
			return
		}

		item, err := store.CreateWorkItem(ctx, pool, userID, body.Type, body.Title, body.Description)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create work item")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": item.ID})
	}
}

// GetWorkItem handles GET /api/work-items/{id}.
func GetWorkItem(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		item, err := store.GetWorkItem(r.Context(), pool, id)
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "work item not found")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to get work item")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toWorkItemResponse(item))
	}
}

var workItemTypeLabels = map[string]string{
	"issue":           "Bug Report",
	"feature_request": "Feature Request",
}

var workItemStatusLabels = map[string]string{
	"open":         "Open",
	"acknowledged": "Acknowledged",
	"in_progress":  "In Progress",
	"resolved":     "Resolved",
	"closed":       "Closed",
}

// UpdateWorkItem handles PUT /api/work-items/{id}.
// Body: {"status": "...", "adminNotes": "..."}
// Sets resolved_by to the session admin when status is "resolved" or "closed".
// Notifies the requestor if the status changed.
func UpdateWorkItem(pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		adminID := web.UserIDFromContext(ctx)

		var body struct {
			Status     string  `json:"status"`
			AdminNotes *string `json:"adminNotes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		validStatuses := map[string]bool{
			"open": true, "acknowledged": true, "in_progress": true,
			"resolved": true, "closed": true,
		}
		if !validStatuses[body.Status] {
			httpx.WriteError(w, http.StatusBadRequest, "invalid status")
			return
		}

		// Fetch current item to detect status change.
		existing, err := store.GetWorkItem(ctx, pool, id)
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "work item not found")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to get work item")
			return
		}
		oldStatus := existing.Status

		var resolvedBy *string
		if (body.Status == "resolved" || body.Status == "closed") && adminID != "" {
			resolvedBy = &adminID
		}

		if err := store.UpdateWorkItemStatus(ctx, pool, id, body.Status, body.AdminNotes, resolvedBy); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update work item")
			return
		}

		store.LogAction(ctx, pool, &adminID, "work_item.updated", id, map[string]string{"status": body.Status})

		if body.Status != oldStatus {
			updated := *existing
			updated.Status = body.Status
			updated.AdminNotes = body.AdminNotes
			go notifyWorkItemStatusChange(context.Background(), pool, encKey, notifier, &updated, oldStatus)
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func notifyWorkItemStatusChange(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, item *store.WorkItem, oldStatus string) {
	typeLabel := workItemTypeLabels[item.Type]
	if typeLabel == "" {
		typeLabel = item.Type
	}
	oldLabel := workItemStatusLabels[oldStatus]
	if oldLabel == "" {
		oldLabel = oldStatus
	}
	newLabel := workItemStatusLabels[item.Status]
	if newLabel == "" {
		newLabel = item.Status
	}

	title := fmt.Sprintf("%s status updated: %s → %s", typeLabel, oldLabel, newLabel)
	body := fmt.Sprintf("Your %s has been updated.\n*%s*\nStatus: %s → %s",
		typeLabel, item.Title, oldLabel, newLabel)
	if item.AdminNotes != nil && *item.AdminNotes != "" {
		body += "\n\n" + *item.AdminNotes
	}

	if err := store.NotifyUser(ctx, pool, encKey, item.RequesterID, "work_item_updated", title, body, "", notifier); err != nil {
		log.Printf("api: notify work item status change for item %s: %v", item.ID, err)
	}
}
