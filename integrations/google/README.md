# Google Plugin

Umbrella plugin that consolidates the YouTube and Google Drive integrations into a single "Google" card on the Integrations page. Manages the shared OAuth connection used by both services.

---

## What it does

- **Unified credentials card** — presents YouTube, Drive, and Google config as one collapsible card instead of three separate ones
- **OAuth status display** — shows the connected Google account email (or "Not connected") with a connect/reconnect button
- **Domain restriction** — optionally restricts the OAuth flow to a specific Google Workspace domain

---

## This plugin does not do setup on its own

The Google plugin is an organizational wrapper. The actual capabilities live in:

- **[YouTube integration](../youtube/README.md)** — video upload, captions, processing poll
- **[Google Drive integration](../gdrive/README.md)** — recording file backup, storage locations

See those READMEs for full setup instructions. The short version:

1. Create a Google Cloud project, enable YouTube Data API v3 and Google Drive API
2. Create OAuth 2.0 Web Application credentials with redirect URI `https://yourdomain.com/auth/google/callback`
3. Go to **Integrations → Google**, enter the Client ID and Client Secret, then click **Connect Google Account**

---

## Domain Restriction (optional)

If your org uses Google Workspace and you want to prevent non-org accounts from being connected:

Go to **Integrations → Google** and set:

| Field | Value |
|---|---|
| Allowed Domain | Your Workspace domain, e.g. `yourdomain.org` |

When set, the OAuth callback will reject sign-ins from accounts outside that domain.

---

## Config Keys Reference

| Service | Key | Sensitive | Description |
|---|---|---|---|
| `google` | `web_client_id` | No | OAuth 2.0 Client ID (Web Application) |
| `google` | `web_client_secret` | Yes | OAuth 2.0 Client Secret |
| `google` | `connected_email` | No | Set automatically after a successful OAuth flow |
| `google` | `allowed_domain` | No | If set, restricts OAuth to this Google Workspace domain |

---

## Architecture Notes

- This plugin's sole job in code is to call `RegisterIntegrationCard` with a spec that absorbs the `youtube`, `gdrive`, and `google` integration types, replacing their default credential cards with a single combined card.
- The OAuth flow lives in `web/handlers.go` (`/auth/google/connect` and `/auth/google/callback`) and is not owned by this plugin.
- Scopes requested during OAuth: `youtube`, `youtube.upload`, `drive`, `userinfo.email`. All scopes are requested in a single consent screen so the user doesn't need to reconnect when adding Drive support later.
