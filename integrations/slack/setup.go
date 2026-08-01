package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// SetupHandler implements plugin.SetupProvider. The returned handler renders the
// Slack setup guide fragment loaded on the integration detail page.
func (p *SlackPlugin) SetupHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		w.Header().Set("Content-Type", "text/html")

		baseURL, _ := store.GetServiceConfig(ctx, pkgPool, "app", "base_url", pkgEncKey)
		baseURL = strings.TrimRight(baseURL, "/")

		if baseURL == "" {
			fmt.Fprint(w, `<div class="mb-6 rounded-lg border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-950/40 px-4 py-3 text-sm text-amber-700 dark:text-amber-300">
  Set your base URL in <a href="/admin/settings" class="underline font-medium">Settings</a> before continuing.
</div>`)
			return
		}

		webhooks, _ := store.ListManagedWebhooks(ctx, pkgPool, "slack")
		hasEvents, hasInteractions := false, false
		for _, wh := range webhooks {
			switch wh.Slug {
			case "slack/events":
				hasEvents = true
			case "slack/interactions":
				hasInteractions = true
			}
		}

		fmt.Fprint(w, renderSetupCard(!hasEvents || !hasInteractions))
	}
}

// handleManifest handles POST /api/integrations/slack/manifest.
// Upserts the two managed webhooks, ensures their actions, builds the manifest
// YAML, and returns either an HTML fragment (default) or a downloadable file
// (?download=1).
func handleManifest(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		baseURL, _ := store.GetServiceConfig(ctx, pool, "app", "base_url", encKey)
		baseURL = strings.TrimRight(baseURL, "/")
		if baseURL == "" {
			http.Error(w, "app.base_url is not configured — set it in Settings first", http.StatusBadRequest)
			return
		}

		eventsWH, err := store.UpsertManagedWebhook(ctx, pool, store.UpsertManagedWebhookParams{
			Slug:          "slack/events",
			Name:          "Slack Events",
			ProcessorType: "slack_events",
			ManagedBy:     "slack",
			Enabled:       true,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

		interactionsWH, err := store.UpsertManagedWebhook(ctx, pool, store.UpsertManagedWebhookParams{
			Slug:          "slack/interactions",
			Name:          "Slack Interactions",
			ProcessorType: "slack_interactions",
			ManagedBy:     "slack",
			Enabled:       true,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

		if err := store.EnsureWebhookAction(ctx, pool, eventsWH.ID, "slack.handle_events"); err != nil {
			http.Error(w, "failed to ensure events webhook action", http.StatusInternalServerError)
			return
		}
		if err := store.EnsureWebhookAction(ctx, pool, interactionsWH.ID, "slack.handle_interactions"); err != nil {
			http.Error(w, "failed to ensure interactions webhook action", http.StatusInternalServerError)
			return
		}

		orgName, _ := store.GetServiceConfig(ctx, pool, "ui", "org_name", encKey)
		if orgName == "" {
			orgName = "My Organization"
		}

		manifest := buildManifestYAML(orgName, baseURL, eventsWH.Slug, interactionsWH.Slug)

		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Type", "text/yaml")
			w.Header().Set("Content-Disposition", `attachment; filename="slack-manifest.yaml"`)
			fmt.Fprint(w, manifest)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		yamlJSON, _ := json.Marshal(manifest)
		fmt.Fprintf(w, `<div x-data='{ yaml: %s, copied: false }'>
  <pre class="text-xs font-mono bg-gray-50 dark:bg-gray-900 border border-gray-200 dark:border-gray-700 rounded p-3 overflow-x-auto whitespace-pre mb-2" x-text="yaml"></pre>
  <div class="flex items-center gap-2 flex-wrap">
    <button
      x-on:click="navigator.clipboard.writeText(yaml); copied = true; setTimeout(() => copied = false, 2000)"
      class="text-xs font-medium rounded px-2.5 py-1.5 transition-colors bg-gray-100 dark:bg-gray-700 text-gray-700 dark:text-gray-300 border border-gray-200 dark:border-gray-600 hover:bg-gray-200 dark:hover:bg-gray-600"
      x-text="copied ? 'Copied!' : 'Copy to clipboard'"
    ></button>
    <form method="post" action="/api/integrations/slack/manifest?download=1" class="inline">
      <button
        type="submit"
        class="text-xs font-medium rounded px-2.5 py-1.5 transition-colors text-gray-700 dark:text-gray-300 bg-gray-100 dark:bg-gray-700 border border-gray-200 dark:border-gray-600 hover:bg-gray-200 dark:hover:bg-gray-600"
      >Download .yaml</button>
    </form>
  </div>
</div>`, string(yamlJSON))
	}
}

func buildManifestYAML(orgName, baseURL, eventsSlug, interactionsSlug string) string {
	return fmt.Sprintf(`display_information:
  name: %s
features:
  app_home:
    home_tab_enabled: true
    messages_tab_enabled: false
oauth_config:
  scopes:
    bot:
      - channels:read
      - chat:write
      - groups:read
      - im:history
      - im:write
      - users:read
      - users:read.email
settings:
  event_subscriptions:
    request_url: %s/webhook/%s
    bot_events:
      - app_home_opened
      - team_join
  interactivity:
    is_enabled: true
    request_url: %s/webhook/%s
  org_deploy_enabled: false
  socket_mode_enabled: false
  token_rotation_enabled: false
`, orgName, baseURL, eventsSlug, baseURL, interactionsSlug)
}

func renderSetupCard(webhookWarning bool) string {
	var b strings.Builder

	b.WriteString(`<div class="bg-white dark:bg-gray-800 rounded-lg shadow mb-6">`)
	b.WriteString(`<div class="px-4 py-3 border-b border-gray-100 dark:border-gray-700">`)
	b.WriteString(`<h2 class="font-medium text-gray-800 dark:text-gray-100">Setup Guide</h2>`)
	b.WriteString(`</div>`)
	b.WriteString(`<div class="divide-y divide-gray-100 dark:divide-gray-700">`)

	// Step 1 — webhook status
	b.WriteString(`<div class="px-4 py-4"><div class="flex gap-3">`)
	b.WriteString(`<span class="shrink-0 w-6 h-6 rounded-full bg-accent-100 dark:bg-accent-900/40 text-accent-700 dark:text-accent-400 text-xs font-semibold flex items-center justify-center">1</span>`)
	b.WriteString(`<div class="flex-1 min-w-0">`)
	b.WriteString(`<p class="text-sm font-medium text-gray-800 dark:text-gray-100 mb-1">Webhook status</p>`)
	if webhookWarning {
		b.WriteString(`<p class="text-xs text-amber-700 dark:text-amber-400 bg-amber-50 dark:bg-amber-950/40 border border-amber-200 dark:border-amber-800 rounded px-2.5 py-1.5">Some managed webhooks are missing — regenerate the manifest to restore them.</p>`)
	} else {
		b.WriteString(`<p class="text-xs text-green-700 dark:text-green-400">Both managed webhooks are present.</p>`)
	}
	b.WriteString(`</div></div></div>`)

	// Step 2 — generate manifest
	b.WriteString(`<div class="px-4 py-4"><div class="flex gap-3">`)
	b.WriteString(`<span class="shrink-0 w-6 h-6 rounded-full bg-accent-100 dark:bg-accent-900/40 text-accent-700 dark:text-accent-400 text-xs font-semibold flex items-center justify-center">2</span>`)
	b.WriteString(`<div class="flex-1 min-w-0">`)
	b.WriteString(`<p class="text-sm font-medium text-gray-800 dark:text-gray-100 mb-1">Generate app manifest</p>`)
	b.WriteString(`<p class="text-xs text-gray-500 dark:text-gray-400 mb-3">Generates a Slack app manifest pre-configured for this instance and creates the managed webhook endpoints used by the Slack integration.</p>`)
	b.WriteString(`<button`)
	b.WriteString(` hx-post="/api/integrations/slack/manifest"`)
	b.WriteString(` hx-target="#slack-manifest-result"`)
	b.WriteString(` hx-swap="innerHTML"`)
	b.WriteString(` class="text-xs font-medium rounded px-3 py-1.5 bg-accent-600 hover:bg-accent-700 text-white transition-colors focus-visible:ring-2 focus-visible:ring-accent-500"`)
	b.WriteString(`>Generate manifest</button>`)
	b.WriteString(`<div id="slack-manifest-result" class="mt-3"></div>`)
	b.WriteString(`</div></div></div>`)

	// Step 3 — create Slack app
	b.WriteString(`<div class="px-4 py-4"><div class="flex gap-3">`)
	b.WriteString(`<span class="shrink-0 w-6 h-6 rounded-full bg-accent-100 dark:bg-accent-900/40 text-accent-700 dark:text-accent-400 text-xs font-semibold flex items-center justify-center">3</span>`)
	b.WriteString(`<div class="flex-1 min-w-0">`)
	b.WriteString(`<p class="text-sm font-medium text-gray-800 dark:text-gray-100 mb-1">Create your Slack app</p>`)
	b.WriteString(`<p class="text-xs text-gray-500 dark:text-gray-400">Go to <a href="https://api.slack.com/apps" target="_blank" rel="noopener noreferrer" class="text-accent-600 dark:text-accent-400 hover:underline">api.slack.com/apps</a> → Create New App → From a manifest → paste the YAML above.</p>`)
	b.WriteString(`</div></div></div>`)

	// Step 4 — enter credentials
	b.WriteString(`<div class="px-4 py-4"><div class="flex gap-3">`)
	b.WriteString(`<span class="shrink-0 w-6 h-6 rounded-full bg-accent-100 dark:bg-accent-900/40 text-accent-700 dark:text-accent-400 text-xs font-semibold flex items-center justify-center">4</span>`)
	b.WriteString(`<div class="flex-1 min-w-0">`)
	b.WriteString(`<p class="text-sm font-medium text-gray-800 dark:text-gray-100 mb-1">Enter your credentials</p>`)
	b.WriteString(`<p class="text-xs text-gray-500 dark:text-gray-400">Once your Slack app is created, copy your Bot User OAuth Token and Signing Secret into the credential fields below.</p>`)
	b.WriteString(`</div></div></div>`)

	b.WriteString(`</div></div>`) // close divide-y + card

	return b.String()
}
