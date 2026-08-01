# S3 Storage Integration

Backs up meeting recording files to an S3 bucket. Works with AWS S3 and S3-compatible services (MinIO, Cloudflare R2, Backblaze B2, etc.). Provides the `s3.storage` capability and the "Save to S3" pipeline action.

---

## What it does

- **Recording backup** — the `s3.save_file` pipeline action uploads every recording file under a dated key prefix (`<location prefix>/<Meeting Topic> - YYYY-MM-DD/`)
- **Streaming upload** — files are streamed from the recording source (e.g. Zoom's CDN) directly to the bucket; nothing is written to disk
- **Job detail** — backup results (key prefix) appear on the job detail page for recording-upload jobs

---

## Prerequisites

- An S3 bucket on AWS or an S3-compatible service
- Access credentials (access key ID + secret access key) with permission to put objects in that bucket

---

## Setup

### 1. Configure in Admin UI

Go to **Integrations → S3 Storage** and enter:

| Field | Value |
|---|---|
| Endpoint URL | Only for S3-compatible services (e.g. `https://<accountid>.r2.cloudflarestorage.com`). Leave blank for AWS S3. |
| Bucket | The bucket name |
| Region | AWS region (e.g. `us-east-1`). Most S3-compatible services accept any non-empty value. |
| Access Key ID | Your credential |
| Secret Access Key | Your credential |
| Enable S3 Storage | `true` |

### 2. Add a Storage Location

Storage locations are the key prefixes backups go under. Manage them on the same Integrations page card — add one with the prefix you want (e.g. `recordings`).

### 3. Wire the Pipeline Action

On the **Triggers** page, add a **Save to S3** action to the recording webhook's pipeline:

| Param | Value |
|---|---|
| Files | `{{all_files}}` |
| Meeting Topic | `{{meeting_topic}}` |
| Meeting Date | `{{meeting_date}}` |
| Storage Location | Pick one of your configured storage locations |

---

## Troubleshooting

**Uploads fail with 403**
- Verify the credentials and that they allow `s3:PutObject` on the bucket

**Uploads fail against an S3-compatible service**
- Set the Endpoint URL — without it the client targets AWS
- Set Region to any non-empty value if the service ignores regions

**Action fails with "recording streamer not configured"**
- The recording source plugin (Zoom) must be enabled; it provides the streamer that downloads the files being backed up

---

## Config Keys Reference

All stored in `config_store` under service `s3`:

| Key | Sensitive | Description |
|---|---|---|
| `enabled` | No | Set `true` to activate the integration |
| `endpoint_url` | No | Custom endpoint for S3-compatible services; blank for AWS |
| `bucket` | No | Bucket name |
| `region` | No | AWS region |
| `access_key_id` | Yes | Access key ID |
| `secret_access_key` | Yes | Secret access key |

---

## Architecture Notes

- Backup metadata lives in the plugin-owned `s3storage.backup_data` table, keyed by job ID.
- The dated prefix uses the org's configured timezone for the meeting date.
