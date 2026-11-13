package slack

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// registerWorkspaceIdentifiers records the workspace's team ID and this app's ID in config_store
// (slack/team_id and slack/app_id) so later features can build links to the app. It is best effort and
// silent: failures are only logged. The team ID comes from auth.test; the app ID comes from bots.info for
// the bot that auth.test reports (bots.info needs the users:read scope the app already requests).
func registerWorkspaceIdentifiers(ctx context.Context, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client) {
	auth, err := client.AuthTestContext(ctx)
	if err != nil {
		log.Printf("slack/identifiers: auth.test: %v", err)
		return
	}
	save := func(key, value string) {
		if value == "" {
			return
		}
		if err := store.SetServiceConfig(ctx, pool, "slack", key, value, false, nil, encKey); err != nil {
			log.Printf("slack/identifiers: save %s: %v", key, err)
		}
	}
	save("team_id", auth.TeamID)

	if auth.BotID == "" {
		return
	}
	bot, err := client.GetBotInfoContext(ctx, slacklib.GetBotInfoParameters{Bot: auth.BotID})
	if err != nil {
		log.Printf("slack/identifiers: bots.info: %v", err)
		return
	}
	save("app_id", bot.AppID)
}
