package testhelpers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/db"
)

// pluginMigrations loads plugin-owned SQL migrations from
// integrations/<plugin>/migrations/*.sql so test databases match the
// production schema. testhelpers cannot import the plugin packages (they
// import store, which would create an import cycle with white-box store
// tests), so the files are read from disk. Plugins with inline Go migrations
// are not picked up — keep plugin migrations in SQL files.
func pluginMigrations(t *testing.T) []db.PluginMigration {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve testhelpers source path")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	files, err := filepath.Glob(filepath.Join(root, "integrations", "*", "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("glob plugin migrations: %v", err)
	}
	sort.Strings(files)

	var out []db.PluginMigration
	for _, f := range files {
		pluginName := filepath.Base(filepath.Dir(filepath.Dir(f)))
		parts := strings.SplitN(filepath.Base(f), "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		sqlBytes, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read plugin migration %s: %v", f, err)
		}
		out = append(out, db.PluginMigration{Plugin: pluginName, Version: version, SQL: string(sqlBytes)})
	}
	return out
}

// adminURL returns the URL of the local maintenance database used to create
// and drop per-test databases. Override with TEST_DATABASE_URL (must be a
// postgres:// URL; the database name in it is used only for this maintenance
// connection, never for the tests themselves).
func adminURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres:///postgres?sslmode=disable"
}

// SetupTestDB creates a uniquely-named throwaway database on the local
// Postgres server, runs all migrations, and returns a connected pool. The
// database and pool are torn down automatically when the test (or subtest)
// finishes.
func SetupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		t.Fatalf("connect to local postgres (%s): %v — is Postgres running? Set TEST_DATABASE_URL to override.", adminURL(), err)
	}

	var suffix [8]byte
	rand.Read(suffix[:])
	name := "commons_test_" + hex.EncodeToString(suffix[:])

	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		admin.Close(ctx)
		t.Fatalf("create test database %s: %v", name, err)
	}
	admin.Close(ctx)

	t.Cleanup(func() {
		admin, err := pgx.Connect(ctx, adminURL())
		if err != nil {
			t.Logf("connect to drop test database %s: %v", name, err)
			return
		}
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", name)); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
	})

	u, err := url.Parse(adminURL())
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name

	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := db.RunMigrations(ctx, pool, pluginMigrations(t)); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return pool
}

// EncKey returns a fixed 32-byte AES-256 key for use in tests.
// Never use this key outside of tests.
func EncKey() []byte {
	return []byte("testencryptionkey1234567890abcde")
}
