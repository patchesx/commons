package core

import (
	"context"
	"fmt"
	"time"

	"commons/plugin"
)

// DelayAction implements plugin.ActionType for "core.delay".
// It pauses the pipeline for the specified duration. The runner persists the
// pipeline run state and the resume scheduler resumes it when the duration expires.
type DelayAction struct{}

func (a *DelayAction) ID() string                     { return "core.delay" }
func (a *DelayAction) Label() string                  { return "Delay" }
func (a *DelayAction) RequiredCapabilities() []string { return nil }
func (a *DelayAction) OutputSchema() []plugin.DataFieldDef {
	return nil
}
func (a *DelayAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "duration", Label: "Duration", Type: "text", Required: true, Dynamic: true,
			Description: "How long to wait. Supports: \"30s\", \"5m\", \"1h\", or {{key}} references."},
	}
}

func (a *DelayAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	durationStr := getString(params, "duration")
	d, err := time.ParseDuration(durationStr)
	if err != nil {
		return nil, fmt.Errorf("core.delay: invalid duration %q: %w", durationStr, err)
	}
	return nil, plugin.PauseSignal{ResumeAt: time.Now().Add(d)}
}
