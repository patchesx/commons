package slack

import (
	"embed"

	"commons/plugin"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func (p *SlackPlugin) Migrations() []plugin.Migration {
	return plugin.LoadMigrations(migrationsFS, "migrations")
}
