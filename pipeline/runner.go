package pipeline

import (
	"context"
	"errors"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/pipelineutil"
	"commons/plugin"
	"commons/store"
)

// RunPipeline creates a pipeline_run record and begins execution.
// Used by all three trigger types: HTTP webhooks, internal events, and scheduled triggers.
//
// If createJob is true, a job record is created and tracked (status, phase, cancellation).
// If createJob is false, the pipeline runs without a job record (used for high-frequency
// event triggers to avoid flooding the jobs table).
//
// For delayed resumption (core.delay), the run is persisted as 'paused' and the
// resume scheduler picks it up when resume_at passes.
func RunPipeline(ctx context.Context, pool *pgxpool.Pool, encKey []byte,
	source store.TriggerSource, data map[string]any, reg CancelRegistry, createJob bool) {

	data = cloneData(data)

	var jobID string
	if createJob {
		job := &store.Job{
			Type:    store.JobTypePipeline,
			Feature: jobFeatureForSource(source),
			Trigger: jobTriggerForSource(source),
			Status:  store.JobStatusPending,
		}
		if err := store.CreateJob(ctx, pool, job); err != nil {
			log.Printf("pipeline: source=%s create job: %v", source.ID, err)
			return
		}
		jobID = job.ID
		data["job_id"] = jobID
	}

	data["trigger_id"] = source.ID
	data["trigger_name"] = source.Name

	run := &store.PipelineRun{
		TriggerID: source.ID,
		JobID:     jobID,
		DataMap:   data,
		Status:    "running",
	}
	if err := store.CreatePipelineRun(ctx, pool, run); err != nil {
		log.Printf("pipeline: source=%s create run: %v", source.ID, err)
		if jobID != "" {
			store.FailJob(ctx, pool, jobID, "failed to create pipeline run")
		}
		return
	}

	executeRun(ctx, pool, encKey, source, run, reg, jobID)
}

// executeRun processes actions starting at run.CurrentStep.
// Stops when: all actions complete, an action fails, or an action pauses (delay).
// On initial run (current_step == 0), evaluates filters first.
func executeRun(ctx context.Context, pool *pgxpool.Pool, encKey []byte,
	source store.TriggerSource, run *store.PipelineRun, reg CancelRegistry, jobID string) {

	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if reg != nil && jobID != "" {
		reg.RegisterJob(jobID, cancel)
		defer reg.UnregisterJob(jobID)
	}

	if jobID != "" {
		store.UpdateJobStatus(jobCtx, pool, jobID, store.JobStatusRunning, nil)
	}

	// Evaluate filters only on the initial run (current_step == 0).
	if run.CurrentStep == 0 {
		filters, err := store.ListWebhookFilters(jobCtx, pool, source.ID)
		if err != nil {
			log.Printf("pipeline: run=%s load filters: %v", run.ID, err)
		}
		filterFailed := false
		for _, f := range filters {
			if !EvaluateFilter(jobCtx, pool, encKey, run.DataMap, f) {
				log.Printf("pipeline: run=%s filter field=%s op=%s: condition not met — skipping pipeline",
					run.ID, f.Field, f.Operator)
				filterFailed = true
				break
			}
		}
		if filterFailed {
			filterFailActions, _ := store.ListPipelineActions(jobCtx, pool, source.ID, "filter_fail")
			runFailureActions(jobCtx, pool, jobID, filterFailActions, run.DataMap)
			store.CompletePipelineRun(jobCtx, pool, run.ID)
			if jobID != "" {
				store.CompleteJob(jobCtx, pool, jobID)
			}
			return
		}
	}

	// Execute success actions starting from current_step.
	successActions, err := store.ListPipelineActions(jobCtx, pool, source.ID, "success")
	if err != nil {
		log.Printf("pipeline: run=%s load success actions: %v", run.ID, err)
		store.FailPipelineRun(jobCtx, pool, run.ID, "failed to load actions")
		if jobID != "" {
			store.FailJob(jobCtx, pool, jobID, "failed to load actions")
		}
		return
	}

	for i := run.CurrentStep; i < len(successActions); i++ {
		action := successActions[i]

		// Check per-action condition — skip if not met.
		if action.Condition != nil && !EvaluateCondition(run.DataMap, *action.Condition) {
			log.Printf("pipeline: run=%s action=%s condition not met — skipping", run.ID, action.ID)
			step := &store.PipelineRunStep{
				RunID:    run.ID,
				ActionID: &action.ID,
				Position: i,
				Status:   "skipped",
			}
			store.CreatePipelineRunStep(jobCtx, pool, step)
			store.SkipPipelineRunStep(jobCtx, pool, step.ID)
			continue
		}

		// Persist position + data map before executing (for crash recovery).
		store.UpdatePipelineRunPosition(jobCtx, pool, run.ID, i, run.DataMap)

		// Create step record.
		step := &store.PipelineRunStep{
			RunID:    run.ID,
			ActionID: &action.ID,
			Position: i,
			Status:   "running",
		}
		store.CreatePipelineRunStep(jobCtx, pool, step)

		// Resolve params and execute.
		at, ok := plugin.GetActionType(action.Type)
		if !ok {
			log.Printf("pipeline: run=%s action=%s type=%q not registered — skipping", run.ID, action.ID, action.Type)
			store.SkipPipelineRunStep(jobCtx, pool, step.ID)
			continue
		}

		var params map[string]any
		if _, hasVariants := action.Params["message_variants"].([]any); hasVariants {
			cursor, claimErr := store.ClaimActionVariantCursor(jobCtx, pool, action.ID)
			if claimErr != nil {
				log.Printf("pipeline: run=%s action=%s claim variant cursor: %v", run.ID, action.ID, claimErr)
				cursor = action.VariantCursor
			}
			params = pipelineutil.ApplyVariant(action.Params, cursor)
		} else {
			params = action.Params
		}
		resolved := pipelineutil.ResolveActionParams(params, run.DataMap)

		log.Printf("pipeline: run=%s action=%s type=%s executing", run.ID, action.ID, action.Type)
		ac := &jobActionContext{pool: pool, jobID: jobID}
		output, err := executeWithRetry(jobCtx, at, resolved, ac, action.RetryConfig, action.TimeoutSeconds)

		if err != nil {
			// Check if this is a pause signal (delay action).
			var pause plugin.PauseSignal
			if errors.As(err, &pause) {
				store.PausePipelineRun(jobCtx, pool, run.ID, pause.ResumeAt, run.DataMap, i+1)
				log.Printf("pipeline: run=%s paused until %s (action=%s)", run.ID, pause.ResumeAt, action.ID)
				return // resume scheduler will pick it up
			}

			// Real error — record failure, run action_fail actions.
			log.Printf("pipeline: run=%s action=%s type=%s failed: %v", run.ID, action.ID, action.Type, err)
			store.FailPipelineRunStep(jobCtx, pool, step.ID, err.Error())
			run.DataMap["error_message"] = err.Error()
			run.DataMap["failed_action_type"] = action.Type
			actionFailActions, _ := store.ListPipelineActions(jobCtx, pool, source.ID, "action_fail")
			runFailureActions(jobCtx, pool, jobID, actionFailActions, run.DataMap)

			// Dead-letter if retries were exhausted; otherwise just fail.
			if action.RetryConfig != nil && action.RetryConfig.MaxAttempts > 1 {
				store.DeadLetterPipelineRun(jobCtx, pool, run.ID, err.Error())
			} else {
				store.FailPipelineRun(jobCtx, pool, run.ID, err.Error())
			}
			if jobID != "" {
				store.FailJob(jobCtx, pool, jobID, err.Error())
			}
			return
		}

		// Merge output into data map.
		for k, v := range output {
			run.DataMap[k] = v
		}
		store.UpdatePipelineRunPosition(jobCtx, pool, run.ID, i+1, run.DataMap)

		// Record step output.
		store.CompletePipelineRunStep(jobCtx, pool, step.ID, output)
		log.Printf("pipeline: run=%s action=%s type=%s ok", run.ID, action.ID, action.Type)
	}

	// All actions complete.
	store.CompletePipelineRun(jobCtx, pool, run.ID)
	if jobID != "" {
		store.CompleteJob(jobCtx, pool, jobID)
	}
	log.Printf("pipeline: run=%s complete", run.ID)
}

// runFailureActions executes failure actions (filter_fail or action_fail) in
// best-effort mode: errors are logged and execution continues to the next action.
func runFailureActions(ctx context.Context, pool *pgxpool.Pool, jobID string, actions []store.WebhookAction, data map[string]any) {
	for _, action := range actions {
		at, ok := plugin.GetActionType(action.Type)
		if !ok {
			log.Printf("pipeline: job=%s failure_action=%s type=%q not registered — skipping", jobID, action.ID, action.Type)
			continue
		}
		resolved := pipelineutil.ResolveActionParams(action.Params, data)
		ac := &jobActionContext{pool: pool, jobID: jobID}
		if _, err := at.Execute(ctx, resolved, ac); err != nil {
			log.Printf("pipeline: job=%s failure_action=%s type=%s failed (best-effort): %v", jobID, action.ID, action.Type, err)
		}
	}
}

// jobFeatureForSource returns the job feature label for the given trigger source type.
func jobFeatureForSource(source store.TriggerSource) string {
	switch source.Type {
	case "http.webhook":
		return store.JobFeatureWebhookPipeline
	case "scheduled":
		return store.JobFeatureScheduledPipeline
	default:
		return store.JobFeatureEventPipeline
	}
}

// jobTriggerForSource returns the job trigger source label for the given trigger source type.
func jobTriggerForSource(source store.TriggerSource) string {
	switch source.Type {
	case "http.webhook":
		return store.JobTriggerWebhook
	case "scheduled":
		return store.JobTriggerScheduled
	default:
		return store.JobTriggerEvent
	}
}

func cloneData(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
