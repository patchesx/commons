package plugin

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// Plugin is the interface all integration plugins must implement.
type Plugin interface {
	// Name returns the unique identifier for this plugin, e.g. "zoom", "slack".
	Name() string

	// Version returns the plugin's semver string.
	Version() string

	// Provides returns the capability strings this plugin makes available,
	// e.g. ["zoom.recordings", "zoom.scheduling"].
	Provides() []string

	// Label returns the human-readable display name for this plugin, e.g. "Zoom", "Slack".
	// Used by the admin UI to label integration cards and nav items.
	Label() string

	// Migrations returns plugin-owned SQL migrations. The runner applies
	// missing ones (keyed by plugin name + version) after core migrations.
	Migrations() []Migration

	// Init is called once at startup after all plugins are registered.
	// Plugins use it to register routes, scheduled jobs, and job detail providers.
	Init(ctx PluginContext) error
}

// NavItemReg is a nav item registered by a plugin for the admin sidebar.
type NavItemReg struct {
	Label string
	Path  string
}

// SettingsCardReg records a plugin-registered settings card fragment.
type SettingsCardReg struct {
	Group string // "integrations" or "features"
	Path  string
}

// PluginContext provides core services to plugins during Init.
type PluginContext interface {
	DB() *pgxpool.Pool
	EncKey() []byte
	Config() store.ConfigGetter
	Notifier() platform.Notifier
	MemberInterface() platform.MemberInterface
	RegisteredPlugins() []Plugin
	HasCapability(cap string) bool

	// RecordingStreamer returns the registered recording streamer, or nil if none.
	RecordingStreamer() platform.RecordingStreamer

	// SetRecordingStreamer registers the recording streamer implementation.
	// Called by the recording source plugin (e.g. ZoomPlugin) during Init.
	// Video hosts and storage backends call this to fetch recording files
	// without knowing anything about the source's CDN or auth scheme.
	SetRecordingStreamer(s platform.RecordingStreamer)

	// MeetingScheduler returns the registered meeting scheduler, or nil if none.
	MeetingScheduler() platform.MeetingScheduler

	// SetMeetingScheduler registers the meeting scheduler implementation.
	// Called by the scheduling integration plugin (e.g. ZoomPlugin) during Init.
	SetMeetingScheduler(s platform.MeetingScheduler)

	// SetNotifier stores a platform.Notifier for use by the rest of the system.
	// Called by the chat integration plugin (e.g. SlackPlugin) during Init.
	// Deprecated: use AddNotifier; kept for backwards compatibility.
	SetNotifier(n platform.Notifier)

	// AddNotifier appends a notifier to the composite so multiple chat
	// integrations (Slack, Discord, etc.) can all receive notifications.
	AddNotifier(n platform.Notifier)

	// Route registration. Plugins call these during Init to register HTTP handlers.
	// method is the HTTP verb ("GET", "POST", etc.); pattern is the URL path.
	RegisterRoute(method, pattern string, handler http.HandlerFunc)
	RegisterAuthRoute(method, pattern string, handler http.HandlerFunc)

	// Job cancellation registry. Plugins call RegisterJob when a long-running job
	// starts and UnregisterJob when it ends. CancelJob cancels a registered job context.
	RegisterJob(id string, cancel context.CancelFunc)
	UnregisterJob(id string)
	CancelJob(id string) bool

	// RegisterScheduledJob has been replaced by the scheduled trigger system
	// (scheduler/ package + scheduled_trigger_config table). Plugins that need
	// scheduled execution should seed a managed scheduled trigger during Init.

	// RegisterNavItem adds a navigation item to the admin sidebar.
	// Called during Init; items appear after the standard nav groups.
	RegisterNavItem(label, path string)

	// RegisterSettingsCard mounts handler at path (auth-protected) and registers it
	// as an extra settings card for the given group ("integrations" or "features").
	// The card is included in the corresponding admin page as an HTMX-loaded fragment.
	RegisterSettingsCard(group, path string, handler http.HandlerFunc)

	// ExtraNavItems returns nav items registered during Init.
	ExtraNavItems() []NavItemReg

	// ExtraSettingsCards returns settings cards registered during Init.
	ExtraSettingsCards() []SettingsCardReg

	// RegisterIntegrationCard registers a plugin-owned card for the Integrations page.
	// The card absorbs all integration rows whose type is in spec.ReplacesTypes.
	RegisterIntegrationCard(spec IntegrationCardSpec)

	// ExtraIntegrationCards returns integration cards registered during Init.
	ExtraIntegrationCards() []IntegrationCardSpec
}

var registry []Plugin

// Register adds a plugin to the registry. Intended to be called from each
// plugin package's init() function so registration is automatic on import.
func Register(p Plugin) {
	registry = append(registry, p)
}

// All returns the full list of registered plugins.
func All() []Plugin {
	return registry
}

// SetupProvider is an optional interface plugins can implement to provide
// a guided setup section on their integration detail page.
// The handler is auto-mounted at GET /admin/fragments/integrations/{name}/setup
// by InitAll after the plugin's Init returns.
type SetupProvider interface {
	SetupHandler() http.HandlerFunc
}

// setupRegistry records which plugin slugs have a setup section.
var setupRegistry = map[string]bool{}

// HasSetup reports whether the plugin with the given name (Plugin.Name()) implements SetupProvider.
func HasSetup(slug string) bool { return setupRegistry[slug] }

// LabelForName returns the Label() of the registered plugin with the given Name(),
// or the name itself if no matching plugin is found.
func LabelForName(name string) string {
	for _, p := range registry {
		if p.Name() == name {
			return p.Label()
		}
	}
	return name
}

// InitAll calls Init on each registered plugin in registration order.
// If a plugin implements SetupProvider, its handler is auto-mounted at
// GET /admin/fragments/integrations/{name}/setup and recorded in setupRegistry.
// Returns the first error encountered; a failed plugin init is fatal.
func InitAll(ctx PluginContext) error {
	for _, p := range registry {
		SetInitPlugin(p.Name())
		if err := p.Init(ctx); err != nil {
			ClearInitPlugin()
			return fmt.Errorf("plugin %s: %w", p.Name(), err)
		}
		ClearInitPlugin()
		if sp, ok := p.(SetupProvider); ok {
			ctx.RegisterAuthRoute("GET",
				"/admin/fragments/integrations/"+p.Name()+"/setup",
				sp.SetupHandler())
			setupRegistry[p.Name()] = true
		}
		log.Printf("plugin: %s v%s initialized", p.Name(), p.Version())
	}
	return nil
}
