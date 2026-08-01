# YouTube Integration

Streams Zoom recordings directly to YouTube using a resumable upload, without writing anything to disk. Automatically sets the title, privacy level, and description; uploads VTT transcripts as closed captions; polls until transcoding completes; and creates a resource library entry for the finished video.

---

## What it does

- **Streaming upload** — pipes Zoom's MP4 stream directly into YouTube's resumable upload API via `io.Pipe`; no temp files, no disk writes
- **Transcript upload** — if the Zoom recording includes a VTT transcript, uploads it to YouTube as closed captions
- **Processing poll** — after upload, polls YouTube until the video finishes transcoding (up to 2 hours)
- **Resource library** — automatically creates a `resource_library` row for the uploaded video so it appears in member-facing interfaces
- **Manual upload** — `POST /api/youtube/upload` accepts a multipart form for one-off uploads outside the pipeline
- **OAuth web flow** — connects a Google account via the web UI at `/auth/google/connect`

---

## Prerequisites

- A Google account with a YouTube channel
- A Google Cloud project with the **YouTube Data API v3** enabled
- OAuth 2.0 credentials configured (Web Application type)
- A publicly accessible HTTPS URL for the OAuth callback

---

## Setup

### 1. Create a Google Cloud Project & Enable the API

1. Go to [console.cloud.google.com](https://console.cloud.google.com)
2. Create a project (or use an existing one)
3. Under **APIs & Services → Library**, search for **YouTube Data API v3** and enable it
4. Also enable **Google Drive API** if you plan to use the Drive backup integration

### 2. Create OAuth 2.0 Credentials

1. Under **APIs & Services → Credentials**, click **Create Credentials → OAuth client ID**
2. Application type: **Web application**
3. Under **Authorized redirect URIs**, add:
   ```
   https://yourdomain.com/auth/google/callback
   ```
4. Copy the **Client ID** and **Client Secret**

### 3. Configure OAuth Consent Screen

Under **APIs & Services → OAuth consent screen**:
- User type: **Internal** (if using Google Workspace) or **External**
- Add scopes: `youtube`, `youtube.upload`, `drive`, `userinfo.email`
- If External, add your admin account as a test user until the app is verified

### 4. Configure in Admin UI

Go to **Integrations → Google** and enter:

| Field | Value |
|---|---|
| Web Client ID | OAuth Client ID from step 2 |
| Web Client Secret | OAuth Client Secret from step 2 |

Then click **Connect Google Account** and complete the OAuth flow. The connected email will appear once authorized.

### 5. Set Upload Defaults (optional)

Go to **Integrations → Google** and optionally configure:

| Config Key | Description |
|---|---|
| `uploads.privacy_status` | Default privacy level: `unlisted` (default), `public`, or `private` |
| `uploads.upload_notification_channel` | Slack/Discord channel ID for upload-complete notifications |

---

## Pipeline Action

The `youtube.upload_video` action type is used in the Zoom webhook pipeline. Parameters:

| Parameter | Type | Notes |
|---|---|---|
| `download_url` | text (dynamic) | `{{download_url}}` — from Zoom webhook data |
| `download_token` | text (dynamic) | `{{download_token}}` — from Zoom webhook data |
| `file_size` | text (dynamic) | `{{file_size}}` — used to size the resumable upload |
| `transcript_url` | text (dynamic) | `{{transcript_url}}` — optional, from Zoom webhook |
| `meeting_topic` | text (dynamic) | `{{meeting_topic}}` — becomes the video title |
| `privacy_status` | select | `unlisted` / `public` / `private` |
| `made_for_kids` | boolean | YouTube "made for kids" flag (default false) |

**Outputs available to subsequent pipeline actions:**

| Key | Value |
|---|---|
| `youtube_url` | `https://youtu.be/{video_id}` |
| `video_id` | YouTube video ID |

---

## Testing

1. Configure credentials and connect a Google account (the connected email should appear in the UI)
2. Trigger the pipeline end-to-end: record a short Zoom test meeting to the cloud and end it (see the Zoom integration README's Testing section)
3. Watch the job in **Admin → Jobs** — it should progress through `running` → `complete`
4. Check **Admin → Resources** — a new entry should appear automatically

---

## Troubleshooting

**OAuth callback fails**
- Verify the redirect URI in Google Cloud exactly matches `https://yourdomain.com/auth/google/callback` (no trailing slash)
- Confirm the OAuth consent screen is configured and your account is an authorized test user if the app is in External mode

**Upload stalls or times out**
- Check that the Zoom download URL is reachable from your server (not expired)
- Large files (> 1 GB) may take several minutes to upload on a slow connection
- If the upload was interrupted, the job will resume automatically on next startup

**"Processing" state never clears**
- YouTube can take up to several hours for long videos; the poller checks every 30 seconds for up to 2 hours, then treats it as success
- Check the YouTube Studio dashboard to confirm the video is accessible

**Transcript not appearing on video**
- VTT transcript upload is non-fatal — if it fails, the upload still succeeds
- Check logs for `youtube: upload captions:` errors
- Ensure the Zoom recording includes a transcript file (`transcript_url` field in the webhook)

**Resource library entry not created**
- Check logs for `youtube: create resource:` errors
- This is non-fatal and doesn't affect the upload itself

---

## Config Keys Reference

Stored in `config_store` under services `youtube` (legacy) and `google`:

| Service | Key | Sensitive | Description |
|---|---|---|---|
| `google` | `web_client_id` | No | OAuth 2.0 Web Client ID |
| `google` | `web_client_secret` | Yes | OAuth 2.0 Web Client Secret |
| `google` | `connected_email` | No | Set automatically after OAuth — shows connected account |
| `uploads` | `privacy_status` | No | Default video privacy: `unlisted`, `public`, or `private` |
| `uploads` | `upload_notification_channel` | No | Channel ID for upload-complete notifications |

---

## Architecture Notes

- **No disk writes:** `io.Pipe` connects the Zoom download reader directly to the YouTube upload writer. Chunking is handled internally by the YouTube resumable upload protocol.
- **Resumable uploads:** YouTube's resumable upload API handles network interruptions gracefully. Jobs left in `processing` state on startup are automatically resumed.
- **Processing poll timeout:** after 2 hours of polling, the job is marked complete regardless. The video is accessible on YouTube even while still transcoding.
- **Resource auto-creation:** on successful upload, a `resource_library` row is inserted with the video title, YouTube URL, and a default category. Failure is logged but does not block the job.
