package legislation

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenStatesSyncData holds the results of an openstates_sync job.
type OpenStatesSyncData struct {
	JobID         string
	IntegrationID string
	BillsImported int
	BillsUpdated  int
	BillsSkipped  int
}

// CreateOpenStatesSyncData inserts a new openstates sync_data row.
func CreateOpenStatesSyncData(ctx context.Context, pool *pgxpool.Pool, d *OpenStatesSyncData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO openstates.sync_data (job_id, integration_id, bills_imported, bills_updated, bills_skipped)
		VALUES ($1, $2, $3, $4, $5)
	`, d.JobID, d.IntegrationID, d.BillsImported, d.BillsUpdated, d.BillsSkipped)
	return err
}

// GetOpenStatesSyncData returns the sync results for a job. Returns nil, nil if not found.
func GetOpenStatesSyncData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*OpenStatesSyncData, error) {
	d := &OpenStatesSyncData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, bills_imported, bills_updated, bills_skipped
		FROM openstates.sync_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.BillsImported, &d.BillsUpdated, &d.BillsSkipped)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// UpdateOpenStatesSyncCounts sets the final bill counts for a sync job.
func UpdateOpenStatesSyncCounts(ctx context.Context, pool *pgxpool.Pool, jobID string, imported, updated, skipped int) error {
	_, err := pool.Exec(ctx, `
		UPDATE openstates.sync_data
		SET bills_imported = $1, bills_updated = $2, bills_skipped = $3
		WHERE job_id = $4
	`, imported, updated, skipped, jobID)
	return err
}

// LegistarSyncData holds the results of a legistar_sync job.
type LegistarSyncData struct {
	JobID         string
	IntegrationID string
	BillsImported int
	BillsUpdated  int
}

// CreateLegistarSyncData inserts a new legistar sync_data row.
func CreateLegistarSyncData(ctx context.Context, pool *pgxpool.Pool, d *LegistarSyncData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO legistar.sync_data (job_id, integration_id, bills_imported, bills_updated)
		VALUES ($1, $2, $3, $4)
	`, d.JobID, d.IntegrationID, d.BillsImported, d.BillsUpdated)
	return err
}

// GetLegistarSyncData returns the sync results for a job. Returns nil, nil if not found.
func GetLegistarSyncData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*LegistarSyncData, error) {
	d := &LegistarSyncData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, bills_imported, bills_updated
		FROM legistar.sync_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.BillsImported, &d.BillsUpdated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// UpdateLegistarSyncCounts sets the final bill counts for a sync job.
func UpdateLegistarSyncCounts(ctx context.Context, pool *pgxpool.Pool, jobID string, imported, updated int) error {
	_, err := pool.Exec(ctx, `
		UPDATE legistar.sync_data
		SET bills_imported = $1, bills_updated = $2
		WHERE job_id = $3
	`, imported, updated, jobID)
	return err
}
