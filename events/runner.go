// Package events implements the EventDispatcher that fires event pipelines
// for internal trigger types (Slack events, scheduler callbacks, etc.).
// Pipeline execution is delegated to the unified pipeline.RunPipeline.
package events

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/pipeline"
	"commons/plugin"
	"commons/store"
)

// Runner implements plugin.EventDispatcher.
type Runner struct {
	pool   *pgxpool.Pool
	encKey []byte
}

// NewRunner creates an EventDispatcher backed by the given DB pool.
func NewRunner(pool *pgxpool.Pool, encKey []byte) *Runner {
	return &Runner{pool: pool, encKey: encKey}
}

// Fire dispatches triggerID to all enabled trigger_sources of that type.
// For FireOnce triggers, each (pipeline, entityID) pair is executed at most once.
// Each pipeline runs in its own goroutine; actions within a pipeline are sequential.
// Event pipelines do not create job records by default (to avoid flooding the jobs
// table with high-frequency triggers like per-member sync events).
func (r *Runner) Fire(ctx context.Context, triggerID, entityID string, data map[string]any) error {
	tt, ok := plugin.GetTriggerType(triggerID)
	if !ok {
		return nil
	}

	pipelines, err := store.ListTriggerSourcesByType(ctx, r.pool, triggerID)
	if err != nil {
		log.Printf("events: list pipelines for trigger %q: %v", triggerID, err)
		return err
	}

	for _, p := range pipelines {
		p := p
		go func() {
			bgCtx := context.Background()
			if tt.FireOnce() && entityID != "" {
				fired, err := store.TryRecordTriggerFire(bgCtx, r.pool, p.ID, entityID)
				if err != nil {
					log.Printf("events: record fire trigger=%s entity=%s: %v", p.ID, entityID, err)
					return
				}
				if !fired {
					return
				}
			}
			// Event pipelines: createJob=false (no job record), reg=nil (no cancellation).
			pipeline.RunPipeline(bgCtx, r.pool, r.encKey, p, data, nil, false)
		}()
	}
	return nil
}
