package matrix

import (
	"context"
	"fmt"

	"commons/plugin"
)

// RoomMessageAction implements plugin.ActionType for "matrix.room".
type RoomMessageAction struct{}

func (a *RoomMessageAction) ID() string                          { return "matrix.room" }
func (a *RoomMessageAction) Label() string                       { return "Post to Matrix Room" }
func (a *RoomMessageAction) RequiredCapabilities() []string      { return []string{"matrix.notify"} }
func (a *RoomMessageAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *RoomMessageAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "room_id", Label: "Room ID", Type: "text", Required: true,
			Description: "Matrix room ID to post the message to."},
		{Key: "message_variants", Label: "Message", Type: "message_variants", Required: true, Dynamic: true,
			Description: "Add multiple variants to cycle through them sequentially."},
	}
}
func (a *RoomMessageAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	roomID, _ := params["room_id"].(string)
	tmpl, _ := params["message_template"].(string)
	if roomID == "" {
		return nil, fmt.Errorf("matrix.room: room_id is required")
	}
	if tmpl == "" {
		return nil, fmt.Errorf("matrix.room: message_template is required")
	}
	return nil, PostRoomMessage(ctx, roomID, tmpl)
}

// DirectMessageAction implements plugin.ActionType for "matrix.dm".
type DirectMessageAction struct{}

func (a *DirectMessageAction) ID() string                          { return "matrix.dm" }
func (a *DirectMessageAction) Label() string                       { return "Send Matrix DM" }
func (a *DirectMessageAction) RequiredCapabilities() []string      { return []string{"matrix.notify"} }
func (a *DirectMessageAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *DirectMessageAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "user_id", Label: "User (Matrix ID)", Type: "text", Required: true, Dynamic: true,
			Description: "Matrix user ID, or {{key}} to target a user dynamically from pipeline data."},
		{Key: "message_variants", Label: "Message", Type: "message_variants", Required: true, Dynamic: true,
			Description: "Add multiple variants to cycle through them sequentially."},
	}
}
func (a *DirectMessageAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	userID, _ := params["user_id"].(string)
	tmpl, _ := params["message_template"].(string)
	if userID == "" {
		return nil, fmt.Errorf("matrix.dm: user_id is required")
	}
	if tmpl == "" {
		return nil, fmt.Errorf("matrix.dm: message_template is required")
	}
	return nil, PostDirectMessage(ctx, userID, tmpl)
}
