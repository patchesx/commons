package core

import (
	"context"
	"fmt"
	"strings"

	"commons/plugin"
)

// ConditionAction implements plugin.ActionType for "core.condition".
// It evaluates a value against an operator and sets a boolean flag in the data map.
// The field param is dynamic (supports {{key}} references), so by the time Execute
// runs, it contains the resolved value to evaluate — not a data map key name.
type ConditionAction struct{}

func (a *ConditionAction) ID() string                     { return "core.condition" }
func (a *ConditionAction) Label() string                  { return "Set Condition Flag" }
func (a *ConditionAction) RequiredCapabilities() []string { return nil }
func (a *ConditionAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "condition_result", Label: "Condition Result", Type: "boolean"},
	}
}
func (a *ConditionAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "field", Label: "Value to Check", Type: "text", Required: true, Dynamic: true,
			Description: "The value to evaluate. Supports {{key}} references, e.g. {{solidaritytech_user_id}}."},
		{Key: "operator", Label: "Operator", Type: "select", Required: true,
			Options: []plugin.SelectOption{
				{Value: "exists", Label: "Exists (not empty)"},
				{Value: "not_exists", Label: "Does not exist (empty)"},
				{Value: "eq", Label: "Equals"},
				{Value: "neq", Label: "Not equals"},
				{Value: "contains", Label: "Contains"},
				{Value: "not_contains", Label: "Does not contain"},
			}},
		{Key: "value", Label: "Compare Value", Type: "text", Dynamic: true,
			Description: "Value to compare against (for eq, neq, contains). Supports {{key}}."},
		{Key: "output_key", Label: "Output Key", Type: "text", Required: true,
			Default: "condition_result",
			Description: "Name of the boolean flag to set in the data map."},
	}
}

func (a *ConditionAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	field := getString(params, "field")
	operator := getString(params, "operator")
	value := getString(params, "value")
	outputKey := getString(params, "output_key")
	if outputKey == "" {
		outputKey = "condition_result"
	}

	result := evaluateOperator(field, operator, value)
	return map[string]any{outputKey: result}, nil
}

// evaluateOperator checks field against value using the given operator.
func evaluateOperator(field, operator, value string) bool {
	switch operator {
	case "exists":
		return field != ""
	case "not_exists":
		return field == ""
	case "eq":
		return field == value
	case "neq":
		return field != value
	case "contains":
		return strings.Contains(field, value)
	case "not_contains":
		return !strings.Contains(field, value)
	}
	return true // unknown operator — fail open
}

func getString(params map[string]any, key string) string {
	if v, ok := params[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}
