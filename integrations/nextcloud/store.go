package nextcloud

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupData holds Nextcloud backup metadata for a recording_upload job.
// Stored in nextcloud.backup_data.
type BackupData struct {
	JobID         string
	IntegrationID string
	FolderPath    *string
}

// CreateBackupData inserts a new nextcloud.backup_data row for a job.
func CreateBackupData(ctx context.Context, pool *pgxpool.Pool, d *BackupData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO nextcloud.backup_data (job_id, integration_id, folder_path)
		VALUES ($1, $2, $3)
	`, d.JobID, d.IntegrationID, d.FolderPath)
	return err
}

// GetBackupData returns the nextcloud.backup_data row for a job. Returns nil, nil if not found.
func GetBackupData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*BackupData, error) {
	d := &BackupData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, folder_path
		FROM nextcloud.backup_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.FolderPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SetFolderPath updates the folder_path on a nextcloud.backup_data row.
func SetFolderPath(ctx context.Context, pool *pgxpool.Pool, jobID, folderPath string) error {
	_, err := pool.Exec(ctx,
		`UPDATE nextcloud.backup_data SET folder_path = $1 WHERE job_id = $2`,
		folderPath, jobID,
	)
	return err
}
