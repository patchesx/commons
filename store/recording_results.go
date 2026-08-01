package store

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
)

// ListRecentRecordingResults returns the most recent recording_upload jobs as
// RecordingJobResult values, joining zoom, youtube, and gdrive extension data.
// Used by the Slack App Home and interactions modal.
func ListRecentRecordingResults(ctx context.Context, pool *pgxpool.Pool, limit int) ([]platform.RecordingJobResult, error) {
	rows, err := pool.Query(ctx, `
		SELECT j.id, j.status, j.error_message, j.started_at, j.completed_at,
		       zrd.meeting_topic, zrd.meeting_date, zrd.duration_secs,
		       yud.video_id, yud.title,
		       gbd.folder_id
		FROM jobs j
		JOIN zoom.recording_data zrd ON zrd.job_id = j.id
		LEFT JOIN youtube.upload_data yud ON yud.job_id = j.id
		LEFT JOIN gdrive.backup_data gbd ON gbd.job_id = j.id
		WHERE j.type = 'recording_upload'
		ORDER BY j.started_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []platform.RecordingJobResult
	for rows.Next() {
		var r platform.RecordingJobResult
		if err := rows.Scan(
			&r.JobID, &r.Status, &r.ErrorMessage, &r.StartedAt, &r.CompletedAt,
			&r.MeetingTopic, &r.MeetingDate, &r.DurationSecs,
			&r.VideoID, &r.VideoTitle,
			&r.FolderID,
		); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	log.Printf("store: ListRecentRecordingResults returning %d results", len(results))
	return results, rows.Err()
}
