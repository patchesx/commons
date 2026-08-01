package youtube

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UploadData holds YouTube-specific upload metadata for a recording_upload job.
// Stored in youtube.upload_data.
type UploadData struct {
	JobID         string
	IntegrationID string
	VideoID       *string
	Title         string
	MeetingTopic  string
	MeetingDate   time.Time
	DurationSecs  *int
}

// CreateUploadData inserts a new youtube.upload_data row for a job.
func CreateUploadData(ctx context.Context, pool *pgxpool.Pool, d *UploadData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO youtube.upload_data (job_id, integration_id, title, meeting_topic, meeting_date, duration_secs)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (job_id) DO NOTHING
	`, d.JobID, d.IntegrationID, d.Title, d.MeetingTopic, d.MeetingDate, d.DurationSecs)
	return err
}

// GetUploadData returns the youtube.upload_data row for a job. Returns nil, nil if not found.
func GetUploadData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*UploadData, error) {
	d := &UploadData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, video_id, title, meeting_topic, meeting_date, duration_secs
		FROM youtube.upload_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.VideoID, &d.Title,
		&d.MeetingTopic, &d.MeetingDate, &d.DurationSecs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SetVideoID updates the video_id on a youtube.upload_data row.
func SetVideoID(ctx context.Context, pool *pgxpool.Pool, jobID, videoID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE youtube.upload_data SET video_id = $1 WHERE job_id = $2`,
		videoID, jobID,
	)
	return err
}
