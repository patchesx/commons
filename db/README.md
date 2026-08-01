# Database and Migrations

Commons uses Postgres 16 with a two-tier migration system: core migrations for the main schema, and plugin-owned migrations for integration-specific tables. Both are tracked in the same `schema_migrations` table and run automatically on startup.

---

## Connection

`db.Connect()` creates a `pgxpool.Pool` from a `DATABASE_URL` and validates it with a ping. The pool is passed to the store layer, plugins, and HTTP handlers.

```go
pool, err := db.Connect(ctx, cfg.DatabaseURL)
```

---

## Migration system

### schema_migrations table

All migrations are tracked in:

```sql
CREATE TABLE schema_migrations (
    plugin     TEXT        NOT NULL DEFAULT 'core',
    version    INTEGER     NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (plugin, version)
);
```

Core migrations use `plugin = 'core'`. Plugin migrations use the plugin's `Name()` (e.g. `slack`, `discord`).

On first run against a database with the old single-column `schema_migrations` format, the table is automatically upgraded to the composite-key format.

### Execution order

1. **Core migrations** — SQL files in `db/migrations/`, embedded into the binary with `//go:embed`. Applied in version order.
2. **Plugin migrations** — collected from each registered plugin's `Migrations()` method. Applied grouped by plugin, in version order within each plugin.

Each migration runs in a transaction. If it fails, the transaction is rolled back and startup halts. Successfully applied migrations are recorded in `schema_migrations` and skipped on subsequent runs.

### RunMigrations

```go
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, pluginMigrations []PluginMigration) error
```

Called from `main.go` after all plugins are registered. Core migrations always run first, then plugin migrations.

---

## Core migrations

Located in `db/migrations/`, named `NNN_description.sql`:

| File | Contents |
|---|---|
| `100_core_tables.sql` | Users, integrations, resources, contacts, quick links |
| `101_user_identities.sql` | External identity linking (Slack, Discord, Matrix, etc.) |
| `102_auth.sql` | User credentials, password resets |
| `103_roles_permissions.sql` | Roles, permissions, role assignments |
| `104_config_system.sql` | `config_store` table for dynamic service credentials |
| `105_calendar.sql` | Calendars and calendar events |
| `111_jobs.sql` | Job records for pipeline execution |
| `112_zoom.sql` | Zoom recording data, meetings |
| `113_library.sql` | Library books, copies, checkouts, holds |
| `114_legislation.sql` | Legislative bodies, bills, filters, tags |
| `115_solidaritytech.sql` | Solidarity Tech integration + config |
| `132_solidaritytech_api_key.sql` | Solidarity Tech API key config (drops dead webhook channel key) |
| `119_trigger_system.sql` | Webhooks, trigger sources, pipeline actions/filters |
| `120_pipeline_seeds.sql` | Seed data for pipeline configuration |
| `121-128_config_seeds_*.sql` | Config store seed data (bot, recordings, meetings, Slack, UI, auth, library, integrations) |
| `129_role_display_name.sql` | Add `display_name` column to roles for human-readable UI labels |
| `130_redefine_roles.sql` | Rename `web_admin`→`admin`, `viewer`→`member`; demote integration-specific roles to non-system |
| `131_role_groups.sql` | Role groups for group-based role assignment; replaces direct `user_roles` |

### Adding a core migration

1. Create a new file in `db/migrations/` with the next available version number:
   ```
   db/migrations/133_my_feature.sql
   ```
2. Write the SQL. The file runs as a single transaction, so it's atomic.
3. Commit the file. It runs automatically on the next startup.

**Naming convention**: `NNN_short_description.sql` where `NNN` is a zero-padded 3-digit version number. The version is parsed from the prefix before the first underscore.

---

## Plugin migrations

Plugins own their schema changes. Migration files live in `integrations/<name>/migrations/` and are embedded into the plugin's binary.

### Structure

```
integrations/discord/
├── plugin.go
├── migrations/
│   ├── 001_create_role_requests.sql
│   └── 002_add_column.sql
└── ...
```

### Loading migrations

In the plugin's `Migrations()` method:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

func (p *DiscordPlugin) Migrations() []plugin.Migration {
    return plugin.LoadMigrations(migrationsFS, "migrations")
}
```

`plugin.LoadMigrations` reads the embedded filesystem, parses version numbers from filenames, and returns a sorted slice. Files must follow the same `NNN_description.sql` naming convention as core migrations.

### Tracking

Plugin migrations are tracked as `(plugin_name, version)` in `schema_migrations`. For example, Discord's migration 001 is recorded as `('discord', 1)`. This means:

- Plugin migrations are independent of each other — adding a migration to the Slack plugin doesn't affect Discord's migration state.
- Removing a plugin from `main.go` doesn't roll back its migrations. If you re-add the plugin later, its migrations are already marked as applied.
- Plugin migration versions must be unique within a plugin, but can overlap with core or other plugin versions.

---

## Testing

Tests that need a database use `testhelpers.SetupTestDB(t)`, which:

1. Connects to the local Postgres (or `TEST_DATABASE_URL`)
2. Creates a uniquely-named throwaway database
3. Runs all core migrations
4. Globs `integrations/*/migrations/*.sql` from disk and runs all plugin migrations
5. Returns a connection pool and registers cleanup via `t.Cleanup()`

This gives each test an isolated, fully-migrated database. The encryption key is fixed at `testencryptionkey1234567890abcde` (`testhelpers.EncKey()`).

```bash
# Run all tests (needs local Postgres)
TEST_DATABASE_URL="postgres://postgres:pass@localhost:5432/postgres?sslmode=disable" go test ./...

# Run a single package
go test ./store/... -run TestFoo
```

### Import cycle constraint

`internal/testhelpers/` cannot import plugin packages (they import `store`, which would create a cycle). Instead, it reads plugin SQL migrations directly from disk by globbing `integrations/*/migrations/*.sql`. This means plugin migrations must be on disk for tests to pick them up — they're not loaded through the plugin registry.
