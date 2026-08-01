package plugin

import (
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// Migration represents a single SQL migration owned by a plugin.
// Plugins return a slice of these from their Migrations() method.
// The runner tracks each as (plugin_name, version) in schema_migrations.
type Migration struct {
	Version int
	SQL     string
}

// LoadMigrations reads numbered SQL files from an embedded filesystem and returns
// them as a sorted Migration slice. Files must be named NNN_description.sql where
// NNN is the integer version. Call this from a plugin's Migrations() method:
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	func (p *MyPlugin) Migrations() []plugin.Migration {
//	    return plugin.LoadMigrations(migrationsFS, "migrations")
//	}
func LoadMigrations(fsys fs.ReadFileFS, dir string) []Migration {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}

	var migrations []Migration
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
			continue
		}
		sql, err := fsys.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			continue
		}
		migrations = append(migrations, Migration{Version: version, SQL: string(sql)})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations
}
