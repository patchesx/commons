package nextcloud

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
)

// SaveFileAction implements plugin.ActionType for "nextcloud.save_file".
type SaveFileAction struct {
	pool   *pgxpool.Pool
	encKey []byte
	pctx   plugin.PluginContext
}

func (a *SaveFileAction) ID() string    { return "nextcloud.save_file" }
func (a *SaveFileAction) Label() string { return "Save to Nextcloud" }
func (a *SaveFileAction) RequiredCapabilities() []string {
	return []string{"nextcloud.storage"}
}
func (a *SaveFileAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *SaveFileAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "all_files", Label: "Files", Type: "text", Required: true, Dynamic: true,
			Description: "All recording files. Use {{all_files}} to reference files from the incoming recording."},
		{Key: "meeting_topic", Label: "Meeting Topic", Type: "text", Dynamic: true,
			Description: "Used to name the Nextcloud subfolder. Use {{meeting_topic}}."},
		{Key: "meeting_date", Label: "Meeting Date", Type: "text", Dynamic: true,
			Description: "Used to name the Nextcloud subfolder. Use {{meeting_date}}."},
		{Key: "folder_id", Label: "Storage Location", Type: "storage_location_select", Required: true,
			Description: "Storage locations are managed under Integrations → Nextcloud."},
	}
}

func (a *SaveFileAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	folderPath, _ := params["folder_id"].(string)
	if folderPath == "" {
		return nil, fmt.Errorf("nextcloud.save_file: folder_id is required")
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
		return nil, fmt.Errorf("nextcloud.save_file: recording streamer not configured")
	}
	if _, err := Backup(ctx, a.pool, a.encKey, folderPath, allFiles, downloadToken, meetingTopic, meetingDate, streamer); err != nil {
		return nil, fmt.Errorf("nextcloud.save_file: %w", err)
	}
	return nil, nil
}
