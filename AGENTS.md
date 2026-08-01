# AGENTS.md — Commons

## Build & Run

```bash
go build -o ./tmp/main .    # build binary
```

Docker Compose is the primary deployment path:
```bash
docker compose up             # starts Postgres 16 + app; entrypoint.sh generates SESSION_SECRET/ENCRYPTION_KEY into /data/secrets.env
HOST_PORT=8081 docker compose up  # bind to a different host port
```

## Test

```bash
go test ./...                 # runs all tests; most need a local Postgres
TEST_DATABASE_URL="postgres://..." go test ./...
```

Tests in `store/`, `api/`, `web/`, `integrations/` create throwaway databases via `testhelpers.SetupTestDB(t)`. This helper:
- Connects to the local Postgres (or the one at `TEST_DATABASE_URL`)
- Creates a uniquely-named DB, runs core + plugin migrations, tears it down after the test
- Uses the fixed key `testencryptionkey1234567890abcde` (`testhelpers.EncKey()`)

Run `go test ./store/... -run TestFoo` to run a single package's tests.

## Architecture

Single-binary Go server with Postgres. Plugin-based integration system.

**Entrypoint**: `main.go` — config load, DB connect, migrations, plugin init, HTTP route wiring, then `ListenAndServe`.

**Plugin system**: Each integration in `integrations/<name>/` self-registers via `init()` / `plugin.Register()`. Plugins implement `plugin.Plugin` and can register routes, scheduled jobs, migrations, and notification backends during `Init()`. Plugins are imported with blank imports in `main.go`.

**Templ templates** (`a-h/templ` v0.3.1001): Source files are `.templ` in `web/templ/` and `web/htmx/`. Generated `_templ.go` files are **committed**. The Dockerfile runs `templ generate` before `go build`. If you edit a `.templ` file, run:
```bash
templ generate
```
before building or the generated Go files will be stale. `templ` is listed as a Go tool in `go.mod`; install with `go install github.com/a-h/templ/cmd/templ@v0.3.1001`.

**Two-tier migrations**: Core migrations in `db/migrations/` (numbered `NNN_name.sql`) run first. Plugin migrations are SQL files under `integrations/<plugin>/migrations/` (same naming convention). Both are tracked in `schema_migrations(plugin, version)`.

**HTMX UI + REST API**: Admin UI uses `a-h/templ` + HTMX. REST API under `/api/` uses session cookies.

**Config**: Bootstrap config from env vars (`DATABASE_URL`, `SESSION_SECRET`, `ENCRYPTION_KEY` required). Dynamic service credentials live encrypted in `config_store` table. Env `ENCRYPTION_KEY` must be 64 hex chars (32 bytes). Sensitive DB config values are stored with `enc:v1:` prefix.

## Repository Layout

| Directory | Purpose |
|---|---|
| `api/` | REST API handlers under `/api/` |
| `config/` | Bootstrap env-var config loading |
| `db/` | Connection pool, core + plugin migration runner |
| `db/migrations/` | Core SQL migration files |
| `events/` | Event pipeline dispatch for triggers |
| `install/` | Setup wizard served at `/install` when no admin exists |
| `integrations/` | One subdirectory per integration (Slack, Zoom, Discord, etc.) |
| `internal/` | Shared helpers (HTTP testing, pipeline utilities, test DB setup) |
| `jobs/` | Scheduled job logic (meeting reminders, overdue books, legislation sync) |
| `legislation/` | Legislative bill tracking |
| `permissions/` | Permission model |
| `platform/` | Interfaces shared across integrations (Notifier, RecordingStreamer, etc.) |
| `plugin/` | Plugin registry, PluginContext, InitAll, scheduled job framework |
| `store/` | Database access layer — all SQL queries live here |
| `util/` | Miscellaneous utilities |
| `web/` | HTTP middleware, sessions, auth, HTMX/templ assets |
| `web/adminui/` | Admin UI page handlers |
| `webhooks/` | Generic webhook processing and trigger pipelines |

## Key Conventions

- **import cycles**: `internal/testhelpers/` cannot import plugin packages (they import `store`). It reads plugin SQL migrations from disk by globbing `integrations/*/migrations/*.sql`.
- **Local dev**: Copy `.env.example` to `.env`, set `POSTGRES_PASSWORD`, set `INSTALL_MODE=true` to force the setup wizard.
- **`.air.toml` is local-only** (gitignored). Run `air` for live reload during dev.
- **No CI config in this repo** — no `.github/workflows/`, no Makefile, no linter config.
- **Sensitive config re-encryption**: On startup, `reencryptSensitiveConfigs` migrates any plaintext sensitive config values to encrypted form. This is a one-way migration — once encrypted, decryption uses the same key.
