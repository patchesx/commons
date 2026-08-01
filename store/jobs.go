package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job type discriminators.
const (
	JobTypeRecordingUpload = "recording_upload"
	JobTypeMemberSync      = "member_sync"
	JobTypeOpenStatesSync  = "openstates_sync"
	JobTypeLegistarSync    = "legistar_sync"
	JobTypeMeetingSync     = "zoom_meeting_sync"
	JobTypePipeline        = "pipeline"
)

// Job feature labels.
const (
	JobFeatureRecordingPipeline = "recording_pipeline"
	JobFeatureMemberPortal      = "member_portal"
	JobFeatureLegislationSync   = "legislation_sync"
	JobFeatureMeetingScheduling = "meeting_scheduling"
)

// Job trigger sources.
const (
	JobTriggerWebhook   = "webhook"
	JobTriggerScheduled = "scheduled"
	JobTriggerManual    = "manual"
)

// JobStatus values — 5-state generic lifecycle.
// Integration-specific in-progress phases live in per-integration extension tables.
const (
	JobStatusPending   = "pending"
	JobStatusRunning   = "running"
	JobStatusComplete  = "complete"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"
)

// Job is the generic pipeline record. Integration-specific data is stored in
// per-integration extension tables (zoom.recording_data, youtube.upload_data, etc.)
// and fetched separately by the integration packages that own them.
type Job struct {
	ID           string
	Type         string // JobType* constant
	Feature      string // JobFeature* constant
	Status       string // JobStatus* constant
	Trigger      string // JobTrigger* constant
	ErrorMessage *string
	StartedAt    time.Time
	CompletedAt  *time.Time
}

// JobFilter restricts ListJobsPaginated results. Zero values mean "no filter".
type JobFilter struct {
	Type    string
	Feature string
	Status  string
}

// CreateJob inserts a new job record and populates j.ID and j.StartedAt.
func CreateJob(ctx context.Context, pool *pgxpool.Pool, j *Job) error {
	return pool.QueryRow(ctx, `
		INSERT INTO jobs (type, feature, status, trigger)
		VALUES ($1, $2, $3, $4)
		RETURNING id, started_at
	`, j.Type, j.Feature, j.Status, j.Trigger).Scan(&j.ID, &j.StartedAt)
}

// UpdateJobStatus sets the job status and optionally an error message.
func UpdateJobStatus(ctx context.Context, pool *pgxpool.Pool, jobID, status string, errMsg *string) error {
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET status = $1, error_message = $2 WHERE id = $3
	`, status, errMsg, jobID)
	return err
}

// CompleteJob marks a job as complete and sets completed_at.
func CompleteJob(ctx context.Context, pool *pgxpool.Pool, jobID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'complete', completed_at = NOW(), error_message = NULL
		WHERE id = $1
	`, jobID)
	return err
}

// FailJob marks a job as failed with an error message.
// Does not overwrite jobs already in a terminal state (complete or cancelled).
func FailJob(ctx context.Context, pool *pgxpool.Pool, jobID, errMsg string) error {
	_, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', error_message = $1, completed_at = NOW()
		WHERE id = $2 AND status NOT IN ('complete', 'cancelled')
	`, errMsg, jobID)
	return err
}

// CancelJob marks a job as cancelled.
// Only updates if the job is currently pending or running — returns false if already terminal.
func CancelJob(ctx context.Context, pool *pgxpool.Pool, jobID string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', completed_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'running')
	`, jobID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// GetJobByID returns a job by its UUID. Returns ErrNotFound if absent.
func GetJobByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Job, error) {
	j := &Job{}
	err := scanJob(pool.QueryRow(ctx, `
		SELECT id, type, feature, status, trigger, error_message, started_at, completed_at
		FROM jobs WHERE id = $1
	`, id), j)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// ListJobsPaginated returns jobs with total count, newest first.
// Optional filter restricts by type, feature, and/or status.
func ListJobsPaginated(ctx context.Context, pool *pgxpool.Pool, limit, offset int, f JobFilter) ([]Job, int, error) {
	where, args := buildFilter(f)
	var total int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM jobs"+where, args[:countArgs(f)]...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := pool.Query(ctx, `
		SELECT id, type, feature, status, trigger, error_message, started_at, completed_at
		FROM jobs`+where+` ORDER BY started_at DESC LIMIT $`+argN(f, 1)+` OFFSET $`+argN(f, 2), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := scanJob(rows, &j); err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, j)
	}
	return jobs, total, rows.Err()
}

// ListJobsByStatus returns all jobs matching the given status, oldest first.
// Used by startup recovery to find jobs interrupted mid-flight.
func ListJobsByStatus(ctx context.Context, pool *pgxpool.Pool, status string) ([]Job, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, feature, status, trigger, error_message, started_at, completed_at
		FROM jobs WHERE status = $1 ORDER BY started_at ASC
	`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := scanJob(rows, &j); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ListJobsByType returns all jobs of a given type, newest first.
func ListJobsByType(ctx context.Context, pool *pgxpool.Pool, jobType string) ([]Job, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, feature, status, trigger, error_message, started_at, completed_at
		FROM jobs WHERE type = $1 ORDER BY started_at DESC
	`, jobType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := scanJob(rows, &j); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner, j *Job) error {
	return s.Scan(
		&j.ID, &j.Type, &j.Feature, &j.Status, &j.Trigger,
		&j.ErrorMessage, &j.StartedAt, &j.CompletedAt,
	)
}

// buildFilter constructs a WHERE clause and positional args from a JobFilter.
// Returns the clause string (empty or starting with " WHERE") and args slice.
// Count args are the filter args only; LIMIT/OFFSET are appended by the caller.
func buildFilter(f JobFilter) (string, []any) {
	var conditions []string
	var args []any
	n := 1
	if f.Type != "" {
		conditions = append(conditions, "type = $"+itoa(n))
		args = append(args, f.Type)
		n++
	}
	if f.Feature != "" {
		conditions = append(conditions, "feature = $"+itoa(n))
		args = append(args, f.Feature)
		n++
	}
	if f.Status != "" {
		conditions = append(conditions, "status = $"+itoa(n))
		args = append(args, f.Status)
		n++
	}
	if len(conditions) == 0 {
		return "", args
	}
	where := " WHERE "
	for i, c := range conditions {
		if i > 0 {
			where += " AND "
		}
		where += c
	}
	return where, args
}

// countArgs returns the number of filter args (used for the COUNT query).
func countArgs(f JobFilter) int {
	n := 0
	if f.Type != "" {
		n++
	}
	if f.Feature != "" {
		n++
	}
	if f.Status != "" {
		n++
	}
	return n
}

// argN returns the positional parameter number for LIMIT (offset=1) or OFFSET (offset=2).
func argN(f JobFilter, offset int) string {
	return itoa(countArgs(f) + offset)
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// SetJobPhase sets the phase column for a job. Used by pipeline actions to track
// in-progress sub-stages (e.g. "downloading", "uploading", "youtube_processing").
func SetJobPhase(ctx context.Context, pool *pgxpool.Pool, jobID string, phase string) error {
	_, err := pool.Exec(ctx,
		`UPDATE jobs SET phase = $1 WHERE id = $2`,
		phase, jobID,
	)
	return err
}

// ClearJobPhase sets the phase column to NULL, indicating no active sub-stage.
func ClearJobPhase(ctx context.Context, pool *pgxpool.Pool, jobID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE jobs SET phase = NULL WHERE id = $1`,
		jobID,
	)
	return err
}

// ListJobsByPhase returns all jobs whose phase column matches the given value.
// Used by plugins to resume jobs stuck in a named sub-stage after server restart.
func ListJobsByPhase(ctx context.Context, pool *pgxpool.Pool, phase string) ([]Job, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, feature, status, trigger,
		       error_message, started_at, completed_at
		FROM jobs
		WHERE phase = $1
		ORDER BY started_at ASC
	`, phase)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(
			&j.ID, &j.Type, &j.Feature, &j.Status, &j.Trigger,
			&j.ErrorMessage, &j.StartedAt, &j.CompletedAt,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
