package slack

import (
	"context"
	"log"

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
	StartRetryDrainer(pool)

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
	plugin.RegisterTriggerType(&MemberUpsertedTrigger{})
	plugin.RegisterTriggerType(&MemberDeactivatedTrigger{})
	plugin.RegisterTriggerType(&MemberSyncCompletedTrigger{})

	// Register action types.
	plugin.RegisterActionType(&ChannelMessageAction{})
	plugin.RegisterActionType(&DirectMessageAction{})
	plugin.RegisterActionType(&HandleEventsAction{pool: pool, encKey: encKey})
	plugin.RegisterActionType(&HandleInteractionsAction{pool: pool, encKey: encKey, pctx: pctx})
	plugin.RegisterActionType(&SyncMembersAction{pool: pool, encKey: encKey})

	// Register API routes.
	pctx.RegisterAuthRoute("GET", "/api/slack/channels", HandleListSlackChannels())
	pctx.RegisterAuthRoute("POST", "/api/integrations/slack/manifest", handleManifest(pool, encKey))

	// Admin UI: Slack retry queue (view, manual retry, delete).
	pctx.RegisterNavItem("Slack Retry Queue", "/admin/slack/retry-queue")
	pctx.RegisterAuthRoute("GET", "/admin/slack/retry-queue", HandleRetryQueuePage(pool))
	pctx.RegisterAuthRoute("POST", "/admin/slack/retry-queue/{id}/retry", HandleRetryQueueRetry(pool))
	pctx.RegisterAuthRoute("DELETE", "/admin/slack/retry-queue/{id}", HandleRetryQueueDelete(pool))

	// Seed managed scheduled trigger for member sync (replaces RegisterScheduledJob).
	st, err := store.UpsertManagedScheduledTrigger(context.Background(), pool, store.UpsertManagedScheduledTriggerParams{
		Name:      "Slack Member Sync",
		Schedule:  "1m",
		Timezone:  "UTC",
		ManagedBy: "slack",
		Enabled:   true,
	})
	if err != nil {
		log.Printf("slack/plugin: seed scheduled trigger: %v", err)
	} else {
		if err := store.EnsureWebhookAction(context.Background(), pool, st.ID, "slack.sync_members"); err != nil {
			log.Printf("slack/plugin: seed sync_members action: %v", err)
		}
	}

	return nil
}
