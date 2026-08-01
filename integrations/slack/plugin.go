package slack

import (
	"context"
	"log"
	"time"

	"commons/plugin"
	"commons/store"
)

// SlackPlugin registers the Slack integration with the plugin system.
type SlackPlugin struct{}

func init() {
	plugin.Register(&SlackPlugin{})
}

func (p *SlackPlugin) Name() string    { return "slack" }
func (p *SlackPlugin) Label() string   { return "Slack" }
func (p *SlackPlugin) Version() string { return "1.0.0" }
func (p *SlackPlugin) Provides() []string {
	return []string{"slack.notify", "slack.app_home"}
}

// Init performs Slack package-level initialization, sets the notifier on the
// plugin context, registers webhook processors, API routes, and scheduled jobs.
func (p *SlackPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	Init(pool, encKey)

	pctx.SetNotifier(NewNotifier(pool))

	// Register webhook processors and seed their types into the DB.
	eventsProc := &SlackEventsProcessor{pool: pool, encKey: encKey}
	plugin.RegisterProcessor(eventsProc)
	if err := store.SeedWebhookProcessorType(context.Background(), pool, eventsProc.Type(), eventsProc.Label()); err != nil {
		log.Printf("slack/plugin: failed to seed events processor type: %v", err)
	}

	interactionsProc := &SlackInteractionsProcessor{pool: pool, encKey: encKey}
	plugin.RegisterProcessor(interactionsProc)
	if err := store.SeedWebhookProcessorType(context.Background(), pool, interactionsProc.Type(), interactionsProc.Label()); err != nil {
		log.Printf("slack/plugin: failed to seed interactions processor type: %v", err)
	}

	slashProc := &SlackSlashCommandProcessor{pool: pool, encKey: encKey}
	plugin.RegisterProcessor(slashProc)
	if err := store.SeedWebhookProcessorType(context.Background(), pool, slashProc.Type(), slashProc.Label()); err != nil {
		log.Printf("slack/plugin: failed to seed slash command processor type: %v", err)
	}

	// Register trigger types.
	plugin.RegisterTriggerType(&teamJoinTrigger{})

	// Register action types.
	plugin.RegisterActionType(&ChannelMessageAction{})
	plugin.RegisterActionType(&DirectMessageAction{})
	plugin.RegisterActionType(&HandleEventsAction{pool: pool, encKey: encKey})
	plugin.RegisterActionType(&HandleInteractionsAction{pool: pool, encKey: encKey, pctx: pctx})

	// Register API routes.
	pctx.RegisterAuthRoute("GET", "/api/slack/channels", HandleListSlackChannels())
	pctx.RegisterAuthRoute("POST", "/api/integrations/slack/manifest", handleManifest(pool, encKey))

	// Register scheduled jobs.
	pctx.RegisterScheduledJob(
		"slack_user_sync_enabled", "slack_user_sync_interval_minutes",
		1*time.Minute, func() {
			SyncAllUsers(context.Background(), pool)
		},
	)

	return nil
}
