package zoom

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
)

// DeleteRecordingAction implements plugin.ActionType for "zoom.delete_recording".
type DeleteRecordingAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *DeleteRecordingAction) ID() string    { return "zoom.delete_recording" }
func (a *DeleteRecordingAction) Label() string { return "Delete Zoom Recording" }
func (a *DeleteRecordingAction) RequiredCapabilities() []string {
	return []string{"zoom.recordings"}
}
func (a *DeleteRecordingAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *DeleteRecordingAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "meeting_uuid", Label: "Meeting UUID", Type: "text", Required: true, Dynamic: true,
			Description: "Zoom meeting UUID. Use {{meeting_uuid}} to reference the incoming recording."},
	}
}

func (a *DeleteRecordingAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	meetingUUID, _ := params["meeting_uuid"].(string)
	if meetingUUID == "" {
		return nil, fmt.Errorf("zoom.delete_recording: meeting_uuid is required")
	}
	if err := DeleteRecording(ctx, a.pool, meetingUUID, a.encKey); err != nil {
		return nil, fmt.Errorf("zoom.delete_recording: %w", err)
	}
	return nil, nil
}
