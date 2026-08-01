package gdrive

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
)

// SaveFileAction implements plugin.ActionType for "gdrive.save_file".
type SaveFileAction struct {
	pool   *pgxpool.Pool
	encKey []byte
	pctx   plugin.PluginContext
}

func (a *SaveFileAction) ID() string    { return "gdrive.save_file" }
func (a *SaveFileAction) Label() string { return "Save to Google Drive" }
func (a *SaveFileAction) RequiredCapabilities() []string {
	return []string{"gdrive.storage"}
}
func (a *SaveFileAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "folder_id", Label: "Google Drive Folder ID", Type: "string",
			Description: "ID of the Drive folder created for this meeting."},
		{Key: "folder_link", Label: "Google Drive Folder Link", Type: "string",
			Description: "Shareable link to the Drive folder created for this meeting."},
	}
}
func (a *SaveFileAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "all_files", Label: "Files", Type: "text", Required: true, Dynamic: true,
			Description: "All recording files. Use {{all_files}} to reference files from the incoming recording."},
		{Key: "meeting_topic", Label: "Meeting Topic", Type: "text", Dynamic: true,
			Description: "Used to name the Drive folder. Use {{meeting_topic}}."},
		{Key: "meeting_date", Label: "Meeting Date", Type: "text", Dynamic: true,
			Description: "Used to name the Drive folder. Use {{meeting_date}}."},
		{Key: "folder_id", Label: "Storage Location", Type: "storage_location_select", Required: true,
			Description: "Storage locations are managed under Integrations → Google."},
	}
}

func (a *SaveFileAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	folderID, _ := params["folder_id"].(string)
	if folderID == "" {
		return nil, fmt.Errorf("gdrive.save_file: folder_id is required")
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
		return nil, fmt.Errorf("gdrive.save_file: recording streamer not configured")
	}
	createdFolderID, err := Backup(ctx, a.pool, a.encKey, folderID, allFiles, downloadToken, meetingTopic, meetingDate, streamer)
	if err != nil {
		return nil, fmt.Errorf("gdrive.save_file: %w", err)
	}

	jobID, _ := params["job_id"].(string)
	if jobID != "" && createdFolderID != "" {
		if err := SetFolderID(ctx, a.pool, jobID, createdFolderID); err != nil {
			log.Printf("gdrive.save_file: set folder ID: %v", err)
		}
	}

	folderLink := "https://drive.google.com/drive/folders/" + createdFolderID
	return map[string]any{
		"folder_id":   createdFolderID,
		"folder_link": folderLink,
	}, nil
}
