package adminui

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/api"
	"commons/store"
	admintempl "commons/web/templ"
)

const jobsPageLimit = 20

func (d Deps) JobsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		typeFilter := q.Get("type")
		statusFilter := q.Get("status")
		jobs, total, totalPages, err := loadJobsPage(ctx, d.Pool, page, 20, typeFilter, statusFilter)
		if err != nil {
			FragmentError(w, r, "failed to load jobs")
			return
		}
		admintempl.JobsPage(jobs, total, page, totalPages, typeFilter, statusFilter).Render(ctx, w)
	}
}

func (d Deps) JobsTable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		typeFilter := q.Get("type")
		statusFilter := q.Get("status")
		jobs, total, totalPages, err := loadJobsPage(ctx, d.Pool, page, 20, typeFilter, statusFilter)
		if err != nil {
			FragmentError(w, r, "failed to load jobs")
			return
		}
		admintempl.JobsTable(jobs, total, page, totalPages, typeFilter, statusFilter).Render(ctx, w)
	}
}

func (d Deps) JobDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		j, err := store.GetJobByID(ctx, d.Pool, r.PathValue("id"))
		if err != nil || j == nil {
			FragmentError(w, r, "not found")
			return
		}
		admintempl.JobDetailFragment(buildJobDetailView(ctx, d.Pool, *j)).Render(ctx, w)
	}
}

func (d Deps) JobCancel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		jobID := r.PathValue("id")
		j, err := store.GetJobByID(ctx, d.Pool, jobID)
		if err != nil || j == nil {
			FragmentError(w, r, "not found")
			return
		}
		if j.Status != store.JobStatusPending && j.Status != store.JobStatusRunning {
			FragmentError(w, r, "job not cancellable")
			return
		}
		if ok, err := store.CancelJob(ctx, d.Pool, jobID); err != nil || !ok {
			FragmentError(w, r, "failed to cancel")
			return
		}
		d.Pctx.CancelJob(jobID)
		w.WriteHeader(http.StatusOK)
	}
}

func loadJobsPage(ctx context.Context, pool *pgxpool.Pool, page, limit int, typeFilter, statusFilter string) ([]admintempl.JobView, int, int, error) {
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit
	filter := store.JobFilter{Type: typeFilter, Status: statusFilter}
	jobs, total, err := store.ListJobsPaginated(ctx, pool, limit, offset, filter)
	if err != nil {
		return nil, 0, 0, err
	}
	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}
	views := make([]admintempl.JobView, len(jobs))
	for i, j := range jobs {
		views[i] = admintempl.JobView{
			ID:           j.ID,
			Type:         j.Type,
			Feature:      j.Feature,
			Status:       j.Status,
			Trigger:      j.Trigger,
			ErrorMessage: j.ErrorMessage,
			StartedAt:    j.StartedAt,
			CompletedAt:  j.CompletedAt,
			Summary:      api.BuildSummary(ctx, pool, j),
		}
	}
	return views, total, totalPages, nil
}

func buildJobDetailView(ctx context.Context, pool *pgxpool.Pool, j store.Job) admintempl.JobDetailView {
	d := admintempl.JobDetailView{Type: j.Type}
	if j.ErrorMessage != nil {
		d.ErrorMessage = *j.ErrorMessage
	}
	raw := api.BuildDetail(ctx, pool, j.Type, j.ID)
	if raw == nil {
		return d
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return d
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return d
	}

	if zoomRaw, ok := m["zoom"]; ok {
		var rec struct {
			MeetingTopic string     `json:"MeetingTopic"`
			MeetingDate  *time.Time `json:"MeetingDate"`
			DurationSecs *int       `json:"DurationSecs"`
			HostEmail    *string    `json:"HostEmail"`
		}
		if json.Unmarshal(zoomRaw, &rec) == nil {
			d.MeetingTopic = rec.MeetingTopic
			d.MeetingDate = rec.MeetingDate
			if rec.DurationSecs != nil {
				d.DurationMins = *rec.DurationSecs / 60
			}
			if rec.HostEmail != nil {
				d.HostEmail = *rec.HostEmail
			}
		}
	}
	if ytRaw, ok := m["youtube"]; ok {
		var upload struct {
			VideoID *string `json:"VideoID"`
			Title   string  `json:"Title"`
		}
		if json.Unmarshal(ytRaw, &upload) == nil {
			d.VideoTitle = upload.Title
			if upload.VideoID != nil {
				d.VideoID = *upload.VideoID
			}
		}
	}
	if gdRaw, ok := m["gdrive"]; ok {
		var backup struct {
			FolderID *string `json:"FolderID"`
		}
		if json.Unmarshal(gdRaw, &backup) == nil && backup.FolderID != nil {
			d.DriveFolderID = *backup.FolderID
		}
	}
	return d
}
