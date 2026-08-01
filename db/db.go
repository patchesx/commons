package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PluginMigration is a single SQL migration owned by a named plugin.
// Passed to RunMigrations by main after collecting from all registered plugins.
type PluginMigration struct {
	Plugin  string
	Version int
	SQL     string
}

// Connect creates and validates a Postgres connection pool.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database health check failed: %w", err)
	}
	return pool, nil
}

// RunMigrations applies any pending migrations in order:
//  1. Core SQL file migrations (db/migrations/*.sql), tracked as plugin="core"
//  2. Plugin migrations provided by the caller, tracked as (plugin, version)
//
// On first run against a DB that has the old single-column schema_migrations
// table, the table is upgraded to the composite-key format before continuing.
func RunMigrations(ctx context.Context, pool *pgxpool.Pool, pluginMigrations []PluginMigration) error {
	if err := ensureMigrationsTable(ctx, pool); err != nil {
		return err
	}

	if err := runCoreMigrations(ctx, pool); err != nil {
		return err
	}

	return runPluginMigrations(ctx, pool, pluginMigrations)
}

// ensureMigrationsTable creates or upgrades the schema_migrations tracking table.
// If the old single-column (version INTEGER PRIMARY KEY) format is found, it is
// migrated inline to the new (plugin TEXT, version INTEGER) composite-key format.
func ensureMigrationsTable(ctx context.Context, pool *pgxpool.Pool) error {
	// Check whether the table exists at all.
	var tableExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'schema_migrations'
		)
	`).Scan(&tableExists); err != nil {
		return fmt.Errorf("check schema_migrations existence: %w", err)
	}

	if !tableExists {
		// Fresh DB — create the new composite-key table directly.
		_, err := pool.Exec(ctx, `
			CREATE TABLE schema_migrations (
				plugin     TEXT        NOT NULL DEFAULT 'core',
				version    INTEGER     NOT NULL,
				applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY (plugin, version)
			)
		`)
		if err != nil {
			return fmt.Errorf("create schema_migrations table: %w", err)
		}
		return nil
	}

	// Table exists — check whether it already has the plugin column.
	var hasPluginCol bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name   = 'schema_migrations'
			  AND column_name  = 'plugin'
		)
	`).Scan(&hasPluginCol); err != nil {
		return fmt.Errorf("check schema_migrations.plugin column: %w", err)
	}

	if hasPluginCol {
		return nil // Already on new format.
	}

	// Upgrade from old single-column format.
	log.Println("db: upgrading schema_migrations to composite-key format")
	_, err := pool.Exec(ctx, `
		ALTER TABLE schema_migrations RENAME TO schema_migrations_legacy;

		CREATE TABLE schema_migrations (
			plugin     TEXT        NOT NULL DEFAULT 'core',
			version    INTEGER     NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (plugin, version)
		);

		INSERT INTO schema_migrations (plugin, version, applied_at)
		SELECT 'core', version, applied_at FROM schema_migrations_legacy;

		DROP TABLE schema_migrations_legacy;
	`)
	if err != nil {
		return fmt.Errorf("upgrade schema_migrations: %w", err)
	}
	log.Println("db: schema_migrations upgrade complete")
	return nil
}

// runCoreMigrations applies SQL files from db/migrations/ as plugin="core".
func runCoreMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}

	type coreMigration struct {
		version  int
		filename string
	}

	var migrations []coreMigration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("parse migration version from %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, coreMigration{version: version, filename: entry.Name()})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})

	for _, m := range migrations {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE plugin = 'core' AND version = $1)",
			m.version,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check core migration %d: %w", m.version, err)
		}
		if exists {
			continue
		}

		sql, err := migrationsFS.ReadFile("migrations/" + m.filename)
		if err != nil {
			return fmt.Errorf("read migration file %q: %w", m.filename, err)
		}

		if err := applyMigration(ctx, pool, "core", m.version, string(sql)); err != nil {
			return fmt.Errorf("apply core migration %d (%s): %w", m.version, m.filename, err)
		}
	}

	return nil
}

// runPluginMigrations applies plugin-owned migrations in (plugin, version) order.
func runPluginMigrations(ctx context.Context, pool *pgxpool.Pool, migrations []PluginMigration) error {
	// Group by plugin, sort each group by version.
	byPlugin := map[string][]PluginMigration{}
	for _, m := range migrations {
		byPlugin[m.Plugin] = append(byPlugin[m.Plugin], m)
	}
	for name := range byPlugin {
		sort.Slice(byPlugin[name], func(i, j int) bool {
			return byPlugin[name][i].Version < byPlugin[name][j].Version
		})
	}

	// Stable plugin order.
	var pluginNames []string
	seen := map[string]bool{}
	for _, m := range migrations {
		if !seen[m.Plugin] {
			pluginNames = append(pluginNames, m.Plugin)
			seen[m.Plugin] = true
		}
	}

	for _, name := range pluginNames {
		for _, m := range byPlugin[name] {
			var exists bool
			if err := pool.QueryRow(ctx,
				"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE plugin = $1 AND version = $2)",
				name, m.Version,
			).Scan(&exists); err != nil {
				return fmt.Errorf("check plugin %s migration %d: %w", name, m.Version, err)
			}
			if exists {
				continue
			}
			if err := applyMigration(ctx, pool, name, m.Version, m.SQL); err != nil {
				return fmt.Errorf("apply plugin %s migration %d: %w", name, m.Version, err)
			}
			log.Printf("db: [plugin:%s] migration %d applied", name, m.Version)
		}
	}

	return nil
}

// applyMigration executes sql in a transaction and records (plugin, version).
func applyMigration(ctx context.Context, pool *pgxpool.Pool, pluginName string, version int, sql string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if _, err := tx.Exec(ctx, sql); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (plugin, version) VALUES ($1, $2)",
		pluginName, version,
	); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit(ctx)
}
