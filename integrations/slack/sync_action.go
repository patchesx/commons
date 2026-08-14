package slack

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
)

// SyncMembersAction implements plugin.ActionType for "slack.sync_members".
// Thin wrapper: calls the existing SyncAllUsers Go function wholesale.
// Will be decomposed into granular actions in Phase 5.
type SyncMembersAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *SyncMembersAction) ID() string                     { return "slack.sync_members" }
func (a *SyncMembersAction) Label() string                  { return "Sync Slack Members" }
func (a *SyncMembersAction) RequiredCapabilities() []string { return []string{"slack.notify"} }
func (a *SyncMembersAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "sync_job_id", Label: "Sync Job ID", Type: "string"},
		{Key: "members_upserted", Label: "Members Upserted", Type: "number"},
	}
}
func (a *SyncMembersAction) ParamSchema() []plugin.ParamDef { return nil }

// Execute calls SyncAllUsers. The function creates its own job record and
// fires slack.team_join per member internally. Output is minimal for now —
// full decomposition in Phase 5 will expose member lists, counts, etc.
func (a *SyncMembersAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	SyncAllUsers(ctx, a.pool)
	return map[string]any{}, nil
}
