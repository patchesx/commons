// Package events implements the EventDispatcher that fires event pipelines
// for internal trigger types (Slack events, scheduler callbacks, etc.).
// HTTP webhook pipelines continue to run through webhooks/pipeline.go.
package events

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/pipelineutil"
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
			r.runPipeline(bgCtx, p, data)
		}()
	}
	return nil
}

func (r *Runner) runPipeline(ctx context.Context, pipeline store.TriggerSource, data map[string]any) {
	pipelineData := cloneData(data)

	successActions, err := store.ListPipelineActions(ctx, r.pool, pipeline.ID, "success")
	if err != nil {
		log.Printf("events: load actions pipeline=%s: %v", pipeline.ID, err)
		return
	}

	for _, action := range successActions {
		at, ok := plugin.GetActionType(action.Type)
		if !ok {
			log.Printf("events: pipeline=%s action=%s type=%q not registered — skipping", pipeline.ID, action.ID, action.Type)
			continue
		}

		var params map[string]any
		if _, hasVariants := action.Params["message_variants"].([]any); hasVariants {
			cursor, claimErr := store.ClaimActionVariantCursor(ctx, r.pool, action.ID)
			if claimErr != nil {
				log.Printf("events: pipeline=%s action=%s claim variant cursor: %v", pipeline.ID, action.ID, claimErr)
				cursor = action.VariantCursor
			}
			params = pipelineutil.ApplyVariant(action.Params, cursor)
		} else {
			params = action.Params
		}
		resolved := pipelineutil.ResolveActionParams(params, pipelineData)
		output, err := at.Execute(ctx, resolved, plugin.NoopActionContext{})
		if err != nil {
			log.Printf("events: pipeline=%s action=%s type=%s failed: %v", pipeline.ID, action.ID, action.Type, err)
			pipelineData["error_message"] = err.Error()
			pipelineData["failed_action_type"] = action.Type
			r.runFailureActions(ctx, pipeline.ID, pipelineData)
			return
		}

		for k, v := range output {
			pipelineData[k] = v
		}
	}
}

func (r *Runner) runFailureActions(ctx context.Context, pipelineID string, data map[string]any) {
	failActions, err := store.ListPipelineActions(ctx, r.pool, pipelineID, "action_fail")
	if err != nil {
		log.Printf("events: load failure actions pipeline=%s: %v", pipelineID, err)
		return
	}
	for _, action := range failActions {
		at, ok := plugin.GetActionType(action.Type)
		if !ok {
			continue
		}
		resolved := pipelineutil.ResolveActionParams(action.Params, data)
		if _, err := at.Execute(ctx, resolved, plugin.NoopActionContext{}); err != nil {
			log.Printf("events: pipeline=%s failure_action=%s type=%s failed (best-effort): %v", pipelineID, action.ID, action.Type, err)
		}
	}
}

func cloneData(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
