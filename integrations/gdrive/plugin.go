package gdrive

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// GDrivePlugin registers the Google Drive integration with the plugin system.
type GDrivePlugin struct{}

func init() {
	plugin.Register(&GDrivePlugin{})
}

func (p *GDrivePlugin) Name() string    { return "gdrive" }
func (p *GDrivePlugin) Label() string   { return "Google Drive" }
func (p *GDrivePlugin) Version() string { return "1.0.0" }

func (p *GDrivePlugin) Migrations() []plugin.Migration { return nil }
func (p *GDrivePlugin) Provides() []string {
	return []string{"gdrive.storage"}
}

// Init registers a job detail contributor for recording_upload jobs and the
// gdrive.save_file action type.
func (p *GDrivePlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	plugin.RegisterJobDetailContributor(store.JobTypeRecordingUpload, func(ctx context.Context, _ *pgxpool.Pool, jobID string) (string, any) {
		backup, err := GetBackupData(ctx, pool, jobID)
		if err != nil || backup == nil {
			return "", nil
		}
		return "gdrive", backup
	})

	// Register action type.
	plugin.RegisterActionType(&SaveFileAction{pool: pool, encKey: encKey, pctx: pctx})

	return nil
}
