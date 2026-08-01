package adminui

import (
	"net/http"
	"strconv"

	"commons/plugin"
	"commons/store"
	admintempl "commons/web/templ"
)

// --- Action fragment handlers ---

func (d Deps) TriggerActionAddForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		channels, users, locs, cats := loadActionFormDeps(ctx, d.Pool)
		availVars := computeTriggerVars(ctx, d.Pool, d.EncKey, sourceID, "")
		runOn := r.URL.Query().Get("run_on")
		if runOn == "" {
			runOn = "success"
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerActionAddForm(sourceID, runOn, enabledActionTypes(ctx, d.Pool), channels, users, locs, cats, availVars).Render(ctx, w)
	}
}

func (d Deps) TriggerActionEditForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		actionID := r.PathValue("aid")
		actions, err := store.ListPipelineActions(ctx, d.Pool, sourceID, "success")
		if err != nil {
			FragmentError(w, r, "failed to load actions")
			return
		}
		var action *store.WebhookAction
		for i := range actions {
			if actions[i].ID == actionID {
				action = &actions[i]
				break
			}
		}
		if action == nil {
			FragmentError(w, r, "action not found")
			return
		}
		channels, users, locs, cats := loadActionFormDeps(ctx, d.Pool)
		availVars := computeTriggerVars(ctx, d.Pool, d.EncKey, sourceID, strconv.Itoa(action.Position))
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerActionEditForm(sourceID, action, enabledActionTypes(ctx, d.Pool), channels, users, locs, cats, availVars).Render(ctx, w)
	}
}

func (d Deps) TriggerActionRow() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		actionID := r.PathValue("aid")
		actions, err := store.ListPipelineActions(ctx, d.Pool, sourceID, "success")
		if err != nil {
			FragmentError(w, r, "failed to load actions")
			return
		}
		var action *store.WebhookAction
		for i := range actions {
			if actions[i].ID == actionID {
				action = &actions[i]
				break
			}
		}
		if action == nil {
			FragmentError(w, r, "action not found")
			return
		}
		ts, _ := store.GetTriggerSourceByID(ctx, d.Pool, sourceID)
		var managedBy *string
		if ts != nil {
			managedBy = ts.ManagedBy
		}
		channels, users, _, _ := loadActionFormDeps(ctx, d.Pool)
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerActionRow(sourceID, *action, managedBy, enabledActionTypes(ctx, d.Pool), channels, users).Render(ctx, w)
	}
}

func (d Deps) TriggerActionParams() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		typeID := r.URL.Query().Get("type")
		positionStr := r.URL.Query().Get("position")
		at := findOrFirstActionType(typeID)
		channels, users, locs, cats := loadActionFormDeps(ctx, d.Pool)
		availVars := computeTriggerVars(ctx, d.Pool, d.EncKey, sourceID, positionStr)
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerActionParamFields(at, nil, channels, users, locs, availVars, cats, sourceID).Render(ctx, w)
	}
}

func (d Deps) TriggerActionCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		actionType := r.FormValue("type")
		if actionType == "" {
			FragmentError(w, r, "type required")
			return
		}
		runOn := r.FormValue("run_on")
		if runOn == "" {
			runOn = "success"
		}
		ts, err := store.GetTriggerSourceByID(ctx, d.Pool, sourceID)
		if err != nil || ts == nil {
			FragmentError(w, r, "trigger source not found")
			return
		}
		var pos int
		if ts.Type == "http.webhook" {
			wh, err := store.GetWebhookByID(ctx, d.Pool, d.EncKey, sourceID)
			if err != nil || wh == nil {
				FragmentError(w, r, "trigger source not found")
				return
			}
			for _, a := range wh.Actions {
				if a.RunOn == runOn {
					pos++
				}
			}
		} else {
			actions, _ := store.ListPipelineActions(ctx, d.Pool, sourceID, "success")
			pos = len(actions)
		}
		if _, err := store.CreateWebhookAction(ctx, d.Pool, sourceID, store.WebhookActionParams{
			Type:     actionType,
			Params:   buildActionParams(r, actionType),
			Position: pos,
			RunOn:    runOn,
		}); err != nil {
			FragmentError(w, r, "failed to create action")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+sourceID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) TriggerActionUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		actionID := r.PathValue("aid")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		actionType := r.FormValue("type")
		pos, _ := strconv.Atoi(r.FormValue("position"))
		runOn := r.FormValue("run_on")
		if runOn == "" {
			runOn = "success"
		}
		if _, err := store.UpdateWebhookAction(ctx, d.Pool, actionID, store.WebhookActionParams{
			Type:     actionType,
			Params:   buildActionParams(r, actionType),
			Position: pos,
			RunOn:    runOn,
		}); err != nil {
			FragmentError(w, r, "failed to update action")
			return
		}
		ts, _ := store.GetTriggerSourceByID(ctx, d.Pool, sourceID)
		var managedBy *string
		if ts != nil {
			managedBy = ts.ManagedBy
		}
		actions, _ := store.ListPipelineActions(ctx, d.Pool, sourceID, runOn)
		var updated *store.WebhookAction
		for i := range actions {
			if actions[i].ID == actionID {
				updated = &actions[i]
				break
			}
		}
		if updated == nil {
			FragmentError(w, r, "action not found after update")
			return
		}
		channels, users, _, _ := loadActionFormDeps(ctx, d.Pool)
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerActionRow(sourceID, *updated, managedBy, enabledActionTypes(ctx, d.Pool), channels, users).Render(ctx, w)
	}
}

func (d Deps) TriggerActionDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sourceID := r.PathValue("id")
		if err := store.DeleteWebhookAction(r.Context(), d.Pool, r.PathValue("aid")); err != nil {
			FragmentError(w, r, "failed to delete action")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+sourceID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Filter fragment handlers ---

func (d Deps) TriggerFilterForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		wh, err := store.GetWebhookByID(ctx, d.Pool, d.EncKey, sourceID)
		if err != nil || wh == nil {
			FragmentError(w, r, "not found")
			return
		}
		schema := webhookProcessorSchema(wh.ProcessorType)
		w.Header().Set("Content-Type", "text/html")
		if fid := r.URL.Query().Get("filterId"); fid != "" {
			var filter *store.WebhookFilter
			for i := range wh.Filters {
				if wh.Filters[i].ID == fid {
					filter = &wh.Filters[i]
					break
				}
			}
			if filter == nil {
				FragmentError(w, r, "filter not found")
				return
			}
			admintempl.TriggerFilterEditForm(sourceID, filter, schema).Render(ctx, w)
		} else {
			admintempl.TriggerFilterAddForm(sourceID, schema).Render(ctx, w)
		}
	}
}

func (d Deps) TriggerFilterCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		field, operator := r.FormValue("field"), r.FormValue("operator")
		if field == "" || operator == "" {
			FragmentError(w, r, "field and operator required")
			return
		}
		wh, err := store.GetWebhookByID(ctx, d.Pool, d.EncKey, sourceID)
		if err != nil || wh == nil {
			FragmentError(w, r, "trigger source not found")
			return
		}
		var val *string
		if v := r.FormValue("value"); v != "" {
			val = &v
		}
		if _, err := store.CreateWebhookFilter(ctx, d.Pool, sourceID, store.WebhookFilterParams{
			Position: len(wh.Filters),
			Field:    field,
			Operator: operator,
			Value:    val,
		}); err != nil {
			FragmentError(w, r, "failed to create filter")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+sourceID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) TriggerFilterRow() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		filterID := r.PathValue("fid")
		wh, err := store.GetWebhookByID(ctx, d.Pool, d.EncKey, sourceID)
		if err != nil || wh == nil {
			FragmentError(w, r, "trigger source not found")
			return
		}
		var filter *store.WebhookFilter
		for i := range wh.Filters {
			if wh.Filters[i].ID == filterID {
				filter = &wh.Filters[i]
				break
			}
		}
		if filter == nil {
			FragmentError(w, r, "filter not found")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerFilterRowDisplay(*wh, *filter, plugin.ListProcessors()).Render(ctx, w)
	}
}

func (d Deps) TriggerFilterUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.PathValue("id")
		filterID := r.PathValue("fid")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		field, operator := r.FormValue("field"), r.FormValue("operator")
		if field == "" || operator == "" {
			FragmentError(w, r, "field and operator required")
			return
		}
		var val *string
		if v := r.FormValue("value"); v != "" {
			val = &v
		}
		pos, _ := strconv.Atoi(r.FormValue("position"))
		if _, err := store.UpdateWebhookFilter(ctx, d.Pool, filterID, store.WebhookFilterParams{
			Position: pos,
			Field:    field,
			Operator: operator,
			Value:    val,
		}); err != nil {
			FragmentError(w, r, "failed to update filter")
			return
		}
		wh, err := store.GetWebhookByID(ctx, d.Pool, d.EncKey, sourceID)
		if err != nil || wh == nil {
			FragmentError(w, r, "trigger source not found")
			return
		}
		var updated *store.WebhookFilter
		for i := range wh.Filters {
			if wh.Filters[i].ID == filterID {
				updated = &wh.Filters[i]
				break
			}
		}
		if updated == nil {
			FragmentError(w, r, "filter not found after update")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.TriggerFilterRowDisplay(*wh, *updated, plugin.ListProcessors()).Render(ctx, w)
	}
}

func (d Deps) TriggerFilterDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sourceID := r.PathValue("id")
		if err := store.DeleteWebhookFilter(r.Context(), d.Pool, r.PathValue("fid")); err != nil {
			FragmentError(w, r, "failed to delete filter")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/triggers/"+sourceID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Message variant (shared, updated to use computeTriggerVars) ---

func (d Deps) TriggerMessageVariant() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sourceID := r.URL.Query().Get("pipeline_id")
		fieldKey := r.URL.Query().Get("key")
		positionStr := r.FormValue("position")
		if positionStr == "" {
			positionStr = r.URL.Query().Get("position")
		}
		indexStr := r.FormValue("variant_next_" + fieldKey)
		if indexStr == "" {
			indexStr = r.URL.Query().Get("variant_next_" + fieldKey)
		}
		index, _ := strconv.Atoi(indexStr)
		availVars := computeTriggerVars(ctx, d.Pool, d.EncKey, sourceID, positionStr)
		channels, _, _, _ := loadActionFormDeps(ctx, d.Pool)
		w.Header().Set("Content-Type", "text/html")
		admintempl.MessageVariantSlot(index, fieldKey, "", channels, availVars).Render(ctx, w)
	}
}
