package youtube

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// YouTubePlugin registers the YouTube integration with the plugin system.
type YouTubePlugin struct{}

func init() {
	plugin.Register(&YouTubePlugin{})
}

func (p *YouTubePlugin) Name() string    { return "youtube" }
func (p *YouTubePlugin) Label() string   { return "YouTube" }
func (p *YouTubePlugin) Version() string { return "1.0.0" }

func (p *YouTubePlugin) Migrations() []plugin.Migration { return nil }
func (p *YouTubePlugin) Provides() []string {
	return []string{"youtube.uploads"}
}

// Init registers job detail contributors, action types, HTTP routes, and
// resumes any upload jobs left in "processing" state from a previous run.
func (p *YouTubePlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	// Register job detail contributor: YouTube upload metadata.
	plugin.RegisterJobDetailContributor(store.JobTypeRecordingUpload, func(ctx context.Context, p *pgxpool.Pool, jobID string) (string, any) {
		upload, err := GetUploadData(ctx, p, jobID)
		if err != nil || upload == nil {
			return "", nil
		}
		return "youtube", upload
	})

	// Register the youtube.upload_video action type.
	plugin.RegisterActionType(&UploadVideoAction{pool: pool, encKey: encKey, pctx: pctx})

	// Register the manual upload route.
	pctx.RegisterAuthRoute("POST", "/api/youtube/upload", HandleUploadToYouTube(pool, encKey))

	return nil
}
