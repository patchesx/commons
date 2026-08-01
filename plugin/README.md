# Plugin System

Commons uses a self-registering plugin architecture. Every integration (Slack, Zoom, Discord, YouTube, Google Drive, S3, Nextcloud, Matrix, Vimeo) is a plugin that lives in `integrations/<name>/` and registers itself at import time. The core binary has no hardcoded knowledge of any specific integration — it only knows the `Plugin` interface and the `PluginContext` that provides core services.

---

## How it works

1. Each plugin package has an `init()` function that calls `plugin.Register(&MyPlugin{})`.
2. `main.go` blank-imports each plugin package: `_ "commons/integrations/zoom"`.
3. At startup, `plugin.InitAll(pctx)` calls `Init()` on every registered plugin in registration order.
4. During `Init()`, plugins register routes, scheduled jobs, webhook processors, action types, trigger types, and capability providers on the `PluginContext`.
5. After `InitAll`, `plugin.FinalizeActionTypes()` disables any action type whose required capabilities aren't satisfied.

---

## The Plugin interface

```go
type Plugin interface {
    Name() string           // unique identifier, e.g. "zoom", "slack"
    Version() string        // semver string
    Provides() []string     // capability strings, e.g. "zoom.recordings"
    Label() string          // human-readable name for the admin UI
    Migrations() []Migration // plugin-owned SQL migrations
    Init(ctx PluginContext) error
}
```

### Registration pattern

Every plugin follows the same pattern in `integrations/<name>/plugin.go`:

```go
package zoom

import "commons/plugin"

type ZoomPlugin struct{}

func init() {
    plugin.Register(&ZoomPlugin{})
}

func (p *ZoomPlugin) Name() string    { return "zoom" }
func (p *ZoomPlugin) Label() string   { return "Zoom" }
func (p *ZoomPlugin) Version() string { return "1.0.0" }

func (p *ZoomPlugin) Provides() []string {
    return []string{"zoom.recordings", "zoom.scheduling"}
}

func (p *ZoomPlugin) Migrations() []plugin.Migration {
    return nil // or plugin.LoadMigrations(migrationsFS, "migrations")
}

func (p *ZoomPlugin) Init(pctx plugin.PluginContext) error {
    // Register routes, jobs, processors, actions, etc.
    return nil
}
```

Then in `main.go`:

```go
import _ "commons/integrations/zoom"
```

The blank import triggers the `init()` function, which registers the plugin. No further wiring is needed.

---

## PluginContext

`PluginContext` is the service locator passed to each plugin's `Init()`. It provides access to the database, encryption key, HTTP mux, and registries for routes, jobs, and platform interfaces.

### Core services

| Method | Returns | Description |
|---|---|---|
| `DB()` | `*pgxpool.Pool` | Postgres connection pool |
| `EncKey()` | `[]byte` | 32-byte AES-256 encryption key |
| `Config()` | `store.ConfigGetter` | Read-only access to `config_store` values |
| `Notifier()` | `platform.Notifier` | Composite notifier (fans out to all registered chat platforms) |
| `RegisteredPlugins()` | `[]Plugin` | All registered plugins |
| `HasCapability(cap)` | `bool` | Check if any plugin provides a capability string |

### Platform interfaces

Plugins can register implementations of shared platform interfaces so other plugins can use them without import dependencies:

| Method | Interface | Registered by |
|---|---|---|
| `SetRecordingStreamer(s)` / `RecordingStreamer()` | `platform.RecordingStreamer` | The recording source plugin (Zoom) |
| `SetMeetingScheduler(s)` / `MeetingScheduler()` | `platform.MeetingScheduler` | The scheduling plugin (Zoom) |
| `AddNotifier(n)` | `platform.Notifier` | Each chat platform (Slack, Discord, Matrix) |

This is how the YouTube plugin downloads a Zoom recording without importing the Zoom package: Zoom registers a `RecordingStreamer` during `Init()`, and YouTube calls `pctx.RecordingStreamer().Stream(...)`.

See `platform/interfaces.go` for the full interface definitions.

### Route registration

```go
// Public route (no auth)
pctx.RegisterRoute("GET", "/api/my-integration/webhook", handler)

// Auth-protected route (session cookie or API auth)
pctx.RegisterAuthRoute("POST", "/api/my-integration/action", handler)
```

Routes are registered on the same `http.ServeMux` as the core routes. Auth routes are wrapped with the API auth middleware.

### Scheduled jobs

```go
pctx.RegisterScheduledJob(
    "my_job_enabled",           // config_store key under service "jobs"
    "my_job_interval_minutes",  // config_store key under service "jobs"
    30*time.Second,             // startup delay before first poll
    func() {
        // Do work. Runs when enabled="true" and interval has elapsed.
    },
)
```

The job runner polls `config_store` every minute. Admins toggle jobs on/off and set intervals from the **Scheduler** admin page. See `plugin/jobs.go` for details.

### UI registration

| Method | Description |
|---|---|
| `RegisterNavItem(label, path)` | Adds a sidebar item to the admin UI |
| `RegisterSettingsCard(group, path, handler)` | Mounts an HTMX fragment as a settings card |
| `RegisterIntegrationCard(spec)` | Replaces default credential cards on the Integrations page |

### Job cancellation

Long-running jobs (like YouTube uploads) register a cancel function so admins can cancel them from the UI:

```go
ctx, cancel := context.WithCancel(parentCtx)
pctx.RegisterJob(jobID, cancel)
defer pctx.UnregisterJob(jobID)

// Admin UI calls pctx.CancelJob(jobID) to cancel
```

---

## Capabilities

Capabilities are string identifiers that a plugin declares it provides. Action types declare required capabilities; if a required capability isn't provided by any registered plugin, the action type is disabled at startup.

```go
// Zoom provides:
func (p *ZoomPlugin) Provides() []string {
    return []string{"zoom.recordings", "zoom.scheduling"}
}

// YouTube's upload action requires:
func (a *UploadVideoAction) RequiredCapabilities() []string {
    return []string{"zoom.recordings"} // only works if Zoom is loaded
}
```

This lets the system gracefully degrade — if you remove the Zoom plugin from `main.go`, the YouTube upload action is automatically disabled rather than crashing at runtime.

---

## Webhook processors

A `WebhookProcessor` handles the intake for a specific integration's webhooks — verifying signatures, parsing payloads, and producing a data map for the pipeline. See [webhooks/README.md](../webhooks/README.md) for the full pipeline flow.

```go
type WebhookProcessor interface {
    Type() string               // unique ID, e.g. "zoom_recording"
    Label() string              // human-readable name for UI dropdowns
    VerificationMethod() string // "none", "header_secret", or "hmac_sha256"
    DataSchema() []DataFieldDef // fields this processor puts in the data map
    Extract(ctx, w, r, payload, webhook) (map[string]any, error)
}
```

Register during `Init()`:

```go
proc := &MyWebhookProcessor{pool: pool, encKey: encKey}
plugin.RegisterProcessor(proc)
store.SeedWebhookProcessorType(ctx, pool, proc.Type(), proc.Label())
```

`Extract` is responsible for writing the HTTP response in all cases. Return `(dataMap, nil)` to run the pipeline, `(nil, nil)` to skip (challenge reply, dedup, etc.), or `(nil, err)` for an internal error.

---

## Action types

Action types are the building blocks of webhook pipelines. Each action type declares a param schema (what the admin configures) and an output schema (what it produces for downstream steps).

```go
type ActionType interface {
    ID() string                    // e.g. "youtube.upload_video"
    Label() string                 // for UI dropdowns
    ParamSchema() []ParamDef       // configurable fields
    OutputSchema() []DataFieldDef  // keys added to the pipeline data map
    RequiredCapabilities() []string
    Execute(ctx, params, ac ActionContext) (map[string]any, error)
}
```

Register during `Init()`:

```go
plugin.RegisterActionType(&MyAction{pool: pool, encKey: encKey})
```

`ParamDef` supports these field types: `text`, `boolean`, `select`, `channel_select`, `user_select`, `storage_location_select`. Dynamic params (those accepting `{{key}}` template references) set `Dynamic: true`.

The `ActionContext` provides `JobID()`, `SetPhase()`, and `ClearPhase()` for long-running actions that want to report progress.

---

## Trigger types

Trigger types represent internal event sources (as opposed to HTTP webhooks). Plugins register trigger types; the event dispatcher fires them when internal events occur.

```go
type TriggerType interface {
    ID() string               // e.g. "slack.team_join"
    Label() string
    DataSchema() []DataFieldDef
    FireOnce() bool           // if true, each (pipeline, entityID) fires at most once
}
```

Register during `Init()`:

```go
plugin.RegisterTriggerType(&MyTriggerType{})
```

Fire a trigger from anywhere in the codebase:

```go
plugin.Fire(ctx, "my.trigger_id", entityID, map[string]any{
    "user_id": "123",
    "name":    "Jane",
})
```

The event runner (`events/runner.go`) finds all enabled pipelines registered for that trigger type and runs them in separate goroutines. See [webhooks/README.md](../webhooks/README.md) for the full event pipeline flow.

---

## Plugin migrations

Plugins can own SQL migrations. These run after core migrations, tracked as `(plugin_name, version)` in `schema_migrations`.

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

func (p *MyPlugin) Migrations() []plugin.Migration {
    return plugin.LoadMigrations(migrationsFS, "migrations")
}
```

Migration files are named `NNN_description.sql` (e.g. `001_create_table.sql`, `002_add_column.sql`). See [db/README.md](../db/README.md) for the full migration system.

---

## SetupProvider (optional)

Plugins that implement the `SetupProvider` interface get a guided setup section on their integration detail page:

```go
type SetupProvider interface {
    SetupHandler() http.HandlerFunc
}
```

The handler is auto-mounted at `GET /admin/fragments/integrations/{name}/setup` by `InitAll` after the plugin's `Init` returns. The admin UI renders it as a collapsible section on the integration's detail page.

---

## Building a new integration

1. **Create the package**: `integrations/myintegration/`

2. **Write `plugin.go`** following the registration pattern above. Implement the `Plugin` interface.

3. **Add migrations** (if needed): create `integrations/myintegration/migrations/NNN_name.sql` and return them from `Migrations()` using `plugin.LoadMigrations`.

4. **Implement `Init()`**: register routes, scheduled jobs, webhook processors, action types, and trigger types as needed.

5. **Import in `main.go`**: add `_ "commons/integrations/myintegration"` to the import block.

6. **Write a README**: `integrations/myintegration/README.md` following the format used by existing integrations (What it does, Prerequisites, Setup, Config Keys Reference, Architecture Notes).

7. **Test**: run `go test ./integrations/myintegration/...` with a local Postgres.

### Reference plugins

- **Slack** (`integrations/slack/`) — the production-tested reference for a chat platform plugin. Implements notifier, member interface, webhook processors, and pipeline actions.
- **Zoom** (`integrations/zoom/`) — the reference for a recording source plugin. Implements webhook processor, recording streamer, meeting scheduler, and pipeline actions.
- **Discord / Matrix** (`integrations/discord/`, `integrations/matrix/`) — alternative chat platform implementations. Good templates for new chat integrations.
- **YouTube** (`integrations/youtube/`) — the reference for a video host plugin. Implements a pipeline action with streaming upload.
- **S3 Storage** (`integrations/s3storage/`) — the reference for a storage backend plugin. Simple, focused implementation.
