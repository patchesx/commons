<!-- Context: project-intelligence/technical | Priority: critical | Version: 1.0 | Updated: 2026-07-11 -->

# Technical Domain — Commons

> Single-binary Go server with Postgres, plugin-based integrations, and HTMX admin UI.

## Primary Stack

| Layer | Technology | Version | Rationale |
|---|---|---|---|
| Language | Go | 1.25 | Single binary, no runtime deps |
| Database | PostgreSQL | 16 | pgx/v5 driver, Docker Compose managed |
| DB Driver | pgx/v5 | v5.8.0 | Native Postgres, connection pooling |
| Templates | a-h/templ | v0.3.1001 | Type-safe Go templates, compiles to Go |
| UI | HTMX | — | Hypermedia-driven, no JS framework |
| Auth | bcrypt + session cookies | — | Password hashing, cookie-based sessions |
| Encryption | AES-256-GCM | — | Config values encrypted in config_store |
| Container | Docker Compose | — | `docker compose up` starts Postgres + app |

## Architecture

```
Type: Single binary with plugin system
Pattern: Plugins self-register via init() + blank imports in main.go
```

**Startup flow**: `main.go` → config.Load() → db.Connect() → db.RunMigrations(core→plugins) → plugin.InitAll() → http.ListenAndServe

**Plugin system**: Integrations in `integrations/<name>/` implement `plugin.Plugin`. Blank imports in `main.go` trigger `init()` → `plugin.Register()`. During `InitAll()`, plugins register routes, scheduled jobs, migrations, and notification backends via `PluginContext`.

**Two-tier migrations**: Core (`db/migrations/NNN_name.sql`) run first, then plugin migrations (`integrations/*/migrations/NNN_name.sql`). Tracked in `schema_migrations(plugin, version)`.

## Key Patterns

### API Handler
```go
// REST endpoints under /api/ — session cookie auth
mux.Handle("POST /api/resources", apiAuth(api.CreateResource(pool)))

// API handler signature: func(pool *pgxpool.Pool) http.HandlerFunc
func CreateResource(pool *pgxpool.Pool) http.HandlerFunc { ... }
```

### Templ Component
```go
// .templ source → templ generate → *_templ.go (committed)
// Component = Go function returning templ.Component
templ LoginPage() {
    @layout.Base("Login") {
        <form hx-post="/login" ...>...</form>
    }
}
```

### Plugin Registration
```go
// integrations/zoom/plugin.go
func init() { plugin.Register(&ZoomPlugin{}) }

// main.go imports plugin packages with blank imports:
import _ "commons/integrations/zoom"
```

### DB Access
```go
// All DB queries live in store/ package
func GetServiceConfig(ctx context.Context, pool *pgxpool.Pool, service, key string, encKey []byte) (string, error)
```

### Test Database
```go
// Per-test throwaway Postgres database with full migrations
db := testhelpers.SetupTestDB(t) // creates DB, runs migrations, tears down after
// Use TEST_DATABASE_URL env var to target a remote Postgres
```

## Naming Conventions

| Type | Convention | Example |
|---|---|---|
| Packages | single lowercase word | `store`, `plugin`, `api` |
| Files | snake_case | `config_store.go`, `channel_requests_test.go` |
| DB columns | snake_case | `created_at`, `display_name` |
| Migrations | NNN_name.sql | `001_users.sql`, `058_auth_unification.sql` |
| Go symbols | PascalCase/MixedCaps (Go standard) | `PluginContext`, `SetupTestDB` |
| Config keys | snake_case | `meeting_reminders_enabled` |

## Code Standards

- **All SQL lives in `store/` package** — no inline queries in handlers
- **Plugin migrations are SQL files** on disk; testhelpers reads them via glob to avoid import cycles
- **Import cycles**: `internal/testhelpers/` reads plugin SQL from `integrations/*/migrations/*.sql` because plugins import `store` and store tests import testhelpers
- **Build**: `go build -o ./tmp/main .` (or `templ generate` first if .templ files changed)
- **Test**: `go test ./...` (needs local Postgres; set `TEST_DATABASE_URL` to override)
- **Live reload**: `air` (uses `.air.toml`, which is gitignored)
- **No Makefile, no CI config, no linter config** in this repo

## Security

| Requirement | Implementation |
|---|---|
| Encrypt sensitive config | AES-256-GCM, stored as `enc:v1:<ciphertext>` in `config_store` |
| Encrypt env var | `ENCRYPTION_KEY` must be 64 hex chars (32 bytes); generate with `openssl rand -hex 32` |
| Password hashing | bcrypt cost 12; `go run . hashpw` generates hash for `ADMIN_PASSWORD_HASH` |
| Session cookies | `SECURE_COOKIES=true` by default (HTTPS); set `false` for local HTTP |
| Re-encryption | `reencryptSensitiveConfigs` migrates plaintext→encrypted on startup (one-way) |
| Session secrets | `entrypoint.sh` generates `SESSION_SECRET` and `ENCRYPTION_KEY` into `/data/secrets.env` if absent |

## Config

**Required env vars**: `DATABASE_URL`, `SESSION_SECRET`, `ENCRYPTION_KEY`
**Optional**: `ADMIN_USERNAME` + `ADMIN_PASSWORD_HASH` (bootstrap first admin), `INSTALL_MODE=true` (force setup wizard), `SECURE_COOKIES=false` (local HTTP)

## 📂 Codebase References

| Concept | Implementation |
|---|---|
| Plugin interface | `plugin/plugin.go` — `Plugin` interface, `Register()`, `InitAll()` |
| Plugin context | `plugin/context.go` — `PluginContext` implementation |
| Migrations runner | `db/db.go` — `RunMigrations()`, core + plugin ordering |
| Config loading | `config/config.go` — `Load()` from env vars |
| Test DB helper | `internal/testhelpers/db.go` — `SetupTestDB(t)`, `EncKey()` |
| Docker entrypoint | `entrypoint.sh` — generates secrets into `/data/secrets.env` |
| Main entrypoint | `main.go` — full startup sequence, route wiring |
| Build/live reload | `.air.toml` (gitignored) |

## Related Files

- `business-domain.md` — What this server does for its users
- `decisions-log.md` — Why architecture choices were made
- `living-notes.md` — Active issues, debt, open questions
- `AGENTS.md` — Quick start commands and conventions
