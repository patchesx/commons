package s3storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
)

// SaveFileAction implements plugin.ActionType for "s3.save_file".
type SaveFileAction struct {
	pool   *pgxpool.Pool
	encKey []byte
	pctx   plugin.PluginContext
}

func (a *SaveFileAction) ID() string    { return "s3.save_file" }
func (a *SaveFileAction) Label() string { return "Save to S3" }
func (a *SaveFileAction) RequiredCapabilities() []string {
	return []string{"s3.storage"}
}
func (a *SaveFileAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *SaveFileAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "all_files", Label: "Files", Type: "text", Required: true, Dynamic: true,
			Description: "All recording files. Use {{all_files}} to reference files from the incoming recording."},
		{Key: "meeting_topic", Label: "Meeting Topic", Type: "text", Dynamic: true,
			Description: "Used to name the S3 key prefix. Use {{meeting_topic}}."},
		{Key: "meeting_date", Label: "Meeting Date", Type: "text", Dynamic: true,
			Description: "Used to name the S3 key prefix. Use {{meeting_date}}."},
		{Key: "folder_id", Label: "Storage Location", Type: "storage_location_select", Required: true,
			Description: "Storage locations are managed under Integrations → S3 Storage."},
	}
}

func (a *SaveFileAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	keyPrefix, _ := params["folder_id"].(string)
	if keyPrefix == "" {
		return nil, fmt.Errorf("s3.save_file: folder_id is required")
	}
	allFiles, _ := params["all_files"].([]platform.RecordingFile)
	meetingTopic, _ := params["meeting_topic"].(string)
	downloadToken, _ := params["download_token"].(string)

	var meetingDate time.Time
	if t, ok := params["meeting_date"].(time.Time); ok {
		meetingDate = t
	}

	streamer := a.pctx.RecordingStreamer()
	if streamer == nil {
		return nil, fmt.Errorf("s3.save_file: recording streamer not configured")
	}
	if _, err := Backup(ctx, a.pool, a.encKey, keyPrefix, allFiles, downloadToken, meetingTopic, meetingDate, streamer); err != nil {
		return nil, fmt.Errorf("s3.save_file: %w", err)
	}
	return nil, nil
}
