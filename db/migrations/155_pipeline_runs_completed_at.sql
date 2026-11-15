-- 143_pipeline_runs.sql gained completed_at (and reordered pipeline_run_steps) after some databases had
-- already applied it, so those databases lack the column and every CompletePipelineRun UPDATE failed,
-- leaving finished runs 'running' to be resumed on each startup. Idempotent for databases that have it.
ALTER TABLE pipeline_runs ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE pipeline_run_steps ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE pipeline_run_steps ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
ALTER TABLE pipeline_run_steps ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

-- Runs that finished but could not be marked complete are still 'running'. Only runs that have executed
-- every action (current_step past the last one) are safe to close, and that needs the action count, so
-- they are left for ResumeInterruptedRuns to complete once on the next startup.
