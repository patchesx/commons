package api

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/store"
)

// ListAudit handles GET /api/audit?page=1&limit=50
func ListAudit(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 || limit > 200 {
			limit = 50
		}
		offset := (page - 1) * limit

		total, err := store.CountAuditLog(ctx, pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to count audit log")
			return
		}

		entries, err := store.ListAuditLog(ctx, pool, limit, offset)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list audit log")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"entries": entries,
			"total":   total,
		})
	}
}
