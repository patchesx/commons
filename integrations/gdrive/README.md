# Google Drive Integration

Backs up Zoom recording files (MP4, transcript, audio, etc.) to a Google Drive folder. Streams files directly from Zoom's CDN to Drive — nothing is written to disk. Creates a per-meeting subfolder named by date and topic.

---

## What it does

- **Streaming backup** — downloads each recording file from Zoom and uploads it to Drive via `io.Pipe`, with no local disk buffering
- **Per-meeting folders** — creates a subfolder under the configured parent folder, named `YYYY-MM-DD — Meeting Topic`
- **Multiple file types** — backs up all completed files: shared screen MP4, audio-only M4A, transcript VTT, gallery view MP4, etc.
- **Storage locations** — an admin can configure multiple named Drive folders (storage locations) and choose between them per webhook action

---

## Prerequisites

- A connected Google account (shared with the YouTube integration — one OAuth connection covers both)
- The **Google Drive API** enabled in your Google Cloud project
- At least one storage location (Drive folder) configured in the admin UI

---

## Setup

### 1. Enable the Google Drive API

1. Go to [console.cloud.google.com](https://console.cloud.google.com)
2. Under **APIs & Services → Library**, search for **Google Drive API** and enable it

The OAuth credentials and consent screen are shared with the YouTube integration. If YouTube is already connected, Drive is automatically included — no additional credentials needed.

If YouTube isn't set up yet, see [integrations/youtube/README.md](../youtube/README.md) for the full OAuth setup.

### 2. Connect a Google Account

Go to **Integrations → Google** and click **Connect Google Account**. Complete the OAuth flow. The Drive integration uses the same connected account as YouTube.

### 3. Add a Storage Location

1. Go to **Integrations → Google → Storage Locations**
2. Click **+ Add Storage Location**
3. Enter a name (e.g. "Recordings Archive") and the Google Drive folder ID
   - To find a folder ID: open the folder in Google Drive and copy the ID from the URL: `https://drive.google.com/drive/folders/{FOLDER_ID}`

You can add multiple storage locations and choose between them in the webhook action editor.

### 4. Wire the Pipeline Action

In the **Webhooks** admin page, add a `gdrive.save_file` action to your Zoom webhook:

| Parameter | Value |
|---|---|
| All Files | `{{all_files}}` |
| Meeting Topic | `{{meeting_topic}}` (optional) |
| Meeting Date | `{{meeting_date}}` (optional) |
| Storage Location | Select from your configured locations |

---

## Testing

1. Confirm a storage location is configured and the Google account is connected
2. Run a test upload pipeline (see [integrations/zoom/README.md](../zoom/README.md))
3. Check **Admin → Jobs** — the job detail should show a "Google Drive" section with the folder URL
4. Verify the folder was created in Drive with the expected files

---

## Troubleshooting

**"gdrive: stream function not set"**
- The Drive plugin bridges to Zoom's download function via `gdrive.SetStreamFn(zoom.Stream)`, which is wired in `main.go`. This error means the Zoom plugin isn't loaded — confirm `_ "commons/integrations/zoom"` is imported.

**Files upload but Drive folder not found later**
- Check that the parent folder ID in the storage location is the folder's actual ID (not the full URL)
- Confirm the connected Google account has write access to that Drive folder

**Partial backup — some files missing**
- Only files with status `completed` at webhook time are included. Files still processing when Zoom fires the webhook are skipped.
- The transcript file is included only if Zoom provides a `transcript_url` in the webhook payload

**OAuth scopes error during upload**
- The Google account must have been connected with Drive scope. Reconnect via **Integrations → Google → Connect Google Account** to re-authorize with the correct scopes.

---

## Config Keys Reference

Drive uses storage locations rather than a single config key for the folder. Storage locations are rows in the `storage_locations` table, managed via the Integrations page.

| Service | Key | Sensitive | Description |
|---|---|---|---|
| `gdrive` | `enabled` | No | Set `true` to enable Drive backups (optional — Drive is always available if connected) |
| `google` | `connected_email` | No | Set automatically after OAuth — confirms Drive access is authorized |

---

## Architecture Notes

- **No disk writes:** `io.Pipe` is used for all file transfers — the Zoom download reader is piped directly to the Drive upload writer.
- **Import cycle prevention:** the Drive plugin needs Zoom's `StreamFunc` to download recordings, but importing Zoom from Drive would create a cycle. Instead, `gdrive.SetStreamFn(zoom.Stream)` is called in `main.go` after both plugins are initialized.
- **Per-meeting folder naming:** folders are created as `YYYY-MM-DD — Meeting Topic`. If a folder with that name already exists under the parent, Drive will create a second one (Drive allows duplicate folder names). Future work could add a dedup check.
- **Job detail:** the job detail panel in the admin UI shows a link to the Drive backup folder and a list of uploaded files.
