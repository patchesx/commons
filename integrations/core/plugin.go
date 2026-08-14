package core

import "commons/plugin"

// CorePlugin registers built-in pipeline action types that are not tied to any
// specific integration: conditions, variables, logging, and HTTP requests.
type CorePlugin struct{}

func init() {
	plugin.Register(&CorePlugin{})
}

func (p *CorePlugin) Name() string    { return "core" }
func (p *CorePlugin) Label() string   { return "Core" }
func (p *CorePlugin) Version() string { return "1.0.0" }

func (p *CorePlugin) Provides() []string { return nil }

func (p *CorePlugin) Migrations() []plugin.Migration { return nil }

func (p *CorePlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	plugin.RegisterActionType(&ConditionAction{})
	plugin.RegisterActionType(&SetVariableAction{})
	plugin.RegisterActionType(&LogAction{})
	plugin.RegisterActionType(&HTTPRequestAction{})
	plugin.RegisterActionType(&DelayAction{})
	plugin.RegisterActionType(&ForEachAction{pool: pool})
	return nil
}
