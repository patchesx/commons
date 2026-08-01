# Discord Integration

> **Status: scaffold — written but untested.** This plugin is fully implemented and registered (notifier, pipeline actions, Gateway sync), but it has never been run against a live Discord server — the maintainers have no Discord environment to test in. Treat every flow as unverified, and use this plugin together with matrix as a template for building new chat-platform plugins. The Slack plugin is the production-tested reference. Some notification helpers (`NotifyNewAdmin`, `NotifyAdminsNewWorkItem`) are scaffolded but not yet called from any flow.

Adds Discord as a full notification and member-interaction platform alongside (or instead of) Slack.

---

## What it does

- **Member portal** — a configurable slash command (default `/home`) opens an ephemeral message with permission-filtered action buttons: Resources, Legislation, Calendar, Library, Schedule Meeting, Manage Meetings, Request Role, Report Issue
- **Role requests** — members can request Discord roles through a modal; requests are stored in `discord.role_requests` and admins are notified
- **DM notifications** — a `platform.Notifier` implementation delivers system DMs (meeting reminders, admin promotions, legislation updates) to users with a linked Discord identity; the `discord.dm` pipeline action covers automation-driven DMs such as job complete/failed
- **Gateway** — a WebSocket connection to Discord keeps member identity in sync in real time (join, leave, update)
- **Scheduled member sync** — periodic full guild member sync, configurable from the Scheduler page
- **Pipeline actions** — `discord.channel` and `discord.dm` action types available in the webhook pipeline editor

---

## Prerequisites

- A Discord account with admin access to the target server
- Go application running with a publicly accessible HTTPS URL (Discord requires HTTPS for interactions)

---

## Setup

### 1. Create a Discord Application

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications)
2. Click **New Application**, give it a name
3. From the left sidebar, go to **Bot**
   - Click **Add Bot** (or **Reset Token** if already created)
   - Copy the **Token** — you'll need this
   - Under **Privileged Gateway Intents**, enable **Server Members Intent** (required for the gateway and member sync)
4. Go to **General Information**
   - Copy the **Application ID**
   - Copy the **Public Key**

### 2. Invite the Bot to Your Server

1. In the Developer Portal, go to **OAuth2 → URL Generator**
2. Select scopes: `bot` and `applications.commands`
3. Select bot permissions: **Send Messages**, **Read Messages/View Channels**
4. Copy and visit the generated URL, then select your server

### 3. Get Your Guild (Server) ID

Right-click your Discord server icon in the sidebar → **Copy Server ID**. (Developer Mode must be on: User Settings → Advanced → Developer Mode.)

### 4. Configure the Integration

1. Log in to the admin panel and go to **Integrations**
2. Find the **Discord** card and fill in:
   | Field | Value |
   |---|---|
   | Enable Discord | `true` |
   | Bot Token | Paste from step 1 |
   | Application ID | Paste from step 1 |
   | Public Key | Paste from step 1 |
   | Guild (Server) ID | Paste from step 3 |
   | Slash Command Name | e.g. `home` (lowercase, no slash) |

### 5. Set the Interactions Endpoint

1. In the Developer Portal, go to **General Information**
2. Under **Interactions Endpoint URL**, enter:
   ```
   https://yourdomain.com/webhook/discord/interactions
   ```
3. Create the webhook row via the Webhooks admin page with:
   - Slug: `discord/interactions`
   - Processor: `Discord Interactions`

   (Processor types are registered automatically at startup by the Discord plugin.)

4. Click **Save Changes** in Discord — Discord will immediately send a PING to verify the endpoint. The app must be running and the public key must be configured for this to succeed.

### 6. Register the Slash Command

Back in the admin panel on the **Integrations** page, scroll down to the **Discord — Slash Command** card and click **Register /\<your-command-name\>**.

This calls Discord's API to create the guild command. It's safe to re-run if you change the command name — Discord will overwrite the existing one.

> **Note:** Slash commands can take up to an hour to propagate to all Discord clients, but usually appear within seconds.

---

## Testing

1. Start the app and check logs for `discord gateway connected`
2. Go to your Discord server and type `/home` (or whatever command name you configured)
3. An ephemeral message should appear with buttons based on your permissions
4. Try clicking **Request Role** to verify the modal and role request flow
5. Try clicking **Report Issue** to verify work item creation
6. Check the **Users** admin page — guild members should appear with Discord identities after the first member sync runs (or immediately if they trigger the gateway by joining)

---

## Troubleshooting

**Interactions endpoint verification fails**
- Make sure `discord.public_key` is saved in config_store before Discord tries to verify
- Confirm the app is running and the `/webhook/discord/interactions` route is reachable from the internet
- Check that the webhook row exists in the database with `processor_type = 'discord_interactions'`

**Gateway doesn't connect**
- Check logs for `discord gateway: open:` errors
- Verify `discord.enabled = true` and `discord.bot_token` are set
- Confirm **Server Members Intent** is enabled in the Developer Portal under Bot → Privileged Gateway Intents

**Slash command not appearing**
- Run the register command from the Integrations page and check for errors
- Verify `discord.application_id` and `discord.guild_id` are correct
- Guild commands propagate faster than global commands; wait up to 1 hour if needed

**DMs not sending**
- Verify the bot has permission to send DMs (it needs to share a server with the user)
- Check that the user has a Discord identity in the `user_identities` table (`provider = 'discord'`)
- Confirm `discord.enabled = true` and `discord.bot_token` is valid

---

## Config Keys Reference

All stored in `config_store` under service `discord`:

| Key | Sensitive | Description |
|---|---|---|
| `enabled` | No | Master toggle — set to `true` to activate |
| `bot_token` | Yes | Discord bot token from the Developer Portal |
| `application_id` | No | Application/client ID for slash command registration |
| `public_key` | No | Ed25519 public key for verifying interaction requests |
| `guild_id` | No | Target Discord server ID |
| `command_name` | No | Slash command name (lowercase, no slash, e.g. `home`) |

---

## Architecture Notes

- **Interactions** are received via HTTP POST through the webhook pipeline. The processor verifies the Ed25519 signature, responds with a deferred ACK (Discord type 5), and passes `raw_body` to the `discord.handle_interactions` action. The action calls `DispatchInteraction`, which routes to the appropriate handler and sends the actual response to Discord via the webhook followup API.
- **Gateway** opens a persistent WebSocket connection via `discordgo`. It handles member join/leave/update events to keep `user_identities` current. The connection is managed by discordgo's built-in reconnect logic.
- **Notifier** implements `platform.Notifier` and is added to the composite notifier, so all platform-level notification calls (job complete, library overdue, etc.) automatically fan out to Discord alongside Slack.
- **Role requests** are stored in `discord.role_requests` (plugin-owned schema/table, applied on first startup via the plugin migration system).
