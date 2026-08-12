-- Durable pipeline execution state.
-- Replaces the implicit "goroutine + in-memory data map" with persistent records
-- that survive server restarts. Required for delays (core.delay).

-- A single execution of a pipeline (trigger source).
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_id    UUID        NOT NULL REFERENCES trigger_sources(id) ON DELETE CASCADE,
    job_id        UUID        REFERENCES jobs(id) ON DELETE SET NULL,
    status        TEXT        NOT NULL DEFAULT 'running',  -- running, paused, complete, failed, cancelled
    data_map      JSONB       NOT NULL DEFAULT '{}',
    current_step  INT         NOT NULL DEFAULT 0,
    resume_at     TIMESTAMPTZ,
    error_message TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_resume ON pipeline_runs (resume_at)
    WHERE status = 'paused';
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_status ON pipeline_runs (status);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_trigger ON pipeline_runs (trigger_id);

-- Per-step execution log for a pipeline run.
CREATE TABLE IF NOT EXISTS pipeline_run_steps (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id        UUID        NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    action_id     UUID        REFERENCES pipeline_actions(id) ON DELETE SET NULL,
    position      INT         NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'pending',  -- pending, running, complete, failed, skipped
    input_params  JSONB,
    output_data   JSONB,
    error_message TEXT,
    started_at    TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pipeline_run_steps_run ON pipeline_run_steps (run_id, position);
