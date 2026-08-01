package zoom

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// RecordingData holds Zoom-specific meeting metadata for a recording_upload job.
// Stored in zoom.recording_data; replaces the old public.zoom_recordings table.
type RecordingData struct {
	JobID                 string
	IntegrationID         string
	MeetingUUID           string
	MeetingTopic          string
	MeetingDate           time.Time
	DurationSecs          *int
	HostEmail             *string
	RecordingDeletedAt    *time.Time
	Phase                 *string // nil when not running; 'backing_up'|'downloading'|'uploading'|'youtube_processing'
	ScheduledOccurrenceID *string // UUID of zoom.meeting_occurrences row; nil if no match
}

// CreateRecordingData inserts a new zoom.recording_data row for a job.
func CreateRecordingData(ctx context.Context, pool *pgxpool.Pool, d *RecordingData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO zoom.recording_data
		    (job_id, integration_id, meeting_uuid, meeting_topic, meeting_date, duration_secs, host_email, scheduled_occurrence_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, d.JobID, d.IntegrationID, d.MeetingUUID, d.MeetingTopic, d.MeetingDate, d.DurationSecs, d.HostEmail, d.ScheduledOccurrenceID)
	return err
}

// FindOccurrenceForRecording looks up a zoom.meeting_occurrences row matching the given
// Zoom meeting ID and occurrence ID. For one-off meetings, occurrenceID is empty and
// the match is on zoom_meeting_id alone (only one occurrence row exists per one-off series).
// Returns nil, nil if no matching scheduled meeting exists locally.
func FindOccurrenceForRecording(ctx context.Context, pool *pgxpool.Pool, integrationID string, zoomMeetingID int64, occurrenceID string) (*string, error) {
	var id string
	var err error
	if occurrenceID == "" {
		err = pool.QueryRow(ctx, `
			SELECT mo.id
			FROM zoom.meeting_occurrences mo
			JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
			WHERE sm.integration_id = $1
			  AND sm.zoom_meeting_id = $2
			  AND (mo.zoom_occurrence_id IS NULL OR mo.zoom_occurrence_id = '')
			LIMIT 1
		`, integrationID, zoomMeetingID).Scan(&id)
	} else {
		err = pool.QueryRow(ctx, `
			SELECT mo.id
			FROM zoom.meeting_occurrences mo
			JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
			WHERE sm.integration_id = $1
			  AND sm.zoom_meeting_id = $2
			  AND mo.zoom_occurrence_id = $3
		`, integrationID, zoomMeetingID, occurrenceID).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// GetSkipUploadForMeeting returns true if the scheduled meeting matching the given
// integration ID and Zoom meeting ID has skip_upload set. Returns false if no row found.
func GetSkipUploadForMeeting(ctx context.Context, pool *pgxpool.Pool, integrationID string, zoomMeetingID int64) (bool, error) {
	var skip bool
	err := pool.QueryRow(ctx, `
		SELECT skip_upload FROM zoom.scheduled_meetings
		WHERE integration_id = $1 AND zoom_meeting_id = $2
		LIMIT 1
	`, integrationID, zoomMeetingID).Scan(&skip)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return skip, nil
}

// GetIsPrivateForMeeting returns true if the scheduled meeting matching the given
// integration ID and Zoom meeting ID has is_private set. Returns false if no row found.
func GetIsPrivateForMeeting(ctx context.Context, pool *pgxpool.Pool, integrationID string, zoomMeetingID int64) (bool, error) {
	var private bool
	err := pool.QueryRow(ctx, `
		SELECT is_private FROM zoom.scheduled_meetings
		WHERE integration_id = $1 AND zoom_meeting_id = $2
		LIMIT 1
	`, integrationID, zoomMeetingID).Scan(&private)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return private, nil
}

// GetRecordingData returns the zoom.recording_data row for a job. Returns nil, nil if not found.
func GetRecordingData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*RecordingData, error) {
	d := &RecordingData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, meeting_uuid, meeting_topic, meeting_date,
		       duration_secs, host_email, recording_deleted_at, phase, scheduled_occurrence_id
		FROM zoom.recording_data WHERE job_id = $1
	`, jobID).Scan(
		&d.JobID, &d.IntegrationID, &d.MeetingUUID, &d.MeetingTopic, &d.MeetingDate,
		&d.DurationSecs, &d.HostEmail, &d.RecordingDeletedAt, &d.Phase, &d.ScheduledOccurrenceID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// GetByMeetingUUID returns the recording_data row for a Zoom meeting UUID.
// Returns nil, nil if not found. Used for webhook dedup checks.
func GetByMeetingUUID(ctx context.Context, pool *pgxpool.Pool, uuid string) (*RecordingData, error) {
	d := &RecordingData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, meeting_uuid, meeting_topic, meeting_date,
		       duration_secs, host_email, recording_deleted_at, phase, scheduled_occurrence_id
		FROM zoom.recording_data WHERE meeting_uuid = $1
	`, uuid).Scan(
		&d.JobID, &d.IntegrationID, &d.MeetingUUID, &d.MeetingTopic, &d.MeetingDate,
		&d.DurationSecs, &d.HostEmail, &d.RecordingDeletedAt, &d.Phase, &d.ScheduledOccurrenceID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SetPhase updates the phase column on zoom.recording_data.
// Pass nil to clear the phase (job is no longer in an in-progress state).
func SetPhase(ctx context.Context, pool *pgxpool.Pool, jobID string, phase *string) error {
	_, err := pool.Exec(ctx,
		`UPDATE zoom.recording_data SET phase = $1 WHERE job_id = $2`,
		phase, jobID,
	)
	return err
}

// MarkDeleted sets recording_deleted_at to NOW() for the given job.
func MarkDeleted(ctx context.Context, pool *pgxpool.Pool, jobID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE zoom.recording_data SET recording_deleted_at = NOW() WHERE job_id = $1`,
		jobID,
	)
	return err
}

// MeetingSyncData holds counters for a zoom meeting sync job.
// Stored in zoom.meeting_sync_data.
type MeetingSyncData struct {
	JobID             string
	IntegrationID     string
	MeetingsImported  int
	MeetingsUpdated   int
	MeetingsDeleted   int
	OccurrencesSynced int
}

// CreateMeetingSyncData inserts a new zoom.meeting_sync_data row for a job.
func CreateMeetingSyncData(ctx context.Context, pool *pgxpool.Pool, d *MeetingSyncData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO zoom.meeting_sync_data
		    (job_id, integration_id, meetings_imported, meetings_updated, meetings_deleted, occurrences_synced)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, d.JobID, d.IntegrationID, d.MeetingsImported, d.MeetingsUpdated, d.MeetingsDeleted, d.OccurrencesSynced)
	return err
}

// GetMeetingSyncData returns the zoom.meeting_sync_data row for a job. Returns nil, nil if not found.
func GetMeetingSyncData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*MeetingSyncData, error) {
	d := &MeetingSyncData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, meetings_imported, meetings_updated, meetings_deleted, occurrences_synced
		FROM zoom.meeting_sync_data WHERE job_id = $1
	`, jobID).Scan(
		&d.JobID, &d.IntegrationID, &d.MeetingsImported, &d.MeetingsUpdated, &d.MeetingsDeleted, &d.OccurrencesSynced,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// UpdateMeetingSyncCounts updates the counters on a zoom.meeting_sync_data row.
func UpdateMeetingSyncCounts(ctx context.Context, pool *pgxpool.Pool, jobID string, imported, updated, deleted, occurrences int) error {
	_, err := pool.Exec(ctx, `
		UPDATE zoom.meeting_sync_data
		SET meetings_imported = $2, meetings_updated = $3, meetings_deleted = $4, occurrences_synced = $5
		WHERE job_id = $1
	`, jobID, imported, updated, deleted, occurrences)
	return err
}

// ListJobsInPhase returns all jobs whose zoom.recording_data.phase matches the given value.
// Used by the YouTube processing poller to resume jobs in 'youtube_processing' phase on startup.
func ListJobsInPhase(ctx context.Context, pool *pgxpool.Pool, phase string) ([]store.Job, error) {
	rows, err := pool.Query(ctx, `
		SELECT j.id, j.type, j.feature, j.status, j.trigger,
		       j.error_message, j.started_at, j.completed_at
		FROM jobs j
		JOIN zoom.recording_data zrd ON zrd.job_id = j.id
		WHERE zrd.phase = $1
		ORDER BY j.started_at ASC
	`, phase)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []store.Job
	for rows.Next() {
		var j store.Job
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
