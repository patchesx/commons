package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// HandleListDiscordChannels returns an HTTP handler that lists Discord text channels.
func HandleListDiscordChannels() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, err := ListChannels(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(channels)
	}
}

// HandleRegisterCommand registers the configured slash command with Discord's API.
// POST /api/discord/register-command
func HandleRegisterCommand(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		appID, _ := store.GetServiceConfig(ctx, pool, "discord", "application_id", encKey)
		guildID, _ := store.GetServiceConfig(ctx, pool, "discord", "guild_id", encKey)
		token, _ := store.GetServiceConfig(ctx, pool, "discord", "bot_token", encKey)
		cmdName, _ := store.GetServiceConfig(ctx, pool, "discord", "command_name", encKey)

		var missing []string
		if appID == "" {
			missing = append(missing, "Application ID")
		}
		if guildID == "" {
			missing = append(missing, "Guild ID")
		}
		if token == "" {
			missing = append(missing, "Bot Token")
		}
		if cmdName == "" {
			missing = append(missing, "Slash Command Name")
		}
		if len(missing) > 0 {
			writeCardResult(w, false, "Missing config: "+joinStrings(missing))
			return
		}

		if err := registerSlashCommand(ctx, appID, guildID, token, cmdName); err != nil {
			log.Printf("discord: register command: %v", err)
			writeCardResult(w, false, err.Error())
			return
		}

		writeCardResult(w, true, fmt.Sprintf("/%s registered successfully.", cmdName))
	}
}

// HandleCommandCard returns the slash command registration card fragment.
// GET /admin/fragments/settings/discord-commands
func HandleCommandCard(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		integ, err := store.GetIntegrationByType(ctx, pool, "discord")
		if err != nil || integ == nil || !integ.Enabled {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		cmdName, _ := store.GetServiceConfig(ctx, pool, "discord", "command_name", encKey)
		if cmdName == "" {
			cmdName = "home"
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, renderCommandCard(cmdName, "", false))
	}
}

// registerSlashCommand calls Discord's API to create/overwrite a guild slash command.
func registerSlashCommand(ctx context.Context, appID, guildID, token, cmdName string) error {
	payload := map[string]any{
		"name":        cmdName,
		"description": "Open the member portal",
		"type":        1, // CHAT_INPUT
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("https://discord.com/api/v10/applications/%s/guilds/%s/commands", appID, guildID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("Discord API %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// renderCommandCard returns the full HTML for the slash command card.
func renderCommandCard(cmdName, msg string, success bool) string {
	var statusHTML string
	if msg != "" {
		colorClass := "has-text-danger"
		if success {
			colorClass = "has-text-success"
		}
		statusHTML = fmt.Sprintf(
			`<p id="discord-cmd-result" class="mt-2 is-size-6 %s">%s</p>`,
			colorClass,
			html.EscapeString(msg),
		)
	}

	return `<div class="box">
  <div class="is-flex is-justify-content-space-between is-align-items-center mb-4">
    <h2 class="title is-6 mb-0">Discord — Slash Command</h2>
  </div>
  <div>
    <p class="is-size-7 has-text-grey mb-3">
      Register the <code class="is-family-monospace is-size-7 has-background-light" style="padding: 0.125rem 0.25rem; border-radius: 0.25rem">/` + html.EscapeString(cmdName) + `</code>
      slash command with your Discord server. You must have the bot token, application ID, guild ID,
      and command name saved above before registering.
    </p>
    <div>
      <button
        hx-post="/api/discord/register-command"
        hx-swap="outerHTML"
        hx-target="closest div.box"
        hx-indicator="#discord-cmd-spinner"
        class="button is-primary is-small"
      >
        <span id="discord-cmd-spinner" class="htmx-indicator is-size-7">Loading… </span>
        Register /` + html.EscapeString(cmdName) + `
      </button>` + statusHTML + `
    </div>
    <p class="is-size-7 has-text-grey-light mt-3">
      Re-registering is safe — Discord will overwrite the existing command.
      After registering, members can type <code class="is-family-monospace is-size-7 has-background-light" style="padding: 0 0.125rem; border-radius: 0.25rem">/` + html.EscapeString(cmdName) + `</code> in your server.
    </p>
  </div>
</div>`
}

// writeCardResult writes the card HTML with a success or error message.
// This is what gets swapped in after the register button POST.
func writeCardResult(w http.ResponseWriter, success bool, msg string) {
	// Read current cmdName from context — not available here, so use a safe default.
	// The card will show the result inline without needing to re-read the config.
	cmdName := "home"
	if pkgPool != nil {
		if name, err := store.GetServiceConfig(context.Background(), pkgPool, "discord", "command_name", pkgEncKey); err == nil && name != "" {
			cmdName = name
		}
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, renderCommandCard(cmdName, msg, success))
}

func joinStrings(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
