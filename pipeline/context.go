// Package pipeline implements the unified pipeline runner used by all three
// trigger types (HTTP webhooks, internal events, scheduled triggers).
// It evaluates filters, creates job records, and executes actions sequentially
// with support for failure paths, phase tracking, and cancellation.
package pipeline

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// CancelRegistry is satisfied by plugin.PluginContext — allows the pipeline
// runner to register a cancel func for each job so cancellation works.
type CancelRegistry interface {
	RegisterJob(id string, cancel context.CancelFunc)
	UnregisterJob(id string)
}

// jobActionContext implements plugin.ActionContext backed by the generic jobs.phase column.
type jobActionContext struct {
	pool  *pgxpool.Pool
	jobID string
}

func (c *jobActionContext) JobID() string { return c.jobID }

func (c *jobActionContext) SetPhase(ctx context.Context, phase string) error {
	if c.jobID == "" {
		return nil
	}
	return store.SetJobPhase(ctx, c.pool, c.jobID, phase)
}

func (c *jobActionContext) ClearPhase(ctx context.Context) error {
	if c.jobID == "" {
		return nil
	}
	return store.ClearJobPhase(ctx, c.pool, c.jobID)
}
