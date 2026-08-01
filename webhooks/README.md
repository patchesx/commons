# Webhooks and Trigger Pipelines

Commons has a unified automation system that processes both incoming HTTP webhooks and internal application events through the same pipeline model. Pipelines are configured visually in the admin UI (the **Triggers** page) and consist of filters and a chain of actions executed sequentially.

---

## Two pipeline sources

| Source | Entry point | Triggered by | Job tracking |
|---|---|---|---|
| **HTTP webhooks** | `webhooks/handler.go` → `RunPipeline()` | External POST to `/webhook/{slug}` | Creates a job record (or uses one pre-created by the processor) |
| **Internal events** | `events/runner.go` → `runPipeline()` | `plugin.Fire()` from application code | No job record; runs in background goroutines |

Both use the same action type registry and the same `{{key}}` template resolution. The difference is how they're triggered and whether they create a job.

---

## HTTP webhooks

### Routing

All webhooks are received at:

```
POST /webhook/{slug...}
```

The slug (e.g. `slack/events`, `zoom`, `discord/interactions`) is looked up in the `webhooks` table. If the webhook is disabled or doesn't exist, the handler returns 404.

### Signature verification

Each webhook row configures a verification mode:

| Mode | How it works |
|---|---|
| `none` | No verification (or the processor handles it internally) |
| `header_secret` | The value of a configured HTTP header must match the stored secret (constant-time comparison) |
| `hmac_sha256` | HMAC-SHA256 of the request body, compared against the header value as `sha256=<hex>` |

Verification happens before the processor runs. Requests older than 5 minutes are rejected by processors that check timestamps (Slack, Zoom).

### Webhook processors

If a webhook has a `processor_type` assigned, the processor handles payload parsing and verification. The processor's `Extract()` method:

1. Verifies the payload (e.g. Slack signature, Zoom HMAC)
2. Writes the HTTP response (always — the handler does not write a response when a processor is present)
3. Returns a data map for the pipeline, or `nil` to skip the pipeline (challenge reply, dedup, unsupported event)

Processors are registered by plugins during `Init()`. See [plugin/README.md](../plugin/README.md) for the `WebhookProcessor` interface.

If no processor is assigned, the webhook runs in "generic mode" — query string and body params (JSON or form-encoded) are extracted into a flat string map and passed directly to actions.

### Data map

The data map is the shared context that flows through the pipeline. It starts with whatever the processor (or generic param extraction) produces, then gets these reserved keys injected:

| Key | Description |
|---|---|
| `job_id` | The pipeline job's database ID |
| `webhook_id` | The webhook row's ID |
| `webhook_slug` | The webhook's URL slug |

As each action executes, its output keys are merged into the data map and become available to subsequent actions as `{{key}}` template references.

---

## Pipeline execution

```
Webhook received
    │
    ▼
Signature verification
    │
    ▼
Processor.Extract()  ────►  data map
    │
    ▼
Create job record (status: running)
    │
    ▼
Evaluate filters (all must pass)
    │           │
    │           └── fail ──► run filter_fail actions ──► complete job
    │
    ▼
Execute success actions (sequential)
    │           │
    │           └── error ──► run action_fail actions ──► fail job
    │
    ▼
Complete job (status: complete)
```

### Filters

Filters are conditions evaluated against the data map before any actions run. All filters must pass for the pipeline to proceed. If any filter fails, the pipeline runs `filter_fail` actions (if any) and completes.

| Operator | Works on | Example |
|---|---|---|
| `eq` | string, boolean | `meeting_topic eq Town Hall` |
| `neq` | string | `host_email neq test@example.com` |
| `contains` | string | `meeting_topic contains Board` |
| `not_contains` | string | `meeting_topic not_contains Test` |
| `gt`, `gte`, `lt`, `lte` | number | `duration_minutes gte 30` |
| `exists` | any | `transcript_url exists` |
| `not_exists` | any | `error_message not_exists` |

Filters can compare against a literal value or a `config_ref` — a reference to a `config_store` value in `service.key` format. This lets filters use dynamic admin-configured values without code changes.

On any resolution error (config lookup failure, type mismatch), filters fail open (return true) so pipeline runs aren't silently dropped due to infrastructure issues.

Numeric filters support `value_scale` — a multiplier applied to the comparison value (e.g. to convert between seconds and minutes).

### Actions

Actions execute sequentially. Each action:

1. Looks up its `ActionType` in the plugin registry
2. Resolves `{{key}}` template references in its params against the data map
3. Executes and returns output keys (merged into the data map)
4. Can set a "phase" on the job record for progress reporting

If an action fails, the pipeline runs `action_fail` actions (if any) and the job is marked as failed.

### run_on modes

Actions are partitioned by their `run_on` field:

| `run_on` | When it runs |
|---|---|
| `success` (default) | When all filters pass, in sequence |
| `filter_fail` | When a filter fails (best-effort, errors logged) |
| `action_fail` | When a success action fails (best-effort, errors logged) |

Failure actions run in best-effort mode — their errors are logged but don't affect the job outcome.

### Message variants

Actions with a `message_variants` param in their config get round-robin variant selection. Each execution claims the next variant using a cursor stored in the database (`ClaimActionVariantCursor`), so consecutive pipeline runs cycle through different message templates.

### Template resolution

Action params support `{{key}}` placeholders that are replaced with values from the data map at execution time:

```json
{
  "channel": "{{upload_notification_channel}}",
  "message": "New upload: {{youtube_url}}"
}
```

The resolver handles both string params (template substitution) and non-string params (passed through as-is).

---

## Internal events

Internal events use the same pipeline model but are triggered by application code rather than HTTP requests. They don't create job records and run in background goroutines.

### Firing an event

```go
plugin.Fire(ctx, "slack.team_join", userID, map[string]any{
    "user_id":  "U12345",
    "username": "jane",
    "email":    "jane@example.com",
})
```

### Event runner

The `events/runner.go` dispatcher:

1. Looks up the trigger type in the plugin registry
2. Finds all enabled `trigger_sources` rows matching that trigger type
3. For `FireOnce` triggers, checks `trigger_fires` to enforce at-most-once execution per `(pipeline, entityID)` pair
4. Runs each matching pipeline in its own goroutine

### Fire-once deduplication

Trigger types can declare `FireOnce() bool`. When true, the runner records each `(pipeline_id, entity_id)` pair in the `trigger_fires` table. If the same event fires again for the same entity, the pipeline is skipped. Pass a meaningful `entityID` to `Fire()` when using fire-once triggers.

### Configuring event pipelines

Admins create event pipelines on the **Triggers** page:

1. Choose a trigger type (registered by plugins)
2. Add actions (same action type registry as webhooks)
3. Configure action params with `{{key}}` references to the trigger's data schema

The trigger type's `DataSchema()` declares what keys will be available, so the UI can show them as available variables.

---

## Job tracking

HTTP webhook pipelines create a job record in the `jobs` table:

| Field | Value |
|---|---|
| `type` | `pipeline` |
| `feature` | `webhook_pipeline` |
| `trigger` | `webhook` |
| `status` | `pending` → `running` → `complete` / `failed` |

If the processor pre-creates a job (e.g. Zoom creates a `recording_upload` job), that job ID is used instead. The pipeline sets the job's `phase` column as actions report progress (e.g. `downloading`, `uploading`, `youtube_processing`).

Jobs are visible on the **Jobs** admin page with their status, phase, and any error messages. Long-running jobs can be cancelled from the UI — the pipeline registers a cancel function via `PluginContext.RegisterJob()` and the cancel endpoint calls `CancelJob()`.

---

## Architecture notes

- **Async execution**: After the processor acknowledges the webhook (writes HTTP 200), the pipeline runs in a background goroutine. This ensures external services (Slack, Zoom) don't time out waiting for pipeline completion.
- **No retry**: If an action fails, the pipeline stops and the job is marked failed. There is no automatic retry — admins re-trigger manually or fix the configuration.
- **1 MB body limit**: Request bodies are capped at 1 MB (`io.LimitReader`). Webhooks with larger payloads are rejected.
- **Query param merging**: For processor-based webhooks, query string params are merged into the data map (processor-extracted keys take precedence). For generic webhooks, both query and body params are extracted.
- **Import cycle avoidance**: The `events` package implements `plugin.EventDispatcher` and is injected from `main.go` via `plugin.SetDispatcher()` to avoid a circular dependency between `plugin` and `events`.
