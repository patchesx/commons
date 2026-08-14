package adminui

import (
	"encoding/json"
	"log"
	"net/http"

	"commons/pipeline"
	"commons/plugin"
	"commons/store"
	admintempl "commons/web/templ"
)

// --- List page ---

func (d Deps) TriggersPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		webhooks, err := store.ListWebhooks(ctx, d.Pool, d.EncKey)
		if err != nil {
			http.Error(w, "failed to load webhooks", http.StatusInternalServerError)
			return
		}

		pluginEnabled := map[string]bool{}
		pluginLabels := map[string]string{}
		for _, wh := range webhooks {
			if wh.ManagedBy == nil || *wh.ManagedBy == "" {
				continue
			}
			key := *wh.ManagedBy
			if _, seen := pluginEnabled[key]; !seen {
				integ, err := store.GetIntegrationByType(ctx, d.Pool, key)
				pluginEnabled[key] = err == nil && integ != nil && integ.Enabled
				pluginLabels[key] = plugin.LabelForName(key)
			}
		}

		var eventGroups []admintempl.EventTriggerGroup
		eventCount := 0
		for _, tt := range plugin.ListTriggerTypes() {
			pipelines, _ := store.ListAllTriggerSourcesByType(ctx, d.Pool, tt.ID())
			var eps []admintempl.EventPipeline
			for _, p := range pipelines {
				actions, _ := store.ListPipelineActions(ctx, d.Pool, p.ID, "success")
				eps = append(eps, admintempl.EventPipeline{Source: p, Actions: actions})
			}
			eventCount += len(pipelines)
			eventGroups = append(eventGroups, admintempl.EventTriggerGroup{TriggerType: tt, Pipelines: eps})
		}

		channels, users, locs, cats := loadActionFormDeps(ctx, d.Pool)
		scheduledTriggers, _ := store.ListScheduledTriggers(ctx, d.Pool)
		data := admintempl.TriggersPageData{
			Webhooks:           webhooks,
			EventGroups:        eventGroups,
			EventCount:         eventCount,
			ScheduledTriggers:  scheduledTriggers,
			ActionTypes:        enabledActionTypes(ctx, d.Pool),
			ProcessorTypes:     plugin.ListProcessors(),
			Channels:           channels,
			Users:              users,
			StorageLocations:   locs,
			ResourceCategories: cats,
			PluginEnabled:      pluginEnabled,
			PluginLabels:       pluginLabels,
		}
		admintempl.TriggersPage(data).Render(ctx, w)
	}
}

// --- Detail page ---

func (d Deps) TriggerDetailPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		ts, err := store.GetTriggerSourceByID(ctx, d.Pool, id)
		if err != nil || ts == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		channels, users, locs, cats := loadActionFormDeps(ctx, d.Pool)
		data := admintempl.TriggerDetailData{
			Source:             *ts,
			ActionTypes:        enabledActionTypes(ctx, d.Pool),
			Channels:           channels,
			Users:              users,
			StorageLocations:   locs,
			ResourceCategories: cats,
		}
		if ts.Type == "http.webhook" {
			wh, err := store.GetWebhookByID(ctx, d.Pool, d.EncKey, id)
			if err != nil || wh == nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			pluginEnabled := map[string]bool{}
			pluginLabels := map[string]string{}
			if wh.ManagedBy != nil && *wh.ManagedBy != "" {
				key := *wh.ManagedBy
				integ, err := store.GetIntegrationByType(ctx, d.Pool, key)
				pluginEnabled[key] = err == nil && integ != nil && integ.Enabled
				pluginLabels[key] = plugin.LabelForName(key)
			}
			data.IsHTTP = true
			data.Webhook = wh
			data.ProcessorTypes = plugin.ListProcessors()
			data.PluginEnabled = pluginEnabled
			data.PluginLabels = pluginLabels
		} else if ts.Type == "scheduled" {
			st, err := store.GetScheduledTriggerByID(ctx, d.Pool, id)
			if err != nil || st == nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			actions, _ := store.ListPipelineActions(ctx, d.Pool, id, "success")
			pluginEnabled := map[string]bool{}
			pluginLabels := map[string]string{}
			if st.ManagedBy != nil && *st.ManagedBy != "" {
				key := *st.ManagedBy
				integ, err := store.GetIntegrationByType(ctx, d.Pool, key)
				pluginEnabled[key] = err == nil && integ != nil && integ.Enabled
				pluginLabels[key] = plugin.LabelForName(key)
			}
			data.IsScheduled = true
			data.Scheduled = st
			data.Actions = actions
			data.PluginEnabled = pluginEnabled
			data.PluginLabels = pluginLabels
		} else {
			tt, ok := plugin.GetTriggerType(ts.Type)
			if !ok {
				http.Error(w, "unknown trigger type", http.StatusNotFound)
				return
			}
			actions, _ := store.ListPipelineActions(ctx, d.Pool, id, "success")
			data.TriggerType = tt
			data.Actions = actions
		}
		admintempl.TriggerDetailPage(data).Render(ctx, w)
	}
}

// --- Webhook create/edit form ---

func (d Deps) TriggerWebhookForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		admintempl.WebhookFormModalContent(nil, plugin.ListProcessors(), "").Render(r.Context(), w)
	}
}

func (d Deps) TriggerWebhookEditForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wh, err := store.GetWebhookByID(r.Context(), d.Pool, d.EncKey, r.PathValue("id"))
		if err != nil || wh == nil {
			FragmentError(w, r, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.WebhookFormModalContent(wh, plugin.ListProcessors(), "").Render(r.Context(), w)
	}
}

func (d Deps) TriggerWebhookCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		slug, name := r.FormValue("slug"), r.FormValue("name")
		if slug == "" || name == "" {
			w.Header().Set("Content-Type", "text/html")
			admintempl.WebhookFormModalContent(nil, plugin.ListProcessors(), "Slug and name are required.").Render(r.Context(), w)
			return
		}
		newWH, err := store.CreateWebhook(r.Context(), d.Pool, d.EncKey, store.CreateWebhookParams{
			Slug:             slug,
			Name:             name,
			Description:      r.FormValue("description"),
			Enabled:          r.FormValue("enabled") == "true",
			VerificationMode: r.FormValue("verification_mode"),
			Secret:           r.FormValue("secret"),
			SecretHeader:     r.FormValue("signature_header"),
			ProcessorType:    r.FormValue("processor_type"),
		})
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			admintempl.WebhookFormModalContent(nil, plugin.ListProcessors(), "Failed to create webhook.").Render(r.Context(), w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+newWH.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) TriggerWebhookUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		if r.FormValue("clear_secret") == "true" {
			if err := store.ClearWebhookSecret(ctx, d.Pool, id); err != nil {
				FragmentError(w, r, "failed to clear secret")
				return
			}
		}
		var procType *string
		if pt := r.FormValue("processor_type"); pt != "" {
			procType = &pt
		}
		if _, err := store.UpdateWebhook(ctx, d.Pool, d.EncKey, id, store.UpdateWebhookParams{
			Name:             r.FormValue("name"),
			Description:      r.FormValue("description"),
			Enabled:          r.FormValue("enabled") == "true",
			VerificationMode: r.FormValue("verification_mode"),
			Secret:           r.FormValue("secret"),
			SecretHeader:     r.FormValue("signature_header"),
			ProcessorType:    procType,
		}); err != nil {
			FragmentError(w, r, "failed to update webhook")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Event pipeline create ---

func (d Deps) TriggerCreateEventPipeline() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		triggerType := r.FormValue("type")
		name := r.FormValue("name")
		if triggerType == "" {
			FragmentError(w, r, "type required")
			return
		}
		if name == "" {
			name = "New pipeline"
		}
		tt, ok := plugin.GetTriggerType(triggerType)
		if !ok {
			FragmentError(w, r, "unknown trigger type")
			return
		}
		if _, err := store.CreateTriggerSource(ctx, d.Pool, triggerType, name); err != nil {
			FragmentError(w, r, "failed to create pipeline")
			return
		}
		pipelines, _ := store.ListAllTriggerSourcesByType(ctx, d.Pool, triggerType)
		var eps []admintempl.EventPipeline
		for _, p := range pipelines {
			actions, _ := store.ListPipelineActions(ctx, d.Pool, p.ID, "success")
			eps = append(eps, admintempl.EventPipeline{Source: p, Actions: actions})
		}
		group := admintempl.EventTriggerGroup{TriggerType: tt, Pipelines: eps}
		channels, users, locs, cats := loadActionFormDeps(ctx, d.Pool)
		data := &admintempl.TriggersPageData{
			ActionTypes:        enabledActionTypes(ctx, d.Pool),
			Channels:           channels,
			Users:              users,
			StorageLocations:   locs,
			ResourceCategories: cats,
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerEventSectionFragment(group, data).Render(ctx, w)
	}
}

// --- Delete ---

func (d Deps) TriggerDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteWebhook(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers")
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Toggle enabled (API) ---

func (d Deps) TriggerToggleEnabled() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := store.SetTriggerSourceEnabled(ctx, d.Pool, r.PathValue("id"), body.Enabled); err != nil {
			http.Error(w, "failed to update", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Scheduled trigger create/edit form ---

func (d Deps) TriggerScheduledForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		admintempl.ScheduledTriggerFormModalContent(nil, "").Render(r.Context(), w)
	}
}

func (d Deps) TriggerScheduledEditForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := store.GetScheduledTriggerByID(r.Context(), d.Pool, r.PathValue("id"))
		if err != nil || st == nil {
			FragmentError(w, r, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.ScheduledTriggerFormModalContent(st, "").Render(r.Context(), w)
	}
}

func (d Deps) TriggerScheduledCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		schedule := r.FormValue("schedule")
		if name == "" || schedule == "" {
			w.Header().Set("Content-Type", "text/html")
			admintempl.ScheduledTriggerFormModalContent(nil, "Name and schedule are required.").Render(r.Context(), w)
			return
		}
		st, err := store.CreateScheduledTrigger(r.Context(), d.Pool, store.CreateScheduledTriggerParams{
			Name:     name,
			Schedule: schedule,
			Timezone: r.FormValue("timezone"),
			Enabled:  r.FormValue("enabled") == "true",
		})
		if err != nil {
			w.Header().Set("Content-Type", "text/html")
			admintempl.ScheduledTriggerFormModalContent(nil, "Failed to create scheduled trigger.").Render(r.Context(), w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+st.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) TriggerScheduledUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		schedule := r.FormValue("schedule")
		if name == "" || schedule == "" {
			st, _ := store.GetScheduledTriggerByID(r.Context(), d.Pool, id)
			w.Header().Set("Content-Type", "text/html")
			admintempl.ScheduledTriggerFormModalContent(st, "Name and schedule are required.").Render(r.Context(), w)
			return
		}
		st, err := store.UpdateScheduledTrigger(r.Context(), d.Pool, id, store.UpdateScheduledTriggerParams{
			Name:     name,
			Schedule: schedule,
			Timezone: r.FormValue("timezone"),
			Enabled:  r.FormValue("enabled") == "true",
		})
		if err != nil || st == nil {
			FragmentError(w, r, "failed to update scheduled trigger")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Pipeline run re-run ---

func (d Deps) PipelineRunRerun() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		runID := r.PathValue("id")
		fromStep := 0
		if r.FormValue("from_step") == "1" {
			fromStep = 1 // re-run from failed step
		}

		run, err := store.ClonePipelineRun(ctx, d.Pool, runID, fromStep)
		if err != nil || run == nil {
			FragmentError(w, r, "failed to clone pipeline run")
			return
		}

		if err := pipeline.ExecuteRun(ctx, d.Pool, d.EncKey, run, nil); err != nil {
			log.Printf("admin: re-run pipeline %s: %v", runID, err)
			FragmentError(w, r, "failed to execute pipeline run")
			return
		}

		w.Header().Set("HX-Redirect", "/admin/triggers")
		w.WriteHeader(http.StatusNoContent)
	}
}
