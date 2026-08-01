package s3storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// S3Plugin registers the S3-compatible storage integration with the plugin system.
type S3Plugin struct{}

func init() {
	plugin.Register(&S3Plugin{})
}

func (p *S3Plugin) Name() string    { return "s3" }
func (p *S3Plugin) Label() string   { return "S3 Storage" }
func (p *S3Plugin) Version() string { return "1.0.0" }

func (p *S3Plugin) Migrations() []plugin.Migration { return nil }
func (p *S3Plugin) Provides() []string {
	return []string{"s3.storage"}
}

// Init performs S3 package-level initialization: sets pool and enc key,
// registers the save_file action type, a job detail contributor, and
// the storage locations settings card on the Integrations page.
func (p *S3Plugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	Init(pool, encKey)

	plugin.RegisterJobDetailContributor(store.JobTypeRecordingUpload, func(ctx context.Context, _ *pgxpool.Pool, jobID string) (string, any) {
		backup, err := GetBackupData(ctx, pool, jobID)
		if err != nil || backup == nil {
			return "", nil
		}
		return "s3", backup
	})

	plugin.RegisterActionType(&SaveFileAction{pool: pool, encKey: encKey, pctx: pctx})

	pctx.RegisterSettingsCard("settings", "/admin/fragments/settings/s3-storage", HandleStorageCard(pool))

	return nil
}
