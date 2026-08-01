package vimeo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// VimeoPlugin registers the Vimeo integration with the plugin system.
type VimeoPlugin struct{}

func init() {
	plugin.Register(&VimeoPlugin{})
}

func (p *VimeoPlugin) Name() string    { return "vimeo" }
func (p *VimeoPlugin) Label() string   { return "Vimeo" }
func (p *VimeoPlugin) Version() string { return "1.0.0" }

func (p *VimeoPlugin) Migrations() []plugin.Migration { return nil }
func (p *VimeoPlugin) Provides() []string {
	return []string{"vimeo.uploads"}
}

// Init registers job detail contributors, action types, and HTTP routes.
func (p *VimeoPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	// Register job detail contributor: Vimeo upload metadata.
	plugin.RegisterJobDetailContributor(store.JobTypeRecordingUpload, func(ctx context.Context, _ *pgxpool.Pool, jobID string) (string, any) {
		upload, err := GetUploadData(ctx, pool, jobID)
		if err != nil || upload == nil {
			return "", nil
		}
		return "vimeo", upload
	})

	// Register the vimeo.upload_video action type.
	plugin.RegisterActionType(&UploadVideoAction{pool: pool, encKey: encKey, pctx: pctx})

	// Register the manual upload route.
	pctx.RegisterAuthRoute("POST", "/api/vimeo/upload", HandleUploadToVimeo(pool, encKey))

	return nil
}
