# Pipeline and Workflow Engine

Commons has a unified workflow engine that processes HTTP webhooks, internal events, and scheduled triggers through the same pipeline model. Pipelines are configured visually in the admin UI (the **Triggers** page) and consist of filters, a chain of actions executed sequentially, with support for conditions, delays, loops, and retry.

---

## Architecture

```
Trigger Sources                    Pipeline Execution
┌──────────────┐                  ┌──────────────────┐
│ HTTP webhook │──────┐           │  pipeline_runs   │ (durable state)
│ Internal event│─────┤           │  pipeline_run_   │ (per-step log)
│ Scheduled    │──────┘           │    steps         │
└──────────────┘                  └──────────────────┘
        │                                │
        ▼                                ▼
┌──────────────────────────────────────────────┐
│           pipeline.RunPipeline               │
│  (unified runner — all three trigger types)  │
├──────────────────────────────────────────────┤
│  1. Evaluate filters (all must pass)         │
│  2. Execute success actions (sequential)     │
│     - Per-action conditions (skip if false)  │
│     - Retry with backoff                     │
│     - Timeout enforcement                    │
│     - PauseSignal → durable delay            │
│     - for_each → loop over array             │
│  3. On failure → action_fail actions         │
│  4. On filter fail → filter_fail actions     │
└──────────────────────────────────────────────┘
```

### Three trigger types

| Type | Config table | Fired by | Job tracking |
|---|---|---|---|
| **HTTP webhook** | `http_trigger_config` | External POST to `/webhook/{slug}` | Yes (job record, cancellation) |
| **Internal event** | (none — `plugin.Fire`) | Application code | No (opt-in via `createJob`) |
| **Scheduled** | `scheduled_trigger_config` | Schedule runner (interval or cron) | Yes (job record, cancellation) |

All three converge on `pipeline.RunPipeline` — a single execution path with filters, actions, conditions, retry, delays, and durable state.

### Durable execution

Pipelines create `pipeline_runs` records that persist:
- **Status**: running → paused (delay) → complete / failed / dead_letter / cancelled
- **Data map**: the shared context that flows through actions (JSONB, persisted before each step)
- **Current step**: index for crash recovery (resume from the action that was executing)
- **Resume at**: when to resume a paused pipeline (for delays)

The resume scheduler (15s poller) resumes paused pipelines when their delay expires. On server restart, `ResumeInterruptedRuns` resumes any pipelines left in running or paused state.

---

## Trigger types in detail

### HTTP webhooks

See [webhooks/README.md](../webhooks/README.md) for the HTTP webhook intake flow (signature verification, processors, data map extraction).

### Internal events

Internal events are fired by application code via `plugin.Fire`:

```go
plugin.Fire(ctx, "slack.member.upserted", userID, map[string]any{
    "user_id":   "U12345",
    "email":     "jane@example.com",
    "member_id": "...",
})
```

The events runner (`events/runner.go`) finds all enabled `trigger_sources` matching the trigger type and runs each pipeline in a goroutine. Event pipelines don't create job records by default (to avoid flooding the jobs table with high-frequency triggers like per-member sync events).

**Fire-once deduplication**: Trigger types can declare `FireOnce() bool`. When true, each (pipeline, entityID) pair fires at most once — tracked in the `trigger_fires` table.

### Scheduled triggers

Scheduled triggers fire on a schedule — either an interval (`1m`, `5m`, `1h`) or a cron expression (`0 9 * * MON` for every Monday 9am). The schedule runner (`scheduler/` package) polls every 30 seconds for due schedules and fires their pipelines.

**Managed triggers**: Plugins seed managed scheduled triggers during `Init` using `store.UpsertManagedScheduledTrigger`. Admins can customize the action chain but can't delete the trigger or change its managed status. This prevents accidental breakage of critical syncs.

**Missed schedules**: Skip, don't catch up. If the server was down, fire once and reset `last_fired_at` to now.

---

## Pipeline execution

### Filters

Filters are declarative conditions evaluated against the data map before any actions run. All filters must pass for the pipeline to proceed. If any filter fails, `filter_fail` actions run (if any) and the pipeline completes.

| Operator | Works on | Example |
|---|---|---|
| `eq`, `neq` | string, boolean | `status eq active` |
| `contains`, `not_contains` | string | `topic contains Board` |
| `gt`, `gte`, `lt`, `lte` | number | `duration gte 30` |
| `exists`, `not_exists` | any | `transcript_url exists` |

Filters can compare against a literal value or a `config_ref` — a reference to a `config_store` value in `service.key` format.

### Actions

Actions execute sequentially. Each action:
1. Looks up its `ActionType` in the plugin registry
2. Evaluates its per-action **condition** (skip if false)
3. Resolves `{{key}}` template references in params against the data map
4. Executes with **retry** and **timeout** (if configured)
5. Returns output keys (merged into the data map)

If an action fails (after all retries), `action_fail` actions run and the pipeline is marked `dead_letter` (if retry was configured) or `failed`.

### Per-action conditions

Each action can have an optional `condition` — a single filter expression evaluated before the action runs. If the condition is false, the action is skipped. This enables branching:

```
1. solidaritytech.lookup_user → outputs: solidaritytech_user_id
2. core.condition (field={{solidaritytech_user_id}}, operator=exists, output_key=has_profile)
3. solidaritytech.set_custom_property
   condition: {field: "has_profile", operator: "eq", value: "true"}
4. slack.dm
   condition: {field: "has_profile", operator: "eq", value: "false"}
```

### Delays

The `core.delay` action pauses the pipeline for a specified duration. The runner persists the pipeline run as `paused` with a `resume_at` timestamp. The resume scheduler resumes the pipeline when the delay expires.

Delays are **durable** — they survive server restarts. The data map is persisted at pause time, so the pipeline resumes with the correct state.

### Loops

The `core.for_each` action iterates over an array in the data map, executing a group of body actions for each item. Body actions are linked to the loop via the `action_group` column on `pipeline_actions`.

```
1. slack.fetch_members → outputs: members[]
2. core.for_each (items={{members}}, item_key=member, group=member_loop)
   Body (action_group=member_loop):
     a. slack.upsert_member (member={{member}})
     b. solidaritytech.lookup_user (external_id={{member.id}})
     c. solidaritytech.set_custom_property (user_id={{solidaritytech_user_id}})
3. slack.sync_channels
```

On error, `for_each` can either `continue` (log and proceed to next item) or `abort` (stop the loop).

### Retry and timeout

Each action can have a `retry_config` and `timeout_seconds`:

| Config | Description |
|---|---|
| `max_attempts` | Total attempts including the first (default: 1, no retry) |
| `backoff` | `fixed` or `exponential` |
| `initial_delay` | Delay before first retry (e.g. `5s`) |
| `max_delay` | Cap for exponential backoff (e.g. `60s`) |
| `timeout_seconds` | Max execution time per attempt (NULL = no timeout) |

After exhausting retries, the pipeline run is marked `dead_letter` (instead of `failed`). Dead-letter runs can be re-run from the UI.

**Idempotency**: Actions can implement `plugin.IdempotentAction` to declare whether they're safe to retry. The runner warns when retry is configured for non-idempotent actions.

### Template resolution

Action params support `{{key}}` placeholders replaced with values from the data map:

```json
{
  "channel": "{{notification_channel}}",
  "message": "New member: {{user_name}} ({{user_id}})"
}
```

If a param value is exactly `{{key}}`, it's replaced with the raw data map value (preserving type — arrays, objects, numbers). If the template is embedded in a larger string, it's substituted as a string.

---

## Core actions

Built-in actions in the `integrations/core/` plugin:

| Action | ID | Description |
|---|---|---|
| Set Condition Flag | `core.condition` | Evaluates a value against an operator, sets a boolean flag |
| Set Variable | `core.set_variable` | Sets a named key in the data map |
| Log Message | `core.log` | Logs a message for debugging |
| HTTP Request | `core.http_request` | Generic HTTP call (GET/POST/PUT/PATCH/DELETE) |
| Delay | `core.delay` | Pauses the pipeline for a duration |
| For Each (Loop) | `core.for_each` | Iterates over an array, running body actions per item |

---

## Pipeline runs

Every pipeline execution creates a `pipeline_runs` record with:
- Status: `running` → `paused` / `complete` / `failed` / `dead_letter` / `cancelled`
- Data map (JSONB, persisted before each step for crash recovery)
- Current step index
- Resume timestamp (for delays)
- Error message (on failure)

Per-step execution is logged in `pipeline_run_steps` with input params, output data, status, and timing.

### Re-run

Failed and dead-letter runs can be re-run via `POST /admin/fragments/pipeline-runs/{id}/rerun`:
- `from_step=0` — re-run from the beginning (creates a new run with the original initial data)
- `from_step=1` — re-run from the failed step (uses the persisted data map from the failure point)

---

## Migration guide: Scheduler page → scheduled triggers

Before this change, scheduled jobs were configured on the **Scheduler** admin page using interval-based toggles. Each job is now a **scheduled trigger** on the **Triggers** page.

| Old (Scheduler page) | New (Triggers page) |
|---|---|
| Toggle "Slack User Sync Enabled" | Enable/disable the "Slack Member Sync" scheduled trigger |
| Set "Slack User Sync Interval (minutes)" | Edit the trigger's schedule field (e.g. `5m`) |
| Job runs opaque Go code | Job runs a pipeline of actions (visible, customizable) |

**For most users:** No action needed. Managed triggers are seeded with defaults matching your previous config.

**For custom configurations:** If you changed an interval on the Scheduler page, re-set it on the Triggers page. The Scheduler page has been removed.

**Adding custom steps to a sync:** Open the scheduled trigger's detail page, add actions before or after the sync action. For per-member customization, use the `slack.member.upserted` event trigger.

---

## Related files

- `pipeline/runner.go` — unified pipeline runner
- `pipeline/filters.go` — filter and condition evaluation
- `pipeline/retry.go` — retry with backoff
- `pipeline/resume.go` — resume scheduler + restart recovery
- `scheduler/` — scheduled trigger runner
- `events/runner.go` — internal event dispatcher
- `webhooks/` — HTTP webhook intake
- `store/triggers.go` — trigger source, action, filter store layer
- `store/pipeline_runs.go` — pipeline run store layer
- `store/scheduled_triggers.go` — scheduled trigger store layer
- `integrations/core/` — built-in core actions
