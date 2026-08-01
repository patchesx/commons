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
			fmt.Fprint(w, `<div class="notification is-warning is-light mb-6">
  Set your base URL in <a href="/admin/settings" class="has-text-weight-medium">Settings</a> before continuing.
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
  <pre class="is-size-7 is-family-monospace has-background-light p-3 mb-2" style="border: 1px solid var(--bulma-border); border-radius: 0.25rem; overflow-x: auto; white-space: pre" x-text="yaml"></pre>
  <div class="is-flex is-align-items-center is-flex-wrap-wrap" style="gap: 0.5rem">
    <button
      x-on:click="navigator.clipboard.writeText(yaml); copied = true; setTimeout(() => copied = false, 2000)"
      class="button is-small"
      x-text="copied ? 'Copied!' : 'Copy to clipboard'"
    ></button>
    <form method="post" action="/api/integrations/slack/manifest?download=1" class="is-inline">
      <button
        type="submit"
        class="button is-small"
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
  bot_user:
    display_name: %s
    always_online: false
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
`, orgName, botDisplayName(orgName), baseURL, eventsSlug, baseURL, interactionsSlug)
}

// botDisplayName derives a Slack-compatible bot display name from an
// organization name. Slack allows only a-z, 0-9, -, _, and . (max 80 chars).
func botDisplayName(orgName string) string {
	s := strings.ToLower(orgName)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s = b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-_.")
	if s == "" {
		s = "commons"
	}
	if len(s) > 80 {
		s = strings.TrimRight(s[:80], "-_.")
	}
	return s
}

func renderSetupCard(webhookWarning bool) string {
	var b strings.Builder

	b.WriteString(`<div class="box mb-6">`)
	b.WriteString(`<div class="mb-4">`)
	b.WriteString(`<h2 class="title is-6 mb-0">Setup Guide</h2>`)
	b.WriteString(`</div>`)

	// Step 1 — webhook status
	b.WriteString(`<div class="py-4" style="border-bottom: 1px solid var(--bulma-border)"><div class="is-flex" style="gap: 0.75rem">`)
	b.WriteString(`<span class="tag is-primary is-light is-rounded is-flex is-align-items-center is-justify-content-center" style="flex-shrink: 0; width: 1.5rem; height: 1.5rem">1</span>`)
	b.WriteString(`<div style="flex: 1; min-width: 0">`)
	b.WriteString(`<p class="is-size-6 has-text-weight-semibold mb-1">Webhook status</p>`)
	if webhookWarning {
		b.WriteString(`<p class="is-size-7 has-text-warning">Some managed webhooks are missing — regenerate the manifest to restore them.</p>`)
	} else {
		b.WriteString(`<p class="is-size-7 has-text-success">Both managed webhooks are present.</p>`)
	}
	b.WriteString(`</div></div></div>`)

	// Step 2 — generate manifest
	b.WriteString(`<div class="py-4" style="border-bottom: 1px solid var(--bulma-border)"><div class="is-flex" style="gap: 0.75rem">`)
	b.WriteString(`<span class="tag is-primary is-light is-rounded is-flex is-align-items-center is-justify-content-center" style="flex-shrink: 0; width: 1.5rem; height: 1.5rem">2</span>`)
	b.WriteString(`<div style="flex: 1; min-width: 0">`)
	b.WriteString(`<p class="is-size-6 has-text-weight-semibold mb-1">Generate app manifest</p>`)
	b.WriteString(`<p class="is-size-7 has-text-grey mb-3">Generates a Slack app manifest pre-configured for this instance and creates the managed webhook endpoints used by the Slack integration.</p>`)
	b.WriteString(`<button`)
	b.WriteString(` hx-post="/api/integrations/slack/manifest"`)
	b.WriteString(` hx-target="#slack-manifest-result"`)
	b.WriteString(` hx-swap="innerHTML"`)
	b.WriteString(` class="button is-primary is-small"`)
	b.WriteString(`>Generate manifest</button>`)
	b.WriteString(`<div id="slack-manifest-result" class="mt-3"></div>`)
	b.WriteString(`</div></div></div>`)

	// Step 3 — create Slack app
	b.WriteString(`<div class="py-4" style="border-bottom: 1px solid var(--bulma-border)"><div class="is-flex" style="gap: 0.75rem">`)
	b.WriteString(`<span class="tag is-primary is-light is-rounded is-flex is-align-items-center is-justify-content-center" style="flex-shrink: 0; width: 1.5rem; height: 1.5rem">3</span>`)
	b.WriteString(`<div style="flex: 1; min-width: 0">`)
	b.WriteString(`<p class="is-size-6 has-text-weight-semibold mb-1">Create your Slack app</p>`)
	b.WriteString(`<p class="is-size-7 has-text-grey">Go to <a href="https://api.slack.com/apps" target="_blank" rel="noopener noreferrer" class="has-text-link">api.slack.com/apps</a> → Create New App → From a manifest → paste the YAML above.</p>`)
	b.WriteString(`</div></div></div>`)

	// Step 4 — enter credentials
	b.WriteString(`<div class="py-4"><div class="is-flex" style="gap: 0.75rem">`)
	b.WriteString(`<span class="tag is-primary is-light is-rounded is-flex is-align-items-center is-justify-content-center" style="flex-shrink: 0; width: 1.5rem; height: 1.5rem">4</span>`)
	b.WriteString(`<div style="flex: 1; min-width: 0">`)
	b.WriteString(`<p class="is-size-6 has-text-weight-semibold mb-1">Enter your credentials</p>`)
	b.WriteString(`<p class="is-size-7 has-text-grey">Once your Slack app is created, copy your Bot User OAuth Token and Signing Secret into the credential fields below.</p>`)
	b.WriteString(`</div></div></div>`)

	b.WriteString(`</div>`)

	return b.String()
}
