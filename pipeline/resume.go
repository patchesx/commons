package pipeline

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// ResumeScheduler polls for paused pipeline_runs whose resume_at has passed
// and resumes execution. Runs every 15 seconds. The goroutine runs until ctx
// is cancelled.
func ResumeScheduler(ctx context.Context, pool *pgxpool.Pool, encKey []byte, reg CancelRegistry) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resumeDueRuns(ctx, pool, encKey, reg)
	}
}
}

// ExecuteRun executes a pipeline run. Used for manual re-run from the UI.
// The run should already be created (e.g. via ClonePipelineRun) and in 'running' status.
func ExecuteRun(ctx context.Context, pool *pgxpool.Pool, encKey []byte, run *store.PipelineRun, reg CancelRegistry) error {
	source, err := store.GetTriggerSourceByID(ctx, pool, run.TriggerID)
	if err != nil {
		return err
	}
	go executeRun(context.Background(), pool, encKey, *source, run, reg, run.JobID)
	return nil
}

// resumeDueRuns finds paused runs with resume_at <= NOW(), claims each atomically,
// and resumes execution in a new goroutine.
func resumeDueRuns(ctx context.Context, pool *pgxpool.Pool, encKey []byte, reg CancelRegistry) {
	runs, err := store.ListDuePipelineRuns(ctx, pool)
	if err != nil {
		log.Printf("pipeline/resume: list due runs: %v", err)
		return
	}

	for _, run := range runs {
		run := run
		claimed, err := store.ClaimPipelineRun(ctx, pool, run.ID)
		if err != nil {
			log.Printf("pipeline/resume: claim run %s: %v", run.ID, err)
			continue
		}
		if !claimed {
			continue // another worker got it
		}

		go func() {
			bgCtx := context.Background()
			source, err := store.GetTriggerSourceByID(bgCtx, pool, run.TriggerID)
			if err != nil {
				log.Printf("pipeline/resume: run=%s load source: %v", run.ID, err)
				store.FailPipelineRun(bgCtx, pool, run.ID, "trigger source not found")
				return
			}
			log.Printf("pipeline/resume: run=%s resuming from step %d", run.ID, run.CurrentStep)
			executeRun(bgCtx, pool, encKey, *source, &run, reg, run.JobID)
		}()
	}
}

// ResumeInterruptedRuns finds pipeline runs left in 'running' or 'paused' state
// (from a server crash or restart) and resumes them. Called once on startup.
// - Paused runs with resume_at <= NOW() → resume immediately
// - Paused runs with resume_at > NOW() → leave for the resume scheduler
// - Running runs (crashed mid-action) → resume from current_step
func ResumeInterruptedRuns(ctx context.Context, pool *pgxpool.Pool, encKey []byte, reg CancelRegistry) {	runs, err := store.ListInterruptedRuns(ctx, pool)
	if err != nil {
		log.Printf("pipeline/resume: list interrupted runs: %v", err)
		return
	}

	now := time.Now()
	for _, run := range runs {
		run := run

		// Skip paused runs that aren't due yet — the resume scheduler will pick them up.
		if run.Status == "paused" && run.ResumeAt != nil && run.ResumeAt.After(now) {
			log.Printf("pipeline/resume: run=%s paused until %s — leaving for scheduler", run.ID, run.ResumeAt)
			continue
		}

		// Claim the run (handles both paused and running states).
		// For 'running' runs (crashed mid-action), we need to reset to 'running'
		// since they're already in that state. ClaimPipelineRun only works for
		// 'paused' runs, so for 'running' runs we just proceed.
		if run.Status == "paused" {
			claimed, err := store.ClaimPipelineRun(ctx, pool, run.ID)
			if err != nil || !claimed {
				log.Printf("pipeline/resume: run=%s claim failed: %v", run.ID, err)
				continue
			}
		}

		go func() {
			bgCtx := context.Background()
			source, err := store.GetTriggerSourceByID(bgCtx, pool, run.TriggerID)
			if err != nil {
				log.Printf("pipeline/resume: run=%s load source: %v", run.ID, err)
				store.FailPipelineRun(bgCtx, pool, run.ID, "trigger source not found")
				return
			}
			log.Printf("pipeline/resume: run=%s resuming from step %d (was %s)", run.ID, run.CurrentStep, run.Status)
			executeRun(bgCtx, pool, encKey, *source, &run, reg, run.JobID)
		}()
	}
}
