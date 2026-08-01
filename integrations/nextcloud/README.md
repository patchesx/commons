# Nextcloud Integration

Backs up meeting recording files to a Nextcloud instance. Provides the `nextcloud.storage` capability and the "Save to Nextcloud" pipeline action, an alternative (or complement) to Google Drive for recording backups.

---

## What it does

- **Recording backup** — the `nextcloud.save_file` pipeline action creates a dated subfolder (`<Meeting Topic> - YYYY-MM-DD`) under a configured storage location and uploads every recording file into it
- **Streaming upload** — files are streamed from the recording source (e.g. Zoom's CDN) directly to Nextcloud via WebDAV; nothing is written to disk
- **Job detail** — backup results (folder path) appear on the job detail page for recording-upload jobs

---

## Prerequisites

- A Nextcloud instance reachable over HTTPS from the app server
- A Nextcloud account to act as the service account (a dedicated bot account is recommended)
- An **app password** for that account: Nextcloud → **Settings → Security → App passwords**. Do not use the account's login password.

---

## Setup

### 1. Configure in Admin UI

Go to **Integrations → Nextcloud** and enter:

| Field | Value |
|---|---|
| Server URL | Your instance URL, e.g. `https://cloud.example.org` |
| Username | The service account's username |
| App Password | The app password generated above |
| Enable Nextcloud | `true` |

### 2. Add a Storage Location

Storage locations are the destination folders backups go into. Manage them on the same Integrations page card — add one with the Nextcloud folder path you want backups under (e.g. `Recordings`).

### 3. Wire the Pipeline Action

On the **Triggers** page, add a **Save to Nextcloud** action to the recording webhook's pipeline:

| Param | Value |
|---|---|
| Files | `{{all_files}}` |
| Meeting Topic | `{{meeting_topic}}` |
| Meeting Date | `{{meeting_date}}` |
| Storage Location | Pick one of your configured storage locations |

---

## Troubleshooting

**Uploads fail with 401**
- Verify the username and app password; app passwords are revocable — check it still exists in Nextcloud's Security settings

**Folder not created**
- The service account must have permission to create folders under the storage location path

**Action fails with "recording streamer not configured"**
- The recording source plugin (Zoom) must be enabled; it provides the streamer that downloads the files being backed up

---

## Config Keys Reference

All stored in `config_store` under service `nextcloud`:

| Key | Sensitive | Description |
|---|---|---|
| `enabled` | No | Set `true` to activate the integration |
| `server_url` | No | Nextcloud instance URL |
| `username` | No | Service account username |
| `app_password` | Yes | App password (not the login password) |

---

## Architecture Notes

- Uploads go through Nextcloud's WebDAV endpoint (`remote.php/dav/files/<user>/...`); intermediate folders are created with MKCOL as needed.
- Backup metadata lives in the plugin-owned `nextcloud.backup_data` table, keyed by job ID.
- The dated-folder name uses the org's configured timezone for the meeting date.
