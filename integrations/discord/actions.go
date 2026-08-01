package discord

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
)

// ChannelMessageAction implements plugin.ActionType for "discord.channel".
type ChannelMessageAction struct{}

func (a *ChannelMessageAction) ID() string                          { return "discord.channel" }
func (a *ChannelMessageAction) Label() string                       { return "Post to Discord Channel" }
func (a *ChannelMessageAction) RequiredCapabilities() []string      { return []string{"discord.notify"} }
func (a *ChannelMessageAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *ChannelMessageAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "channel_id", Label: "Channel", Type: "channel_select", Required: true,
			Description: "Channels are fetched from your connected Discord server."},
		{Key: "message_variants", Label: "Message", Type: "message_variants", Required: true, Dynamic: true,
			Description: "Add multiple variants to cycle through them sequentially."},
	}
}
func (a *ChannelMessageAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	channelID, _ := params["channel_id"].(string)
	tmpl, _ := params["message_template"].(string)
	if channelID == "" {
		return nil, fmt.Errorf("discord.channel: channel_id is required")
	}
	if tmpl == "" {
		return nil, fmt.Errorf("discord.channel: message_template is required")
	}
	return nil, PostChannelMessage(ctx, channelID, tmpl)
}

// DirectMessageAction implements plugin.ActionType for "discord.dm".
type DirectMessageAction struct{}

func (a *DirectMessageAction) ID() string                          { return "discord.dm" }
func (a *DirectMessageAction) Label() string                       { return "Send Discord DM" }
func (a *DirectMessageAction) RequiredCapabilities() []string      { return []string{"discord.notify"} }
func (a *DirectMessageAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *DirectMessageAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "user_id", Label: "User (Discord ID)", Type: "text", Required: true, Dynamic: true,
			Description: "Discord user ID, or {{key}} to target a user dynamically from pipeline data."},
		{Key: "message_variants", Label: "Message", Type: "message_variants", Required: true, Dynamic: true,
			Description: "Add multiple variants to cycle through them sequentially."},
	}
}
func (a *DirectMessageAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	userID, _ := params["user_id"].(string)
	tmpl, _ := params["message_template"].(string)
	if userID == "" {
		return nil, fmt.Errorf("discord.dm: user_id is required")
	}
	if tmpl == "" {
		return nil, fmt.Errorf("discord.dm: message_template is required")
	}
	return nil, PostDirectMessage(ctx, userID, tmpl)
}

// HandleInteractionsAction implements plugin.ActionType for "discord.handle_interactions".
type HandleInteractionsAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *HandleInteractionsAction) ID() string    { return "discord.handle_interactions" }
func (a *HandleInteractionsAction) Label() string { return "Route Discord Interactions" }
func (a *HandleInteractionsAction) RequiredCapabilities() []string {
	return []string{"discord.app_home"}
}
func (a *HandleInteractionsAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *HandleInteractionsAction) ParamSchema() []plugin.ParamDef      { return nil }
func (a *HandleInteractionsAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	rawBody, err := extractRawBody(params)
	if err != nil {
		return nil, fmt.Errorf("discord.handle_interactions: %w", err)
	}
	DispatchInteraction(ctx, a.pool, a.encKey, rawBody)
	return nil, nil
}

// extractRawBody pulls string from params["raw_body"].
func extractRawBody(params map[string]any) (string, error) {
	switch v := params["raw_body"].(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	}
	return "", fmt.Errorf("raw_body missing or wrong type")
}
