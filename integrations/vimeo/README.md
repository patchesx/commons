# Vimeo Integration

Uploads meeting recordings to Vimeo. Provides the `vimeo.uploads` capability and the "Upload to Vimeo" pipeline action — an alternative video host to YouTube.

---

## What it does

- **Recording upload** — the `vimeo.upload_video` pipeline action streams the recording MP4 from the source (e.g. Zoom's CDN) directly to Vimeo; nothing is written to disk
- **Pipeline output** — the action exposes `vimeo_url` and `video_id` to later pipeline steps, so a follow-up action (e.g. a Slack message) can share the link
- **Privacy control** — per-action privacy setting: Unlisted (default), Public, or Private
- **Manual upload** — `POST /api/vimeo/upload` re-runs the upload for an existing job from the admin UI

---

## Prerequisites

- A Vimeo account with upload quota (paid plans; upload limits vary by tier)
- A **personal access token** with upload scope: [developer.vimeo.com/apps](https://developer.vimeo.com/apps) → create an app → **Authentication** → generate a token with the `upload`, `private`, and `edit` scopes

---

## Setup

### 1. Configure in Admin UI

Go to **Integrations → Vimeo** and enter:

| Field | Value |
|---|---|
| Access Token | The personal access token from above |

### 2. Wire the Pipeline Action

On the **Triggers** page, add an **Upload to Vimeo** action to the recording webhook's pipeline:

| Param | Value |
|---|---|
| Download URL | `{{download_url}}` |
| Download Token | `{{download_token}}` |
| File Size | `{{file_size}}` |
| Meeting Topic | `{{meeting_topic}}` |
| Privacy | Unlisted / Public / Private |

---

## Troubleshooting

**Upload fails with 401**
- The access token is missing, expired, or lacks the `upload` scope

**Upload fails mid-transfer**
- Vimeo's single-PUT upload needs an accurate file size; confirm `{{file_size}}` is wired and the source recording reports one

**Video uploads but is not viewable**
- Free and lower-tier accounts have weekly upload quotas and file size caps — check your account's quota in Vimeo settings

---

## Config Keys Reference

All stored in `config_store` under service `vimeo`:

| Key | Sensitive | Description |
|---|---|---|
| `access_token` | Yes | Personal access token with upload scope |

---

## Architecture Notes

- Upload metadata (video ID, title, meeting topic/date, duration) lives in the plugin-owned `vimeo.upload_data` table, keyed by job ID.
- The upload is a single `PUT` to the upload link returned by Vimeo's `POST /me/videos` (tus is not used); the recording stream is piped straight through with `Content-Length` set from the source file size.
