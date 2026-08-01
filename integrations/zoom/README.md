# Zoom Integration

Handles Zoom recording webhooks and kicks off the upload pipeline when a meeting ends. Optionally connects to Zoom's Server-to-Server (S2S) OAuth API for meeting management, scheduling, and automatic recording deletion.

---

## What it does

- **Recording pipeline** — receives `recording.completed` webhooks from Zoom, verifies the HMAC signature, deduplicates by `meeting_uuid`, selects the best MP4 file, creates a job record, and passes everything to the pipeline (YouTube upload, Drive backup, Slack notifications)
- **Private meeting detection** — after acknowledging the webhook, calls the Zoom API to check if the meeting was private; the pipeline can filter on `is_private eq false`
- **Meeting management** — S2S OAuth integration for creating, listing, and cancelling Zoom meetings from the admin UI and Slack App Home
- **Meeting sync** — periodic pull of scheduled meetings from Zoom into the local database for calendar and reminder features
- **Recording deletion** — optional `zoom.delete_recording` pipeline action that soft-deletes cloud recordings from Zoom after a successful upload

---

## Prerequisites

- A Zoom account (Pro or higher for webhooks and cloud recording)
- A publicly accessible HTTPS URL for your app

---

## Setup

### Part A: Recording Webhooks (required for upload pipeline)

#### 1. Create a Webhook-Only App

1. Go to [marketplace.zoom.us](https://marketplace.zoom.us) → **Develop → Build App**
2. Choose **Webhook Only**
3. Under **Feature → Event Subscriptions**, add a subscription:
   - **Event notification endpoint URL:** `https://yourdomain.com/webhook/zoom`
   - Add event: `Recording → recording.completed`
4. Copy the **Secret Token** from the Event Subscriptions section

#### 2. Create the Webhook Row

Add it via the **Webhooks** admin page: slug `zoom`, processor `Zoom Recording Webhook`. (Processor types are registered automatically at startup by the Zoom plugin.)

Then wire actions to the webhook's pipeline on the **Triggers** page — at minimum an upload action such as `youtube.upload_video`.

#### 3. Configure in Admin UI

Go to **Integrations → Zoom** and enter:

| Field | Value |
|---|---|
| Webhook Secret | Secret Token from step 1 |

### Part B: Meeting Management (optional, for scheduling features)

#### 1. Create a Server-to-Server OAuth App

1. Go to [marketplace.zoom.us](https://marketplace.zoom.us) → **Develop → Build App**
2. Choose **Server-to-Server OAuth**
3. Under **Scopes**, add:
   - `meeting:read:admin`, `meeting:write:admin`
   - `recording:read:admin`, `recording:write:admin` (for deletion)
4. Copy the **Account ID**, **Client ID**, and **Client Secret**

#### 2. Configure in Admin UI

Go to **Integrations → Zoom** and add:

| Field | Value |
|---|---|
| Account ID | From step 1 |
| API Client ID | From step 1 |
| API Client Secret | From step 1 |
| Delete After Upload | `true` to remove cloud recordings after successful upload (optional) |

---

## Pipeline Filters

The Zoom processor exposes these fields for pipeline filter rules:

| Field | Type | Example use |
|---|---|---|
| `duration_minutes` | number | `gte 30` — skip short test calls |
| `is_private` | boolean | `eq false` — skip private meetings |
| `meeting_topic` | string | `contains Town Hall` — only specific meetings |
| `host_email` | string | `eq host@example.com` — specific host |

Filters are configured per-webhook in the Webhooks admin page.

---

## Testing

1. Start the app. Check logs — no errors for missing credentials.
2. On the Webhooks admin page, confirm the Zoom webhook exists and is enabled.
3. Record a short test meeting to the Zoom cloud and end it — Zoom fires `recording.completed` a few minutes after the recording finishes processing.
4. Check **Jobs** in the admin panel — a new job should appear with status `running` then `complete`.

If the webhook has a minimum-duration filter (see above), make the test meeting long enough to pass it, or temporarily disable the filter.

---

## Troubleshooting

**Webhook returns 401**
- Verify `webhook_secret` in config_store matches the Secret Token in Zoom Marketplace
- Check that the `x-zm-request-timestamp` header is recent — requests older than 5 minutes are rejected

**Job created but recording not downloaded**
- Confirm the `youtube.upload_video` action is wired to the Zoom webhook
- Check that the MP4 URL in the webhook payload is accessible (some recording types don't produce an MP4)

**Private meetings still being processed**
- Add a filter `is_private eq false` to the Zoom webhook in the Webhooks admin page

**S2S API calls fail (meeting create/delete)**
- Verify Account ID, Client ID, and Client Secret are all set
- Check that the S2S app has the required scopes — reinstall if you added scopes after initial creation

**Meeting sync not running**
- Check Scheduler page: `zoom_meeting_sync_enabled` must be `true`

---

## Config Keys Reference

All stored in `config_store` under service `zoom` (recording config keys use service `recordings`):

| Service | Key | Sensitive | Description |
|---|---|---|---|
| `zoom` | `webhook_secret` | Yes | HMAC secret for webhook signature verification |
| `zoom` | `account_id` | No | S2S OAuth Account ID |
| `zoom` | `api_client_id` | No | S2S OAuth Client ID |
| `zoom` | `api_client_secret` | Yes | S2S OAuth Client Secret |
| `zoom` | `delete_after_upload` | No | Set `true` to delete cloud recordings after upload |
| `recordings` | `upload_notification_channel` | No | Slack channel ID to post upload-complete notifications (instead of individual DMs) |

---

## Architecture Notes

- **Replay prevention:** requests with `x-zm-request-timestamp` older than 5 minutes are rejected before HMAC verification.
- **Deduplication:** each recording has a unique `meeting_uuid`; the `recording_data` primary key prevents duplicate pipeline runs for the same recording.
- **Private detection is deferred:** the webhook is acknowledged with HTTP 200 immediately; the Zoom API call to check `is_private` happens after the ACK to avoid Zoom's 3-second timeout. The `is_private` field is available as a pipeline filter field.
- **No disk I/O:** recording files are streamed from Zoom's CDN directly to YouTube via `io.Pipe`. Nothing is written to disk.
- **Best-file selection:** when a recording has multiple MP4 files, the processor picks `shared_screen_with_speaker_view` first, then `shared_screen_with_gallery_view`, then the largest available MP4.
