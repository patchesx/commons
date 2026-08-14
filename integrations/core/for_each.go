package core

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/pipelineutil"
	"commons/plugin"
	"commons/store"
)

// ForEachAction implements plugin.ActionType for "core.for_each".
// It iterates over an array in the data map, executing a group of body actions
// for each item. Body actions are pipeline_actions with action_group matching
// the group param, ordered by position.
//
// The items array is read from the data map key specified in the items param
// (supports {{key}} references). Each item is merged into the data map under
// item_key (default: "item") for the duration of that iteration.
//
// On error, behavior depends on on_error: "continue" (default) logs and proceeds
// to the next item; "abort" stops the loop and returns the error.
//
// If a body action returns a PauseSignal (e.g. core.delay), the for_each action
// propagates it to the runner, which pauses the pipeline. On resume, the entire
// loop re-executes from the first item (v1 simplification).
type ForEachAction struct {
	pool *pgxpool.Pool
}

func (a *ForEachAction) ID() string                     { return "core.for_each" }
func (a *ForEachAction) Label() string                  { return "For Each (Loop)" }
func (a *ForEachAction) RequiredCapabilities() []string { return nil }
func (a *ForEachAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "loop_count", Label: "Items Processed", Type: "number"},
		{Key: "loop_errors", Label: "Items Failed", Type: "number"},
	}
}
func (a *ForEachAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "items", Label: "Items Array", Type: "text", Required: true, Dynamic: true,
			Description: "The data map key holding the array to iterate, e.g. {{members}}."},
		{Key: "item_key", Label: "Item Variable Name", Type: "text", Required: true,
			Default: "item",
			Description: "Name to expose each array element as in the data map, e.g. 'member' → {{member.id}}."},
		{Key: "group", Label: "Action Group", Type: "text", Required: true,
			Description: "The action_group name that links body actions to this loop."},
		{Key: "on_error", Label: "On Error", Type: "select",
			Options: []plugin.SelectOption{
				{Value: "continue", Label: "Continue (log and proceed)"},
				{Value: "abort", Label: "Abort (stop loop)"},
			}},
	}
}

func (a *ForEachAction) Execute(ctx context.Context, params map[string]any, ac plugin.ActionContext) (map[string]any, error) {
	items, ok := params["items"].([]any)
	if !ok {
		return map[string]any{"loop_count": 0, "loop_errors": 0}, nil
	}

	itemKey := getString(params, "item_key")
	if itemKey == "" {
		itemKey = "item"
	}
	group := getString(params, "group")
	if group == "" {
		return nil, fmt.Errorf("core.for_each: group is required")
	}
	triggerID := getString(params, "trigger_id")
	if triggerID == "" {
		return nil, fmt.Errorf("core.for_each: trigger_id is required")
	}
	onError := getString(params, "on_error")
	if onError == "" {
		onError = "continue"
	}

	// Load body actions for this group.
	bodyActions, err := store.ListActionsByGroup(ctx, a.pool, triggerID, group)
	if err != nil {
		return nil, fmt.Errorf("core.for_each: load body actions: %w", err)
	}
	if len(bodyActions) == 0 {
		log.Printf("core.for_each: no body actions found for group %q on trigger %s", group, triggerID)
		return map[string]any{"loop_count": 0, "loop_errors": 0}, nil
	}

	loopCount := 0
	loopErrors := 0

	for _, item := range items {
		// Create a data map copy with the item merged in.
		itemData := make(map[string]any, len(params))
		for k, v := range params {
			itemData[k] = v
		}
		itemData[itemKey] = item

		// Execute body actions sequentially for this item.
		var itemFailed bool
		for _, action := range bodyActions {
			at, ok := plugin.GetActionType(action.Type)
			if !ok {
				log.Printf("core.for_each: body action %s type %q not registered — skipping", action.ID, action.Type)
				continue
			}

			// Check per-action condition.
			if action.Condition != nil && !evaluateActionCondition(itemData, *action.Condition) {
				continue
			}

			resolved := pipelineutil.ResolveActionParams(action.Params, itemData)
			output, err := at.Execute(ctx, resolved, ac)
			if err != nil {
				// Propagate PauseSignal to the runner.
				if _, ok := err.(plugin.PauseSignal); ok {
					return nil, err
				}

				log.Printf("core.for_each: item %d action %s failed: %v", loopCount, action.ID, err)
				itemFailed = true
				if onError == "abort" {
					return nil, fmt.Errorf("core.for_each: item %d action %s: %w", loopCount, action.ID, err)
				}
				break // skip remaining body actions for this item
			}

			// Merge output into item data for subsequent body actions.
			for k, v := range output {
				itemData[k] = v
			}
		}

		if itemFailed {
			loopErrors++
		}
		loopCount++
	}

	log.Printf("core.for_each: group=%s processed %d items, %d errors", group, loopCount, loopErrors)
	return map[string]any{
		"loop_count":  loopCount,
		"loop_errors": loopErrors,
	}, nil
}

// evaluateActionCondition checks whether the data map satisfies a per-action condition.
// This is a simplified version of pipeline.EvaluateCondition for use inside the for_each loop.
func evaluateActionCondition(data map[string]any, cond store.ActionCondition) bool {
	val, valExists := data[cond.Field]
	fieldPresent := valExists && val != nil

	switch cond.Operator {
	case "exists":
		return fieldPresent
	case "not_exists":
		return !fieldPresent
	}

	if !fieldPresent {
		return false
	}

	if cond.Value == nil {
		return true
	}

	dataStr := fmt.Sprintf("%v", val)
	switch cond.Operator {
	case "eq":
		return dataStr == *cond.Value
	case "neq":
		return dataStr != *cond.Value
	case "contains":
		return strings.Contains(dataStr, *cond.Value)
	}

	return true
}
