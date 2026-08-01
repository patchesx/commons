package nextcloud

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// NextcloudPlugin registers the Nextcloud integration with the plugin system.
type NextcloudPlugin struct{}

func init() {
	plugin.Register(&NextcloudPlugin{})
}

func (p *NextcloudPlugin) Name() string    { return "nextcloud" }
func (p *NextcloudPlugin) Label() string   { return "Nextcloud" }
func (p *NextcloudPlugin) Version() string { return "1.0.0" }

func (p *NextcloudPlugin) Migrations() []plugin.Migration { return nil }
func (p *NextcloudPlugin) Provides() []string {
	return []string{"nextcloud.storage"}
}

// Init performs Nextcloud package-level initialization: sets pool and enc key,
// registers action types, a job detail contributor, and the storage locations
// settings card on the Integrations page.
func (p *NextcloudPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	Init(pool, encKey)

	plugin.RegisterJobDetailContributor(store.JobTypeRecordingUpload, func(ctx context.Context, _ *pgxpool.Pool, jobID string) (string, any) {
		backup, err := GetBackupData(ctx, pool, jobID)
		if err != nil || backup == nil {
			return "", nil
		}
		return "nextcloud", backup
	})

	plugin.RegisterActionType(&SaveFileAction{pool: pool, encKey: encKey, pctx: pctx})

	pctx.RegisterSettingsCard("settings", "/admin/fragments/settings/nextcloud-storage", HandleStorageCard(pool))

	return nil
}
