package s3storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupData holds S3 backup metadata for a recording_upload job.
// Stored in s3storage.backup_data.
type BackupData struct {
	JobID         string
	IntegrationID string
	KeyPrefix     *string
}

// CreateBackupData inserts a new s3storage.backup_data row for a job.
func CreateBackupData(ctx context.Context, pool *pgxpool.Pool, d *BackupData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO s3storage.backup_data (job_id, integration_id, key_prefix)
		VALUES ($1, $2, $3)
	`, d.JobID, d.IntegrationID, d.KeyPrefix)
	return err
}

// GetBackupData returns the s3storage.backup_data row for a job. Returns nil, nil if not found.
func GetBackupData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*BackupData, error) {
	d := &BackupData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, key_prefix
		FROM s3storage.backup_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.KeyPrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SetKeyPrefix updates the key_prefix on a s3storage.backup_data row.
func SetKeyPrefix(ctx context.Context, pool *pgxpool.Pool, jobID, keyPrefix string) error {
	_, err := pool.Exec(ctx,
		`UPDATE s3storage.backup_data SET key_prefix = $1 WHERE job_id = $2`,
		keyPrefix, jobID,
	)
	return err
}
