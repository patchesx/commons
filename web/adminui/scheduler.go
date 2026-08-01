package adminui

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
	admintempl "commons/web/templ"
)

var schedulerJobDefs = []struct {
	ID    string
	Label string
}{
	{"zoom_meeting_sync", "Zoom Meeting Sync"},
	{"legislation_sync", "Legislation Sync"},
	{"slack_user_sync", "Slack User Sync"},
	{"meeting_reminders", "Meeting Reminders"},
	{"library_overdue", "Library Overdue Reminders"},
}

func validSchedulerID(id string) bool {
	for _, def := range schedulerJobDefs {
		if def.ID == id {
			return true
		}
	}
	return false
}

func schedulerJobLabel(id string) string {
	for _, def := range schedulerJobDefs {
		if def.ID == id {
			return def.Label
		}
	}
	return id
}

func loadSchedulerJobs(ctx context.Context, pool *pgxpool.Pool) ([]admintempl.SchedulerJob, error) {
	configs, err := store.ListServiceConfigs(ctx, pool, "jobs")
	if err != nil {
		return nil, err
	}
	schema, err := store.ListConfigSchema(ctx, pool, "jobs")
	if err != nil {
		return nil, err
	}

	configByKey := make(map[string]string, len(configs))
	for _, c := range configs {
		configByKey[c.Key] = c.Value
	}
	descByKey := make(map[string]string, len(schema))
	for _, s := range schema {
		if s.Description != nil {
			descByKey[s.Key] = *s.Description
		}
	}

	jobs := make([]admintempl.SchedulerJob, 0, len(schedulerJobDefs))
	for _, def := range schedulerJobDefs {
		enabled := configByKey[def.ID+"_enabled"] == "true"
		minutes := 0
		if v, ok := configByKey[def.ID+"_interval_minutes"]; ok {
			minutes, _ = strconv.Atoi(v)
		}
		jobs = append(jobs, admintempl.SchedulerJob{
			ID:          def.ID,
			Label:       def.Label,
			Enabled:     enabled,
			Minutes:     minutes,
			Description: descByKey[def.ID+"_interval_minutes"],
		})
	}
	return jobs, nil
}

func (d Deps) SchedulerPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobs, err := loadSchedulerJobs(r.Context(), d.Pool)
		if err != nil {
			http.Error(w, "failed to load scheduler config", http.StatusInternalServerError)
			return
		}
		admintempl.SchedulerPage(jobs).Render(r.Context(), w)
	}
}

func (d Deps) SchedulerToggle() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSchedulerID(id) {
			FragmentError(w, r, "unknown job")
			return
		}
		label := schedulerJobLabel(id)
		current, err := store.GetServiceConfig(r.Context(), d.Pool, "jobs", id+"_enabled", d.EncKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			FragmentError(w, r, "failed to read config")
			return
		}
		newVal := "true"
		if current == "true" {
			newVal = "false"
		}
		if err := store.SetServiceConfig(r.Context(), d.Pool, "jobs", id+"_enabled", newVal, false, nil, d.EncKey); err != nil {
			FragmentError(w, r, "failed to update config")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.SchedulerToggleButton(id, label, newVal == "true").Render(r.Context(), w)
	}
}

func (d Deps) SchedulerInterval() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSchedulerID(id) {
			FragmentError(w, r, "unknown job")
			return
		}
		label := schedulerJobLabel(id)
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		hours, _ := strconv.Atoi(r.FormValue("hours"))
		mins, _ := strconv.Atoi(r.FormValue("minutes"))
		if hours < 0 {
			hours = 0
		}
		if mins < 0 {
			mins = 0
		}
		if mins > 59 {
			mins = 59
		}
		totalMinutes := hours*60 + mins

		schemaEntry, _ := store.GetConfigSchemaEntry(r.Context(), d.Pool, "jobs", id+"_interval_minutes")
		desc := ""
		if schemaEntry != nil && schemaEntry.Description != nil {
			desc = *schemaEntry.Description
		}

		if err := store.SetServiceConfig(r.Context(), d.Pool, "jobs", id+"_interval_minutes", strconv.Itoa(totalMinutes), false, nil, d.EncKey); err != nil {
			FragmentError(w, r, "failed to update config")
			return
		}

		w.Header().Set("Content-Type", "text/html")
		admintempl.SchedulerIntervalSection(id, label, totalMinutes, desc, "").Render(r.Context(), w)
	}
}
