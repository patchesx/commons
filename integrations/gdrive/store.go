package gdrive

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BackupData holds Google Drive backup metadata for a recording_upload job.
// Stored in gdrive.backup_data.
type BackupData struct {
	JobID         string
	IntegrationID string
	FolderID      *string
}

// CreateBackupData inserts a new gdrive.backup_data row for a job.
func CreateBackupData(ctx context.Context, pool *pgxpool.Pool, d *BackupData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO gdrive.backup_data (job_id, integration_id, folder_id)
		VALUES ($1, $2, $3)
	`, d.JobID, d.IntegrationID, d.FolderID)
	return err
}

// GetBackupData returns the gdrive.backup_data row for a job. Returns nil, nil if not found.
func GetBackupData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*BackupData, error) {
	d := &BackupData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, folder_id
		FROM gdrive.backup_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.FolderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SetFolderID updates the folder_id on a gdrive.backup_data row.
func SetFolderID(ctx context.Context, pool *pgxpool.Pool, jobID, folderID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE gdrive.backup_data SET folder_id = $1 WHERE job_id = $2`,
		folderID, jobID,
	)
	return err
}
