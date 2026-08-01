# Slack Integration

The primary member-facing interface and notification hub. Provides an App Home tab for members, webhook processing for events and interactions, DM notifications for all system events, and a Slack channel action type for the pipeline.

---

## What it does

- **App Home** — role-aware interactive tab in Slack showing jobs feed, resource library, legislation tracking, channel access requests, meeting scheduling, library checkout, and quick links/contacts
- **DM notifications** — delivers all system events (job complete/failed, new admin, channel request, work items, library events) as Slack DMs
- **Webhook processing** — receives and verifies Slack Events API and Interactions payloads; routes them through the webhook pipeline
- **Pipeline actions** — `slack.channel` and `slack.dm` action types for use in the webhook pipeline editor
- **Member sync** — periodic sync of Slack workspace members to the platform `users` table

---

## Prerequisites

- A Slack workspace where you have admin access
- A publicly accessible HTTPS URL for your app

---

## Setup

### 1. Create a Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App → From scratch**
2. Name it (e.g. "Org Operations Bot") and select your workspace

### 2. Configure OAuth Scopes

Under **OAuth & Permissions → Scopes → Bot Token Scopes**, add:

| Scope | Purpose |
|---|---|
| `app_mentions:read` | Receive mention events |
| `channels:read` | List public channels |
| `chat:write` | Post messages |
| `groups:read` | List private channels |
| `im:read` | Read DM channel info |
| `im:write` | Open DM channels |
| `mpim:write` | Open multi-person DMs |
| `users:read` | List workspace members |
| `users:read.email` | Read member email addresses |

Install the app to your workspace and copy the **Bot User OAuth Token** (`xoxb-…`).

### 3. Enable App Home

Under **App Home**, enable the **Home Tab**. Optionally disable the Messages tab if you don't want the bot to appear in DMs.

### 4. Configure Events

Under **Event Subscriptions**:
1. Toggle **Enable Events** on
2. Set **Request URL** to:
   ```
   https://yourdomain.com/webhook/slack/events
   ```
3. Under **Subscribe to bot events**, add: `app_home_opened`, `message.im`
4. Save changes

### 5. Configure Interactivity

Under **Interactivity & Shortcuts**:
1. Toggle **Interactivity** on
2. Set **Request URL** to:
   ```
   https://yourdomain.com/webhook/slack/interactions
   ```
3. Save changes

### 6. Get Your Signing Secret

Under **Basic Information → App Credentials**, copy the **Signing Secret**.

### 7. Create the Webhook Rows

The **Generate manifest** step on the Slack integration page creates them for you: it upserts the `slack/events`, `slack/interactions`, and `slack/slash-commands` webhooks and wires their handler actions (`slack.handle_events`, `slack.handle_interactions`) automatically. No manual seeding is required.

### 8. Configure in Admin UI

Go to **Integrations → Slack** and enter:

| Field | Value |
|---|---|
| Bot Token | `xoxb-…` from step 2 |
| Signing Secret | From step 6 |

Changes take effect immediately without a restart.

---

## Testing

1. Invite the bot to a channel: `/invite @YourBotName`
2. Open the app's Home tab in Slack — the bot should publish a view
3. Trigger an upload pipeline event — you should receive a DM
4. Check **Jobs** in the admin panel — job records should show Slack user identities after App Home opens

---

## Troubleshooting

**App Home shows nothing / "This app hasn't configured a Home tab"**
- Check that `bot_token` is saved and the **Home Tab** is enabled in the Slack app settings
- Look for `app_home_opened` events arriving in logs

**Signature verification fails (401s in logs)**
- Confirm `signing_secret` in config_store matches what's in **Basic Information → App Credentials**
- Check that the webhook row is using processor type `slack_events` or `slack_interactions`

**Bot can't post to channels**
- Verify the `chat:write` scope is granted and the bot is invited to the channel

**Member sync not running**
- Check Scheduler page: `slack_user_sync_enabled` must be `true` and `slack_user_sync_interval_minutes` set to a positive integer

---

## Config Keys Reference

All stored in `config_store` under service `slack`:

| Key | Sensitive | Description |
|---|---|---|
| `bot_token` | Yes | Bot User OAuth Token (`xoxb-…`) |
| `signing_secret` | Yes | App signing secret for webhook verification |

---

## Architecture Notes

- The bot token is cached in memory and only rebuilt when the token value changes — no restart needed after updating credentials.
- Signature verification uses `x-slack-signature` and `x-slack-request-timestamp` headers; requests older than 5 minutes are rejected.
- The processor responds HTTP 200 immediately; event/interaction routing runs asynchronously so Slack never times out.
- Both event and interaction payloads are passed as `raw_body` through the pipeline to the `slack.handle_events` / `slack.handle_interactions` action types, which dispatch to the internal handlers.
