package solidaritytech

import (
	"commons/plugin"
)

// SolidarityPlugin registers the Solidarity Tech integration with the plugin system.
// It is an API-driven integration: it exposes pipeline actions that look up SolidarityTech
// user profiles and update their custom properties, tying profiles to member records via
// user_identities (provider="solidaritytech").
type SolidarityPlugin struct{}

func init() {
	plugin.Register(&SolidarityPlugin{})
}

func (p *SolidarityPlugin) Name() string    { return "solidaritytech" }
func (p *SolidarityPlugin) Label() string   { return "Solidarity Tech" }
func (p *SolidarityPlugin) Version() string { return "1.0.0" }

func (p *SolidarityPlugin) Migrations() []plugin.Migration { return nil }

func (p *SolidarityPlugin) Provides() []string {
	return []string{"solidaritytech.api"}
}

// Init registers the SolidarityTech pipeline action types.
func (p *SolidarityPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	plugin.RegisterActionType(&LookupUserAction{pool: pool, encKey: encKey})
	plugin.RegisterActionType(&SetCustomPropertyAction{pool: pool, encKey: encKey})

	return nil
}
