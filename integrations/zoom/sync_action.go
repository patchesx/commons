package zoom

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
)

// SyncMeetingsAction implements plugin.ActionType for "zoom.sync_meetings".
// Thin wrapper: calls the existing SyncMeetings Go function.
type SyncMeetingsAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *SyncMeetingsAction) ID() string                     { return "zoom.sync_meetings" }
func (a *SyncMeetingsAction) Label() string                  { return "Sync Zoom Meetings" }
func (a *SyncMeetingsAction) RequiredCapabilities() []string { return nil }
func (a *SyncMeetingsAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *SyncMeetingsAction) ParamSchema() []plugin.ParamDef      { return nil }
func (a *SyncMeetingsAction) Execute(ctx context.Context, _ map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	SyncMeetings(ctx, a.pool, a.encKey)
	return map[string]any{}, nil
}
