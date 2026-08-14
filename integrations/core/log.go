package core

import (
	"context"
	"log"

	"commons/plugin"
)

// LogAction implements plugin.ActionType for "core.log".
// It logs a message (supports {{key}} references) for debugging pipelines.
type LogAction struct{}

func (a *LogAction) ID() string                     { return "core.log" }
func (a *LogAction) Label() string                  { return "Log Message" }
func (a *LogAction) RequiredCapabilities() []string { return nil }
func (a *LogAction) OutputSchema() []plugin.DataFieldDef {
	return nil
}
func (a *LogAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "message", Label: "Message", Type: "text", Required: true, Dynamic: true,
			Description: "Message to log. Supports {{key}} references."},
	}
}

func (a *LogAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	msg := getString(params, "message")
	log.Printf("pipeline [core.log]: %s", msg)
	return nil, nil
}
