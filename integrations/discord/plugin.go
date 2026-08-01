package discord

import (
	"context"
	"log"
	"time"

	"commons/plugin"
	"commons/store"
)

// DiscordPlugin registers the Discord integration with the plugin system.
type DiscordPlugin struct{}

func init() {
	plugin.Register(&DiscordPlugin{})
}

func (p *DiscordPlugin) Name() string    { return "discord" }
func (p *DiscordPlugin) Label() string   { return "Discord" }
func (p *DiscordPlugin) Version() string { return "1.0.0" }
func (p *DiscordPlugin) Provides() []string {
	return []string{"discord.notify", "discord.app_home"}
}

// Init performs Discord package-level initialization: sets the notifier on the
// plugin context, registers the webhook processor, action types, API routes,
// scheduled jobs, and starts the Gateway.
func (p *DiscordPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	Init(pool, encKey)

	pctx.AddNotifier(NewNotifier(pool))

	// Register the interactions webhook processor.
	proc := &DiscordInteractionsProcessor{pool: pool, encKey: encKey}
	plugin.RegisterProcessor(proc)
	if err := store.SeedWebhookProcessorType(context.Background(), pool, proc.Type(), proc.Label()); err != nil {
		log.Printf("discord/plugin: failed to seed interactions processor type: %v", err)
	}

	// Register action types.
	plugin.RegisterActionType(&ChannelMessageAction{})
	plugin.RegisterActionType(&DirectMessageAction{})
	plugin.RegisterActionType(&HandleInteractionsAction{pool: pool, encKey: encKey})

	// Register API routes.
	pctx.RegisterAuthRoute("GET", "/api/discord/channels", HandleListDiscordChannels())
	pctx.RegisterAuthRoute("POST", "/api/discord/register-command", HandleRegisterCommand(pool, encKey))

	// Register the slash command card on the Settings page.
	pctx.RegisterSettingsCard("settings", "/admin/fragments/settings/discord-commands", HandleCommandCard(pool, encKey))

	// Register scheduled jobs.
	pctx.RegisterScheduledJob(
		"discord_user_sync_enabled", "discord_user_sync_interval_minutes",
		1*time.Minute, func() {
			SyncAllUsers(context.Background(), pool, encKey)
		},
	)

	// Start the Gateway WebSocket in the background.
	go func() {
		if err := StartGateway(context.Background(), pool, encKey); err != nil {
			log.Printf("discord/plugin: gateway error: %v", err)
		}
	}()

	return nil
}
