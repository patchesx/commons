package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/plugin"
	"commons/store"
)

// apiJob is the list-view response shape for all job types.
type apiJob struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Feature      string     `json:"feature"`
	Status       string     `json:"status"`
	Trigger      string     `json:"trigger"`
	ErrorMessage *string    `json:"errorMessage,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	Summary      string     `json:"summary"`
}

// apiJobDetail is returned by GET /api/jobs/{id}.
type apiJobDetail struct {
	apiJob
	Detail any `json:"detail,omitempty"`
}

// BuildSummary returns a one-line description of a job. If a provider has been
// registered for the job type (via plugin.RegisterJobSummaryProvider) it is
// called; otherwise a static fallback is used.
func BuildSummary(ctx context.Context, pool *pgxpool.Pool, j store.Job) string {
	if fn := plugin.GetJobSummaryProvider(j.Type); fn != nil {
		return fn(ctx, pool, j.ID)
	}
	switch j.Type {
	case store.JobTypeRecordingUpload:
		return "Recording upload"
	case store.JobTypeMemberSync:
		return "Member sync"
	case store.JobTypeOpenStatesSync:
		return "OpenStates sync"
	case store.JobTypeLegistarSync:
		return "Legistar sync"
	case store.JobTypeMeetingSync:
		return "Zoom meeting sync"
	default:
		return j.Type
	}
}

// BuildDetail collects all registered contributor values for the job type and
// returns them as a map. Returns nil if no contributors are registered.
func BuildDetail(ctx context.Context, pool *pgxpool.Pool, jobType, jobID string) any {
	fns := plugin.GetJobDetailContributors(jobType)
	if len(fns) == 0 {
		return nil
	}
	m := map[string]any{}
	for _, fn := range fns {
		k, v := fn(ctx, pool, jobID)
		if k != "" && v != nil {
			m[k] = v
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func toAPIJob(ctx context.Context, pool *pgxpool.Pool, j store.Job) apiJob {
	return apiJob{
		ID:           j.ID,
		Type:         j.Type,
		Feature:      j.Feature,
		Status:       j.Status,
		Trigger:      j.Trigger,
		ErrorMessage: j.ErrorMessage,
		StartedAt:    j.StartedAt,
		CompletedAt:  j.CompletedAt,
		Summary:      BuildSummary(ctx, pool, j),
	}
}

// ListJobs handles GET /api/jobs?page=1&limit=20&type=&status=&feature=
func ListJobs(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()

		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit < 1 || limit > 100 {
			limit = 20
		}
		offset := (page - 1) * limit

		filter := store.JobFilter{
			Type:    q.Get("type"),
			Feature: q.Get("feature"),
			Status:  q.Get("status"),
		}

		jobs, total, err := store.ListJobsPaginated(ctx, pool, limit, offset, filter)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list jobs")
			return
		}

		out := make([]apiJob, len(jobs))
		for i, j := range jobs {
			out[i] = toAPIJob(ctx, pool, j)
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"jobs":  out,
			"total": total,
		})
	}
}

// GetJob handles GET /api/jobs/{id} — returns base fields + type-specific detail.
func GetJob(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		jobID := r.PathValue("id")

		j, err := store.GetJobByID(ctx, pool, jobID)
		if err != nil || j == nil {
			httpx.WriteError(w, http.StatusNotFound, "job not found")
			return
		}

		httpx.WriteJSON(w, http.StatusOK, apiJobDetail{
			apiJob: toAPIJob(ctx, pool, *j),
			Detail: BuildDetail(ctx, pool, j.Type, j.ID),
		})
	}
}

// CancelJob handles POST /api/jobs/{id}/cancel.
// Marks the job cancelled in the DB and calls cancelFn to abort its context if registered.
func CancelJob(pool *pgxpool.Pool, cancelFn func(string) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		jobID := r.PathValue("id")

		job, err := store.GetJobByID(ctx, pool, jobID)
		if err != nil || job == nil {
			httpx.WriteError(w, http.StatusNotFound, "job not found")
			return
		}
		if job.Status != store.JobStatusPending && job.Status != store.JobStatusRunning {
			httpx.WriteError(w, http.StatusConflict, "job is not in a cancellable state")
			return
		}
		updated, err := store.CancelJob(ctx, pool, jobID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to cancel job")
			return
		}
		if !updated {
			httpx.WriteError(w, http.StatusConflict, "job is not in a cancellable state")
			return
		}
		cancelFn(jobID)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	}
}
