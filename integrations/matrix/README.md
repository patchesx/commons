# Matrix Integration

> **Status: scaffold — written but untested.** This plugin is fully implemented and registered (notifier, pipeline actions, sync loop), but it has never been run against a live Matrix homeserver — the maintainers have no Matrix environment to test in. Treat every flow as unverified, and use this plugin together with discord as a template for building new chat-platform plugins. The Slack plugin is the production-tested reference. Some notification helpers (`NotifyNewAdmin`, `NotifyAdminsNewWorkItem`) are scaffolded but not yet called from any flow.

Connects the platform to a Matrix homeserver. The bot delivers system DMs via the platform.Notifier interface, handles text commands from room members, syncs home room membership, and exposes pipeline action types for webhook-triggered room/DM messages.

---

## What It Does

- **Notifier DMs** — delivers job completion alerts, legislation updates, and library notifications as Matrix DMs to users who have a Matrix identity linked in their user record
- **Pipeline action types** — `matrix.room` (post to a room) and `matrix.dm` (send a DM) are available in the webhook action editor, supporting `{{key}}` template references
- **Text commands** — the bot responds to commands in any room it has joined (see command table below)
- **Member sync** — a scheduled job reads the configured home room's membership and upserts those users into the platform's user store with a `matrix` external identity

---

## Prerequisites

- A Matrix account for the bot (e.g. `@orgbot:matrix.org`)
- The bot's access token (obtained at account creation or via `/login` API)
- The bot must be invited to any room it should post in or sync from

---

## Setup

1. **Create the bot account** on your Matrix homeserver (or matrix.org)
2. **Obtain the access token** — shown once at registration, or retrieve via `POST /_matrix/client/v3/login`
3. **Configure in the admin UI** — go to Integrations, open the Matrix card, and fill in all six config keys (see table below)
4. **Invite the bot to the home room** — the room whose membership is synced as Matrix users; the bot must accept the invite before sync will find any members
5. **Invite the bot to any pipeline target rooms** — rooms referenced in webhook actions must have the bot as a member
6. **Set `enabled` to `true`** — the sync loop and command handler will start on the next restart (or immediately if the process reads config hot)

---

## Available Commands

Commands are matched by prefix (default `!`). The prefix is configurable via the `command_prefix` key.

| Command | Description |
|---|---|
| `!home` | Shows a welcome message and link to the member portal |
| `!jobs` | Lists recent recording jobs and their status |
| `!resources` | Lists published resource library items |
| `!bills` | Lists currently tracked legislation bills |
| `!role-request <reason>` | Submits a role/channel access request for admin review |
| `!report <title>` | Files an issue report (title is required) |

---

## Config Keys

| Key | Label | Sensitive | Description |
|---|---|---|---|
| `enabled` | Enable Matrix Integration | No | Set to `"true"` to enable the Matrix bot |
| `homeserver` | Homeserver URL | No | Matrix homeserver URL, e.g. `https://matrix.org` |
| `user_id` | Bot User ID | No | Matrix bot user ID, e.g. `@orgbot:matrix.org` |
| `access_token` | Access Token | Yes | Bot access token (generated when bot account is created) |
| `home_room_id` | Home Room ID | No | Room whose members are synced as Matrix users |
| `command_prefix` | Command Prefix | No | Prefix character for bot commands (default: `!`) |

---

## Architecture Notes

- **Sync loop** — uses mautrix-go's `Client.SyncWithContext` (long-polling `/sync`), not a persistent WebSocket. The loop runs in a background goroutine started at plugin init. If the bot is not configured, `StartSync` logs and returns immediately.
- **No persistent sync token** — `MemorySyncStore` is used; the sync token is not written to the database and is lost on restart. The bot will re-process recent events from the homeserver's since-token on each startup.
- **No interactive UI components** — Matrix's event model does not support Slack-style Block Kit modals or Discord-style buttons. All interactions are plain text commands and plain text replies.
- **DM room creation per recipient** — `PostDirectMessage` always calls `CreateRoom` with `is_direct: true`. The Matrix spec allows this; homeservers deduplicate DM rooms. A future improvement could cache the DM room ID per user.
- **Hot-reloadable client** — `getClient` checks the `access_token` on every call and rebuilds the mautrix Client only when the token value changes (guarded by `sync.Mutex`), matching the Discord and Slack hot-reload patterns.
- **Member sync** — reads joined members of `home_room_id` and upserts external identities with platform `"matrix"`. Members no longer in the room are marked `deactivated`. Runs on the scheduler interval configured by `matrix_user_sync_interval_minutes`.
