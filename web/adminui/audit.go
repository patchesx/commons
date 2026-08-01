package adminui

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
	admintempl "commons/web/templ"
)

const auditPageLimit = 50

func loadAuditPage(ctx context.Context, pool *pgxpool.Pool, page int) ([]store.AuditEntry, int, int, error) {
	total, err := store.CountAuditLog(ctx, pool)
	if err != nil {
		return nil, 0, 0, err
	}
	offset := (page - 1) * auditPageLimit
	entries, err := store.ListAuditLog(ctx, pool, auditPageLimit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	totalPages := (total + auditPageLimit - 1) / auditPageLimit
	if totalPages < 1 {
		totalPages = 1
	}
	return entries, total, totalPages, nil
}

func (d Deps) AuditPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		entries, total, totalPages, err := loadAuditPage(r.Context(), d.Pool, page)
		if err != nil {
			http.Error(w, "failed to load audit log", http.StatusInternalServerError)
			return
		}
		admintempl.AuditPage(entries, total, page, totalPages).Render(r.Context(), w)
	}
}

func (d Deps) AuditRows() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		entries, total, totalPages, err := loadAuditPage(r.Context(), d.Pool, page)
		if err != nil {
			FragmentError(w, r, "failed to load audit log")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.AuditRows(entries, total, page, totalPages).Render(r.Context(), w)
	}
}
