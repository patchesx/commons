package matrix

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
)

// SyncMembersAction implements plugin.ActionType for "matrix.sync_members".
// Thin wrapper: calls the existing SyncAllUsers Go function.
type SyncMembersAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *SyncMembersAction) ID() string                     { return "matrix.sync_members" }
func (a *SyncMembersAction) Label() string                  { return "Sync Matrix Members" }
func (a *SyncMembersAction) RequiredCapabilities() []string { return nil }
func (a *SyncMembersAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *SyncMembersAction) ParamSchema() []plugin.ParamDef      { return nil }
func (a *SyncMembersAction) Execute(ctx context.Context, _ map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	SyncAllUsers(ctx, a.pool, a.encKey)
	return map[string]any{}, nil
}
