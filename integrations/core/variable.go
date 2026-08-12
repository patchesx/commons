package core

import (
	"context"
	"fmt"

	"commons/plugin"
)

// SetVariableAction implements plugin.ActionType for "core.set_variable".
// It sets a named key in the data map to a value (supports {{key}} references).
type SetVariableAction struct{}

func (a *SetVariableAction) ID() string                     { return "core.set_variable" }
func (a *SetVariableAction) Label() string                  { return "Set Variable" }
func (a *SetVariableAction) RequiredCapabilities() []string { return nil }
func (a *SetVariableAction) OutputSchema() []plugin.DataFieldDef {
	return nil
}
func (a *SetVariableAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "name", Label: "Variable Name", Type: "text", Required: true,
			Description: "Name of the data map key to set."},
		{Key: "value", Label: "Value", Type: "text", Required: true, Dynamic: true,
			Description: "Value to set. Supports {{key}} references."},
	}
}

func (a *SetVariableAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	name := getString(params, "name")
	value := getString(params, "value")
	if name == "" {
		return nil, fmt.Errorf("core.set_variable: name is required")
	}
	return map[string]any{name: value}, nil
}
