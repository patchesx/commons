package webhooks

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/pipelineutil"
	"commons/plugin"
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

// RunPipeline executes the webhook's filter chain and then its action list sequentially
// in a background goroutine. data is the initial map from the processor's Extract().
// If data already contains a "job_id" key (e.g. the Zoom processor pre-created the job),
// that job is used; otherwise a new pipeline job record is created.
// All filters are evaluated first — if any fails, the pipeline is skipped silently.
func RunPipeline(ctx context.Context, pool *pgxpool.Pool, encKey []byte, webhook store.Webhook, data map[string]any, reg CancelRegistry) {
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var jobID string

	// Use pre-created job if the processor already set job_id in the data map.
	if id, ok := data["job_id"].(string); ok && id != "" {
		jobID = id
	} else {
		job := &store.Job{
			Type:    store.JobTypePipeline,
			Feature: "webhook_pipeline",
			Trigger: store.JobTriggerWebhook,
			Status:  store.JobStatusPending,
		}
		if err := store.CreateJob(jobCtx, pool, job); err != nil {
			log.Printf("webhooks/pipeline: webhook=%s create job: %v", webhook.Slug, err)
			return
		}
		jobID = job.ID
	}

	// Inject reserved keys into the data map before filters and actions.
	data["job_id"] = jobID
	data["webhook_id"] = webhook.ID
	data["webhook_slug"] = webhook.Slug

	if reg != nil {
		reg.RegisterJob(jobID, cancel)
		defer reg.UnregisterJob(jobID)
	}

	if err := store.UpdateJobStatus(jobCtx, pool, jobID, store.JobStatusRunning, nil); err != nil {
		log.Printf("webhooks/pipeline: job=%s status running: %v", jobID, err)
	}

	// Partition actions by run_on.
	var successActions, filterFailActions, actionFailActions []store.WebhookAction
	for _, a := range webhook.Actions {
		switch a.RunOn {
		case "filter_fail":
			filterFailActions = append(filterFailActions, a)
		case "action_fail":
			actionFailActions = append(actionFailActions, a)
		default:
			successActions = append(successActions, a)
		}
	}

	// Evaluate filters — all must pass for the pipeline to run.
	for _, f := range webhook.Filters {
		if !evaluateFilter(jobCtx, pool, encKey, data, f) {
			log.Printf("webhooks/pipeline: job=%s webhook=%s filter field=%s op=%s: condition not met — skipping pipeline",
				jobID, webhook.Slug, f.Field, f.Operator)
			runFailureActions(jobCtx, pool, jobID, filterFailActions, data)
			if err := store.CompleteJob(jobCtx, pool, jobID); err != nil {
				log.Printf("webhooks/pipeline: job=%s CompleteJob (filtered): %v", jobID, err)
			}
			return
		}
	}

	for _, action := range successActions {
		at, ok := plugin.GetActionType(action.Type)
		if !ok {
			log.Printf("webhooks/pipeline: job=%s action=%s type=%q not registered — skipping", jobID, action.ID, action.Type)
			continue
		}

		var params map[string]any
		if _, hasVariants := action.Params["message_variants"].([]any); hasVariants {
			cursor, claimErr := store.ClaimActionVariantCursor(jobCtx, pool, action.ID)
			if claimErr != nil {
				log.Printf("webhooks/pipeline: job=%s action=%s claim variant cursor: %v", jobID, action.ID, claimErr)
				cursor = action.VariantCursor
			}
			params = pipelineutil.ApplyVariant(action.Params, cursor)
		} else {
			params = action.Params
		}
		resolved := pipelineutil.ResolveActionParams(params, data)

		log.Printf("webhooks/pipeline: job=%s action=%s type=%s executing", jobID, action.ID, action.Type)
		ac := &jobActionContext{pool: pool, jobID: jobID}
		output, err := at.Execute(jobCtx, resolved, ac)
		if err != nil {
			log.Printf("webhooks/pipeline: job=%s action=%s type=%s failed: %v", jobID, action.ID, action.Type, err)
			data["error_message"] = err.Error()
			data["failed_action_type"] = action.Type
			runFailureActions(jobCtx, pool, jobID, actionFailActions, data)
			if dbErr := store.FailJob(jobCtx, pool, jobID, err.Error()); dbErr != nil {
				log.Printf("webhooks/pipeline: job=%s FailJob: %v", jobID, dbErr)
			}
			return
		}

		for k, v := range output {
			data[k] = v
		}
		log.Printf("webhooks/pipeline: job=%s action=%s type=%s ok", jobID, action.ID, action.Type)
	}

	if err := store.CompleteJob(jobCtx, pool, jobID); err != nil {
		log.Printf("webhooks/pipeline: job=%s CompleteJob: %v", jobID, err)
	}
}

// runFailureActions executes a list of failure actions (filter_fail or action_fail) in
// best-effort mode: errors are logged and execution continues to the next action.
// The job outcome is not affected by failure action errors.
func runFailureActions(ctx context.Context, pool *pgxpool.Pool, jobID string, actions []store.WebhookAction, data map[string]any) {
	for _, action := range actions {
		at, ok := plugin.GetActionType(action.Type)
		if !ok {
			log.Printf("webhooks/pipeline: job=%s failure_action=%s type=%q not registered — skipping", jobID, action.ID, action.Type)
			continue
		}
		resolved := pipelineutil.ResolveActionParams(action.Params, data)
		ac := &jobActionContext{pool: pool, jobID: jobID}
		if _, err := at.Execute(ctx, resolved, ac); err != nil {
			log.Printf("webhooks/pipeline: job=%s failure_action=%s type=%s failed (best-effort): %v", jobID, action.ID, action.Type, err)
		}
	}
}

// evaluateFilter returns true if the data map satisfies the filter condition.
// On any resolution error (config lookup failure, type mismatch) it fails open (returns true)
// so pipeline runs aren't silently dropped due to infrastructure issues.
func evaluateFilter(ctx context.Context, pool *pgxpool.Pool, encKey []byte, data map[string]any, f store.WebhookFilter) bool {
	val, valExists := data[f.Field]
	fieldPresent := valExists && val != nil

	// exists/not_exists don't need a comparison value.
	switch f.Operator {
	case "exists":
		return fieldPresent
	case "not_exists":
		return !fieldPresent
	}

	// Resolve comparison value from config_store or literal.
	var compareStr string
	if f.ConfigRef != nil && *f.ConfigRef != "" {
		parts := strings.SplitN(*f.ConfigRef, ".", 2)
		if len(parts) != 2 {
			log.Printf("webhooks/filter: invalid config_ref %q — skipping filter (pass)", *f.ConfigRef)
			return true
		}
		var err error
		compareStr, err = store.GetServiceConfig(ctx, pool, parts[0], parts[1], encKey)
		if err != nil {
			log.Printf("webhooks/filter: resolve config_ref %q: %v — skipping filter (pass)", *f.ConfigRef, err)
			return true
		}
	} else if f.Value != nil {
		compareStr = *f.Value
	} else {
		return true // no value to compare against
	}

	if !fieldPresent {
		return false // field missing — comparison fails
	}

	// Numeric operators.
	switch f.Operator {
	case "gt", "gte", "lt", "lte":
		dataNum, err := toFloat64(val)
		if err != nil {
			log.Printf("webhooks/filter: field %q value %v not numeric for operator %s — failing filter", f.Field, val, f.Operator)
			return false
		}
		cmpNum, err := strconv.ParseFloat(compareStr, 64)
		if err != nil {
			log.Printf("webhooks/filter: compare value %q not numeric for operator %s — failing filter", compareStr, f.Operator)
			return false
		}
		if f.ValueScale != 0 && f.ValueScale != 1 {
			cmpNum *= f.ValueScale
		}
		switch f.Operator {
		case "gt":
			return dataNum > cmpNum
		case "gte":
			return dataNum >= cmpNum
		case "lt":
			return dataNum < cmpNum
		case "lte":
			return dataNum <= cmpNum
		}
	}

	// String/boolean operators — coerce data value to string.
	dataStr := fmt.Sprintf("%v", val)
	switch f.Operator {
	case "eq":
		return dataStr == compareStr
	case "neq":
		return dataStr != compareStr
	case "contains":
		return strings.Contains(dataStr, compareStr)
	case "not_contains":
		return !strings.Contains(dataStr, compareStr)
	}

	log.Printf("webhooks/filter: unknown operator %q — skipping filter (pass)", f.Operator)
	return true
}

func toFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case string:
		return strconv.ParseFloat(n, 64)
	}
	return 0, fmt.Errorf("cannot convert %T to float64", v)
}
