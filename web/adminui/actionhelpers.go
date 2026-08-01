package adminui

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	slackpkg "commons/integrations/slack"
	"commons/plugin"
	"commons/store"
	admintempl "commons/web/templ"
)

func loadActionFormDeps(ctx context.Context, pool *pgxpool.Pool) ([]admintempl.WHChannel, []admintempl.WHUser, []store.StorageLocation, []string) {
	var whChannels []admintempl.WHChannel
	if chs, err := slackpkg.ListChannels(ctx); err == nil {
		for _, c := range chs {
			whChannels = append(whChannels, admintempl.WHChannel{ID: c.ID, Name: c.Name})
		}
	}
	users, _ := store.ListUsers(ctx, pool)
	var whUsers []admintempl.WHUser
	for _, u := range users {
		whUsers = append(whUsers, admintempl.WHUser{
			ID:          u.ID,
			DisplayName: u.DisplayName,
			SlackID:     u.SlackID,
		})
	}
	sort.Slice(whUsers, func(i, j int) bool {
		return whUsers[i].DisplayName < whUsers[j].DisplayName
	})
	locs, _ := store.ListStorageLocations(ctx, pool)
	cats, _ := store.ListCategories(ctx, pool)
	return whChannels, whUsers, locs, cats
}

// enabledActionTypes returns all action types whose parent integration is
// enabled (or that are core actions not tied to any integration).
func enabledActionTypes(ctx context.Context, pool *pgxpool.Pool) []plugin.ActionTypeInfo {
	all := plugin.ListActionTypes()
	integrations, err := store.ListIntegrations(ctx, pool)
	if err != nil {
		return all
	}
	enabled := make(map[string]bool, len(integrations))
	for _, i := range integrations {
		if i.Enabled {
			enabled[i.Type] = true
		}
	}
	out := all[:0:0]
	for _, at := range all {
		if at.PluginName == "" || enabled[at.PluginName] {
			out = append(out, at)
		}
	}
	return out
}

func findOrFirstActionType(id string) *plugin.ActionTypeInfo {
	types := plugin.ListActionTypes()
	for i := range types {
		if types[i].ID == id {
			return &types[i]
		}
	}
	if len(types) > 0 {
		return &types[0]
	}
	return nil
}

func buildActionParams(r *http.Request, actionType string) map[string]any {
	at := findOrFirstActionType(actionType)
	if at == nil {
		return nil
	}
	params := make(map[string]any, len(at.ParamSchema))
	for _, def := range at.ParamSchema {
		if def.Type == "message_variants" {
			var variants []any
			for i := range 20 {
				v := r.FormValue(fmt.Sprintf("param_%s_%d", def.Key, i))
				if v != "" {
					variants = append(variants, v)
				}
			}
			if len(variants) > 0 {
				params[def.Key] = variants
			}
		} else {
			val := r.FormValue("param_" + def.Key)
			if val != "" || def.Type == "boolean" {
				params[def.Key] = val
			}
		}
	}
	return params
}

func webhookProcessorSchema(procType *string) []plugin.DataFieldDef {
	if procType == nil {
		return nil
	}
	for _, p := range plugin.ListProcessors() {
		if p.Type == *procType {
			return p.DataSchema
		}
	}
	return nil
}

// computeTriggerVars returns the available template variables for a trigger source
// of any type. For HTTP webhooks, variables come from the processor's DataSchema;
// for internal trigger types, they come from the TriggerType's DataSchema. Prior
// action outputs (by position) are appended in both cases.
func computeTriggerVars(ctx context.Context, pool *pgxpool.Pool, encKey []byte, sourceID, positionStr string) []plugin.DataFieldDef {
	var vars []plugin.DataFieldDef
	ts, err := store.GetTriggerSourceByID(ctx, pool, sourceID)
	if err != nil || ts == nil {
		return vars
	}
	cutoff := -1
	if positionStr != "" {
		if p, parseErr := strconv.Atoi(positionStr); parseErr == nil {
			cutoff = p
		}
	}
	if ts.Type == "http.webhook" {
		wh, err := store.GetWebhookByID(ctx, pool, encKey, sourceID)
		if err != nil || wh == nil {
			return vars
		}
		if wh.ProcessorType != nil {
			if p, ok := plugin.GetProcessor(*wh.ProcessorType); ok {
				vars = append(vars, p.DataSchema()...)
			}
		}
		for _, a := range wh.Actions {
			if cutoff >= 0 && a.Position >= cutoff {
				continue
			}
			if at, ok := plugin.GetActionType(a.Type); ok {
				vars = append(vars, at.OutputSchema()...)
			}
		}
	} else {
		if tt, ok := plugin.GetTriggerType(ts.Type); ok {
			vars = append(vars, tt.DataSchema()...)
		}
		actions, err := store.ListPipelineActions(ctx, pool, sourceID, "success")
		if err != nil {
			return vars
		}
		for _, a := range actions {
			if cutoff >= 0 && a.Position >= cutoff {
				continue
			}
			if at, ok := plugin.GetActionType(a.Type); ok {
				vars = append(vars, at.OutputSchema()...)
			}
		}
	}
	return vars
}
