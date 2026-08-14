package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PipelineRun is a single execution of a pipeline (trigger source).
type PipelineRun struct {
	ID           string
	TriggerID    string
	JobID        string
	Status       string // running, paused, complete, failed, cancelled
	DataMap      map[string]any
	CurrentStep  int
	ResumeAt     *time.Time
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PipelineRunStep is a per-step execution log entry for a pipeline run.
type PipelineRunStep struct {
	ID           string
	RunID        string
	ActionID     *string
	Position     int
	Status       string // pending, running, complete, failed, skipped
	InputParams  map[string]any
	OutputData   map[string]any
	ErrorMessage *string
	StartedAt    *time.Time
	CompletedAt  *time.Time
	CreatedAt    time.Time
}

// CreatePipelineRun inserts a new pipeline_run record and populates run.ID and run.CreatedAt.
func CreatePipelineRun(ctx context.Context, pool *pgxpool.Pool, run *PipelineRun) error {
	rawData, _ := json.Marshal(run.DataMap)
	var jobID *string
	if run.JobID != "" {
		jobID = &run.JobID
	}
	return pool.QueryRow(ctx, `
		INSERT INTO pipeline_runs (trigger_id, job_id, status, data_map, current_step)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		run.TriggerID, jobID, run.Status, rawData, run.CurrentStep).Scan(&run.ID, &run.CreatedAt)
}

// UpdatePipelineRunPosition persists the current step index and data map.
// Called before each action executes so crash recovery can resume from the right point.
func UpdatePipelineRunPosition(ctx context.Context, pool *pgxpool.Pool, id string, step int, dataMap map[string]any) error {
	rawData, _ := json.Marshal(dataMap)
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_runs SET current_step = $2, data_map = $3, updated_at = NOW() WHERE id = $1`,
		id, step, rawData)
	return err
}

// PausePipelineRun sets the run to 'paused' with a resume_at time and persists the data map.
func PausePipelineRun(ctx context.Context, pool *pgxpool.Pool, id string, resumeAt time.Time, dataMap map[string]any, nextStep int) error {
	rawData, _ := json.Marshal(dataMap)
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_runs SET status = 'paused', resume_at = $2, data_map = $3, current_step = $4, updated_at = NOW() WHERE id = $1`,
		id, resumeAt, rawData, nextStep)
	return err
}

// CompletePipelineRun marks the run as complete.
func CompletePipelineRun(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_runs SET status = 'complete', resume_at = NULL, completed_at = NOW(), updated_at = NOW() WHERE id = $1`,
		id)
	return err
}

// FailPipelineRun marks the run as failed with an error message.
func FailPipelineRun(ctx context.Context, pool *pgxpool.Pool, id string, errMsg string) error {
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_runs SET status = 'failed', resume_at = NULL, error_message = $2, updated_at = NOW() WHERE id = $1`,
		id, errMsg)
	return err
}

// DeadLetterPipelineRun marks the run as 'dead_letter' — failed after exhausting
// retries. Dead-letter runs don't auto-resume on restart; admins must re-run them.
func DeadLetterPipelineRun(ctx context.Context, pool *pgxpool.Pool, id string, errMsg string) error {
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_runs SET status = 'dead_letter', resume_at = NULL, error_message = $2, updated_at = NOW() WHERE id = $1`,
		id, errMsg)
	return err
}

// ClonePipelineRun creates a new pipeline_run from an existing one, optionally
// starting from a specific step. Used for manual re-run from the UI.
// If fromStep is 0, re-runs from the beginning. If fromStep > 0, resumes from
// that step with the original run's persisted data map.
func ClonePipelineRun(ctx context.Context, pool *pgxpool.Pool, originalID string, fromStep int) (*PipelineRun, error) {
	original, err := GetPipelineRunByID(ctx, pool, originalID)
	if err != nil {
		return nil, err
	}

	step := fromStep
	if step < 0 || step > original.CurrentStep {
		step = 0 // default to beginning if invalid
	}

	run := &PipelineRun{
		TriggerID:   original.TriggerID,
		JobID:       original.JobID,
		DataMap:     original.DataMap,
		Status:      "running",
		CurrentStep: step,
	}
	if err := CreatePipelineRun(ctx, pool, run); err != nil {
		return nil, err
	}
	return run, nil
}

// ClaimPipelineRun atomically claims a paused run for resumption.
// Sets status to 'running' and clears resume_at. Returns false if the run was
// already claimed by another worker or is no longer paused.
func ClaimPipelineRun(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	tag, err := pool.Exec(ctx,
		`UPDATE pipeline_runs SET status = 'running', resume_at = NULL, updated_at = NOW()
		 WHERE id = $1 AND status = 'paused'`,
		id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListDuePipelineRuns returns paused runs whose resume_at has passed.
func ListDuePipelineRuns(ctx context.Context, pool *pgxpool.Pool) ([]PipelineRun, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, trigger_id, COALESCE(job_id::text,''), status, data_map, current_step, resume_at, error_message, created_at, updated_at
		 FROM pipeline_runs
		 WHERE status = 'paused' AND resume_at <= NOW()
		 ORDER BY resume_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPipelineRuns(rows)
}

// ListInterruptedRuns returns runs in 'running' or 'paused' state (for restart recovery).
func ListInterruptedRuns(ctx context.Context, pool *pgxpool.Pool) ([]PipelineRun, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, trigger_id, COALESCE(job_id::text,''), status, data_map, current_step, resume_at, error_message, created_at, updated_at
		 FROM pipeline_runs
		 WHERE status IN ('running', 'paused')
		 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPipelineRuns(rows)
}

// GetPipelineRunByID returns a single pipeline run by ID.
func GetPipelineRunByID(ctx context.Context, pool *pgxpool.Pool, id string) (*PipelineRun, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, trigger_id, COALESCE(job_id::text,''), status, data_map, current_step, resume_at, error_message, created_at, updated_at
		 FROM pipeline_runs WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs, err := scanPipelineRuns(rows)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, ErrNotFound
	}
	return &runs[0], nil
}

func scanPipelineRuns(rows pgx.Rows) ([]PipelineRun, error) {
	var out []PipelineRun
	for rows.Next() {
		var r PipelineRun
		var rawData []byte
		if err := rows.Scan(&r.ID, &r.TriggerID, &r.JobID, &r.Status, &rawData, &r.CurrentStep, &r.ResumeAt, &r.ErrorMessage, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if len(rawData) > 0 {
			json.Unmarshal(rawData, &r.DataMap) //nolint:errcheck
		}
		if r.DataMap == nil {
			r.DataMap = map[string]any{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreatePipelineRunStep inserts a new step record and returns the created step.
func CreatePipelineRunStep(ctx context.Context, pool *pgxpool.Pool, step *PipelineRunStep) error {
	var actionID *string
	if step.ActionID != nil {
		actionID = step.ActionID
	}
	var rawInput []byte
	if step.InputParams != nil {
		rawInput, _ = json.Marshal(step.InputParams)
	}
	return pool.QueryRow(ctx, `
		INSERT INTO pipeline_run_steps (run_id, action_id, position, status, input_params, started_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		RETURNING id, created_at`,
		step.RunID, actionID, step.Position, step.Status, rawInput).Scan(&step.ID, &step.CreatedAt)
}

// CompletePipelineRunStep marks a step as complete with its output data.
func CompletePipelineRunStep(ctx context.Context, pool *pgxpool.Pool, id string, output map[string]any) error {
	var rawOutput []byte
	if output != nil {
		rawOutput, _ = json.Marshal(output)
	}
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_run_steps SET status = 'complete', output_data = $2, completed_at = NOW() WHERE id = $1`,
		id, rawOutput)
	return err
}

// FailPipelineRunStep marks a step as failed with an error message.
func FailPipelineRunStep(ctx context.Context, pool *pgxpool.Pool, id string, errMsg string) error {
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_run_steps SET status = 'failed', error_message = $2, completed_at = NOW() WHERE id = $1`,
		id, errMsg)
	return err
}

// SkipPipelineRunStep marks a step as skipped (condition not met).
func SkipPipelineRunStep(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx,
		`UPDATE pipeline_run_steps SET status = 'skipped', completed_at = NOW() WHERE id = $1`,
		id)
	return err
}
