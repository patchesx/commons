package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

// SubscribeBill handles POST /api/legislation/bills/{id}/subscribe.
func SubscribeBill(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		billID := r.PathValue("id")
		if err := store.SubscribeUserToBill(r.Context(), pool, userID, billID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to subscribe")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// UnsubscribeBill handles DELETE /api/legislation/bills/{id}/subscribe.
func UnsubscribeBill(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		billID := r.PathValue("id")
		if err := store.UnsubscribeUserFromBill(r.Context(), pool, userID, billID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to unsubscribe")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ListMySubscriptions handles GET /api/legislation/subscriptions.
// Returns the bill IDs the current user is subscribed to.
func ListMySubscriptions(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := web.UserIDFromContext(r.Context())
		bills, err := store.GetUserSubscriptions(r.Context(), pool, userID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list subscriptions")
			return
		}
		ids := make([]string, len(bills))
		for i, b := range bills {
			ids[i] = b.ID
		}
		httpx.WriteJSON(w, http.StatusOK, map[string][]string{"billIds": ids})
	}
}

// ListLegislativeBodies handles GET /api/legislation/bodies.
func ListLegislativeBodies(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bodies, err := store.ListLegislativeBodies(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list bodies")
			return
		}
		if bodies == nil {
			bodies = []store.LegislativeBody{}
		}
		httpx.WriteJSON(w, http.StatusOK, bodies)
	}
}

// ListBills handles GET /api/legislation/bills.
// Optional query param: ?body={bodyID} to filter by legislative body.
func ListBills(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		bodyID := r.URL.Query().Get("body")

		var (
			bills []store.Bill
			err   error
		)
		if bodyID != "" {
			bills, err = store.ListBillsByBody(ctx, pool, bodyID)
		} else {
			bills, err = store.ListBills(ctx, pool)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list bills")
			return
		}

		// Enrich with tags.
		for i := range bills {
			tags, err := store.ListBillTags(ctx, pool, bills[i].ID)
			if err == nil {
				bills[i].Tags = tags
			}
		}

		if bills == nil {
			bills = []store.Bill{}
		}
		httpx.WriteJSON(w, http.StatusOK, bills)
	}
}

// GetBill handles GET /api/legislation/bills/{id}.
func GetBill(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		bill, err := store.GetBill(ctx, pool, id)
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "bill not found")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to get bill")
			return
		}

		tags, err := store.ListBillTags(ctx, pool, id)
		if err == nil {
			bill.Tags = tags
		}

		httpx.WriteJSON(w, http.StatusOK, bill)
	}
}

// CreateBill handles POST /api/legislation/bills.
// For manually adding a bill not sourced from an API.
func CreateBill(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var b store.Bill
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(b.BodyID) == "" || strings.TrimSpace(b.Identifier) == "" || strings.TrimSpace(b.Title) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "body_id, identifier, and title are required")
			return
		}

		if err := store.CreateBill(ctx, pool, &b); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create bill")
			return
		}

		httpx.WriteJSON(w, http.StatusCreated, b)
	}
}

// UpdateBill handles PUT /api/legislation/bills/{id}.
// Updates admin-managed fields: identifier, title, chapter_position, notes, link.
func UpdateBill(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		var body struct {
			Identifier      string   `json:"identifier"`
			Title           string   `json:"title"`
			ChapterPosition *string  `json:"chapterPosition"`
			Notes           *string  `json:"notes"`
			Link            *string  `json:"link"`
			TagIDs          []string `json:"tagIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		b := store.Bill{
			ID:              id,
			Identifier:      body.Identifier,
			Title:           body.Title,
			ChapterPosition: body.ChapterPosition,
			Notes:           body.Notes,
			Link:            body.Link,
		}
		if err := store.UpdateBill(ctx, pool, &b); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update bill")
			return
		}

		if body.TagIDs != nil {
			if err := store.SetBillTags(ctx, pool, id, body.TagIDs); err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "failed to update tags")
				return
			}
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DismissBill handles DELETE /api/legislation/bills/{id}.
// Sets following=false so the bill is hidden and skipped on future syncs.
func DismissBill(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.SetBillFollowing(ctx, pool, id, false); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to dismiss bill")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ListTags handles GET /api/legislation/tags.
func ListTags(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := store.ListTags(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list tags")
			return
		}
		if tags == nil {
			tags = []store.Tag{}
		}
		httpx.WriteJSON(w, http.StatusOK, tags)
	}
}

// ListBodyFilters handles GET /api/legislation/bodies/{id}/filters.
func ListBodyFilters(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		filters, err := store.ListImportFilters(r.Context(), pool, id)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list filters")
			return
		}
		if filters == nil {
			filters = []store.ImportFilter{}
		}
		httpx.WriteJSON(w, http.StatusOK, filters)
	}
}

// CreateBodyFilter handles POST /api/legislation/bodies/{id}/filters.
func CreateBodyFilter(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		bodyID := r.PathValue("id")

		var body struct {
			FilterType string `json:"filterType"`
			Value      string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(body.FilterType) == "" || strings.TrimSpace(body.Value) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "filterType and value are required")
			return
		}

		f := &store.ImportFilter{
			BodyID:     bodyID,
			FilterType: strings.TrimSpace(body.FilterType),
			Value:      strings.TrimSpace(body.Value),
			Active:     true,
		}
		if err := store.CreateImportFilter(ctx, pool, f); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create filter")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, f)
	}
}

// DeleteBodyFilter handles DELETE /api/legislation/filters/{id}.
func DeleteBodyFilter(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.DeleteImportFilter(r.Context(), pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete filter")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// CreateBody handles POST /api/legislation/bodies.
func CreateBody(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var b store.LegislativeBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.Level) == "" ||
			strings.TrimSpace(b.State) == "" || strings.TrimSpace(b.DataSource) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name, level, state, and data_source are required")
			return
		}
		b.Active = true // default new bodies to active

		if err := store.CreateLegislativeBody(ctx, pool, &b); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create body")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, b)
	}
}

// UpdateBody handles PUT /api/legislation/bodies/{id}.
func UpdateBody(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		var b store.LegislativeBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.Level) == "" ||
			strings.TrimSpace(b.State) == "" || strings.TrimSpace(b.DataSource) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name, level, state, and data_source are required")
			return
		}
		b.ID = id

		if err := store.UpdateLegislativeBody(ctx, pool, &b); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update body")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// TriggerSync handles POST /api/legislation/sync.
// Runs the sync in a background goroutine and returns immediately.
// syncFn is injected from main.go to avoid an import cycle between api and legislation packages.
func TriggerSync(syncFn func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go syncFn()
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "sync started"})
	}
}

// CreateTag handles POST /api/legislation/tags.
func CreateTag(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name is required")
			return
		}

		tag, err := store.CreateTag(ctx, pool, strings.TrimSpace(body.Name))
		if err != nil {
			if strings.Contains(err.Error(), "23505") {
				httpx.WriteError(w, http.StatusConflict, "a tag with that name already exists")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create tag")
			return
		}

		httpx.WriteJSON(w, http.StatusCreated, tag)
	}
}
