package discord

import (
	"embed"

	"commons/plugin"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func (p *DiscordPlugin) Migrations() []plugin.Migration {
	return plugin.LoadMigrations(migrationsFS, "migrations")
}
