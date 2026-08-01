package google

import (
	"context"

	"commons/plugin"
	"commons/store"
)

// GooglePlugin is the umbrella plugin for all Google-backed services (YouTube, Drive).
// It registers the Integrations page card for Google credentials and OAuth status.
type GooglePlugin struct{}

func init() { plugin.Register(&GooglePlugin{}) }

func (p *GooglePlugin) Name() string               { return "google" }
func (p *GooglePlugin) Label() string              { return "Google" }
func (p *GooglePlugin) Version() string            { return "1.0.0" }
func (p *GooglePlugin) Migrations() []plugin.Migration { return nil }
func (p *GooglePlugin) Provides() []string         { return []string{"google.oauth"} }

func (p *GooglePlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	// Register the Integrations page card.
	// ReplacesTypes absorbs youtube, gdrive, and google integration rows — one card.
	pctx.RegisterIntegrationCard(plugin.IntegrationCardSpec{
		ReplacesTypes:   []string{"youtube", "gdrive", "google"},
		Name:            "Google",
		ConfigService:   "google",
		OAuthConnectURL: "/auth/google/connect",
		GetOAuthStatus: func(ctx context.Context) (bool, string) {
			email, _ := store.GetServiceConfig(ctx, pool, "google", "connected_email", encKey)
			return email != "", email
		},
	})

	return nil
}
