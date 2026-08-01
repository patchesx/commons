package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ScheduledMeeting struct {
	ID                string
	IntegrationID     string
	ZoomMeetingID     int64
	HostEmail         string
	Topic             string
	Agenda            string
	DurationMinutes   int
	Timezone          string
	Password          string // plaintext in memory; encrypted at rest
	JoinURL           string
	StartURL          string // plaintext in memory; encrypted at rest
	IsRecurring       bool
	IsPrivate         bool
	RecurrencePattern []byte // raw JSONB; nil for one-off
	Settings          []byte // raw JSONB
	CreatedBy         string // UUID, references users.id
	SendReminder      bool
	SkipUpload        bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type MeetingOccurrence struct {
	ID               string
	MeetingID        string // UUID, references zoom.scheduled_meetings.id
	ZoomOccurrenceID string // empty string for one-off meetings
	StartTime        time.Time
	DurationMinutes  int
	Status           string // "available" | "deleted"
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// UpcomingOccurrence is MeetingOccurrence plus the parent meeting's topic,
// returned by ListUpcomingOccurrences for use in overlap error messages.
type UpcomingOccurrence struct {
	MeetingOccurrence
	Topic string
}

// CreateScheduledMeeting inserts the series row and all occurrence rows in a single
// transaction. Encrypts m.Password (if non-empty) and m.StartURL before insert.
func CreateScheduledMeeting(ctx context.Context, pool *pgxpool.Pool, encKey []byte, m *ScheduledMeeting, occurrences []MeetingOccurrence) error {
	encPassword := ""
	if m.Password != "" {
		var err error
		encPassword, err = Encrypt(encKey, m.Password)
		if err != nil {
			return err
		}
	}

	encStartURL, err := Encrypt(encKey, m.StartURL)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var meetingID string
	err = tx.QueryRow(ctx, `
		INSERT INTO zoom.scheduled_meetings (
			integration_id, zoom_meeting_id, host_email, topic, agenda,
			duration_minutes, timezone, password, join_url, start_url,
			is_recurring, is_private, recurrence_pattern, settings, created_by, send_reminder, skip_upload
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id
	`,
		m.IntegrationID, m.ZoomMeetingID, m.HostEmail, m.Topic, m.Agenda,
		m.DurationMinutes, m.Timezone,
		nullableString(encPassword), m.JoinURL, encStartURL,
		m.IsRecurring, m.IsPrivate, nullableBytes(m.RecurrencePattern), nullableBytes(m.Settings),
		m.CreatedBy, m.SendReminder, m.SkipUpload,
	).Scan(&meetingID)
	if err != nil {
		return err
	}

	for _, o := range occurrences {
		_, err = tx.Exec(ctx, `
			INSERT INTO zoom.meeting_occurrences (
				meeting_id, zoom_occurrence_id, start_time, duration_minutes, status
			) VALUES ($1, $2, $3, $4, $5)
		`,
			meetingID,
			nullableString(o.ZoomOccurrenceID),
			o.StartTime,
			o.DurationMinutes,
			o.Status,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ListUpcomingOccurrences returns all future available occurrences for an integration,
// including the parent meeting's topic. Does NOT decrypt password/start_url.
//
// excludeMeetingID: exclude all occurrences belonging to this meeting (series edit flow).
// excludeOccurrenceID: exclude a single specific occurrence (occurrence edit flow).
// Pass empty strings for no exclusion.
func ListUpcomingOccurrences(ctx context.Context, pool *pgxpool.Pool, integrationID string, excludeMeetingID string, excludeOccurrenceID string) ([]UpcomingOccurrence, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			mo.id, mo.meeting_id, COALESCE(mo.zoom_occurrence_id, ''),
			mo.start_time, mo.duration_minutes, mo.status,
			mo.created_at, mo.updated_at,
			sm.topic
		FROM zoom.meeting_occurrences mo
		JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
		WHERE sm.integration_id = $1
		  AND mo.start_time > NOW()
		  AND mo.status = 'available'
		  AND ($2 = '' OR sm.id::text != $2)
		  AND ($3 = '' OR mo.id::text != $3)
		ORDER BY mo.start_time ASC
	`, integrationID, excludeMeetingID, excludeOccurrenceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []UpcomingOccurrence
	for rows.Next() {
		var u UpcomingOccurrence
		if err := rows.Scan(
			&u.ID, &u.MeetingID, &u.ZoomOccurrenceID,
			&u.StartTime, &u.DurationMinutes, &u.Status,
			&u.CreatedAt, &u.UpdatedAt,
			&u.Topic,
		); err != nil {
			return nil, err
		}
		results = append(results, u)
	}
	return results, rows.Err()
}

// CountUpcomingOccurrences returns the count of future available occurrences for an
// integration, for threshold enforcement before calling zoom.ScheduleMeeting.
func CountUpcomingOccurrences(ctx context.Context, pool *pgxpool.Pool, integrationID string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM zoom.meeting_occurrences mo
		JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
		WHERE sm.integration_id = $1
		  AND mo.start_time > NOW()
		  AND mo.status = 'available'
	`, integrationID).Scan(&count)
	return count, err
}

// GetScheduledMeetingByZoomID looks up a series by its Zoom meeting ID.
// Returns ErrNotFound if absent.
func GetScheduledMeetingByZoomID(ctx context.Context, pool *pgxpool.Pool, meetingID int64) (*ScheduledMeeting, error) {
	m := &ScheduledMeeting{}
	var password, recurrencePattern, settings *string
	err := pool.QueryRow(ctx, `
		SELECT
			id, integration_id, zoom_meeting_id, host_email, topic, agenda,
			duration_minutes, timezone, password, join_url, start_url,
			is_recurring, is_private, recurrence_pattern::text, settings::text,
			created_by, send_reminder, skip_upload, created_at, updated_at
		FROM zoom.scheduled_meetings
		WHERE zoom_meeting_id = $1
	`, meetingID).Scan(
		&m.ID, &m.IntegrationID, &m.ZoomMeetingID, &m.HostEmail, &m.Topic, &m.Agenda,
		&m.DurationMinutes, &m.Timezone, &password, &m.JoinURL, &m.StartURL,
		&m.IsRecurring, &m.IsPrivate, &recurrencePattern, &settings,
		&m.CreatedBy, &m.SendReminder, &m.SkipUpload, &m.CreatedAt, &m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if password != nil {
		m.Password = *password
	}
	if recurrencePattern != nil {
		m.RecurrencePattern = []byte(*recurrencePattern)
	}
	if settings != nil {
		m.Settings = []byte(*settings)
	}
	return m, nil
}

// MeetingSummary is a lightweight view of a meeting series for the management modal.
// For one-off meetings, NextStart is set and Occurrences is empty.
// For recurring meetings, NextStart is zero and Occurrences is populated.
type MeetingSummary struct {
	ID              string
	ZoomMeetingID   int64
	Topic           string
	IsRecurring     bool
	NextStart       time.Time
	DurationMinutes int
	Timezone        string
	Occurrences     []OccurrenceSummary
}

// OccurrenceSummary is a lightweight view of a single occurrence for the management modal.
type OccurrenceSummary struct {
	ID               string
	ZoomOccurrenceID string
	StartTime        time.Time
	DurationMinutes  int
}

// OccurrenceDetail is an occurrence joined with its parent meeting's key fields.
// Used for single-occurrence edit and cancel handlers.
type OccurrenceDetail struct {
	OccurrenceID     string
	ZoomOccurrenceID string
	StartTime        time.Time
	DurationMinutes  int
	MeetingID        string
	ZoomMeetingID    int64
	Topic            string
	Timezone         string
}

// ListUpcomingMeetingSummaries returns the next 10 upcoming meeting series for an integration.
// For recurring meetings, at most 3 upcoming occurrences are included.
// For one-off meetings, NextStart is set. Ordered by next upcoming start_time ASC.
func ListUpcomingMeetingSummaries(ctx context.Context, pool *pgxpool.Pool, integrationID string) ([]MeetingSummary, error) {
	rows, err := pool.Query(ctx, `
		WITH base AS (
			SELECT
				sm.id, sm.zoom_meeting_id, sm.topic, sm.is_recurring,
				sm.duration_minutes, sm.timezone,
				mo.id AS mo_id,
				COALESCE(mo.zoom_occurrence_id, '') AS zoom_occurrence_id,
				mo.start_time, mo.duration_minutes AS mo_duration,
				ROW_NUMBER() OVER (PARTITION BY sm.id ORDER BY mo.start_time) AS occ_rank,
				MIN(mo.start_time) OVER (PARTITION BY sm.id) AS first_occ
			FROM zoom.meeting_occurrences mo
			JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
			WHERE sm.integration_id = $1
			  AND mo.start_time > NOW()
			  AND mo.status = 'available'
		),
		ranked AS (
			SELECT *,
				DENSE_RANK() OVER (ORDER BY first_occ, id) AS series_rank
			FROM base
		)
		SELECT id, zoom_meeting_id, topic, is_recurring,
		       duration_minutes, timezone,
		       mo_id, zoom_occurrence_id, start_time, mo_duration
		FROM ranked
		WHERE occ_rank <= 1
		  AND series_rank <= 10
		ORDER BY series_rank, start_time ASC
	`, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rowData struct {
		smID             string
		zoomMeetingID    int64
		topic            string
		isRecurring      bool
		smDuration       int
		timezone         string
		moID             string
		zoomOccurrenceID string
		startTime        time.Time
		moDuration       int
	}
	var rds []rowData
	for rows.Next() {
		var rd rowData
		if err := rows.Scan(
			&rd.smID, &rd.zoomMeetingID, &rd.topic, &rd.isRecurring,
			&rd.smDuration, &rd.timezone,
			&rd.moID, &rd.zoomOccurrenceID,
			&rd.startTime, &rd.moDuration,
		); err != nil {
			return nil, err
		}
		rds = append(rds, rd)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var order []string
	summaries := make(map[string]*MeetingSummary)
	for _, rd := range rds {
		if _, ok := summaries[rd.smID]; !ok {
			order = append(order, rd.smID)
			summaries[rd.smID] = &MeetingSummary{
				ID:              rd.smID,
				ZoomMeetingID:   rd.zoomMeetingID,
				Topic:           rd.topic,
				IsRecurring:     rd.isRecurring,
				DurationMinutes: rd.smDuration,
				Timezone:        rd.timezone,
			}
		}
		occ := OccurrenceSummary{
			ID:               rd.moID,
			ZoomOccurrenceID: rd.zoomOccurrenceID,
			StartTime:        rd.startTime,
			DurationMinutes:  rd.moDuration,
		}
		if rd.isRecurring {
			summaries[rd.smID].Occurrences = append(summaries[rd.smID].Occurrences, occ)
		} else {
			summaries[rd.smID].NextStart = rd.startTime
		}
	}

	result := make([]MeetingSummary, 0, len(order))
	for _, id := range order {
		result = append(result, *summaries[id])
	}
	return result, nil
}

// GetScheduledMeetingByID looks up a full meeting series by its internal UUID.
// Decrypts Password and StartURL. Returns ErrNotFound if absent.
func GetScheduledMeetingByID(ctx context.Context, pool *pgxpool.Pool, encKey []byte, id string) (*ScheduledMeeting, error) {
	m := &ScheduledMeeting{}
	var password, startURL, recurrencePattern, settings *string
	err := pool.QueryRow(ctx, `
		SELECT
			id, integration_id, zoom_meeting_id, host_email, topic, agenda,
			duration_minutes, timezone, password, join_url, start_url,
			is_recurring, is_private, recurrence_pattern::text, settings::text,
			created_by, send_reminder, skip_upload, created_at, updated_at
		FROM zoom.scheduled_meetings
		WHERE id = $1
	`, id).Scan(
		&m.ID, &m.IntegrationID, &m.ZoomMeetingID, &m.HostEmail, &m.Topic, &m.Agenda,
		&m.DurationMinutes, &m.Timezone, &password, &m.JoinURL, &startURL,
		&m.IsRecurring, &m.IsPrivate, &recurrencePattern, &settings,
		&m.CreatedBy, &m.SendReminder, &m.SkipUpload, &m.CreatedAt, &m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if password != nil {
		if dec, err := Decrypt(encKey, *password); err == nil {
			m.Password = dec
		}
	}
	if startURL != nil {
		if dec, err := Decrypt(encKey, *startURL); err == nil {
			m.StartURL = dec
		}
	}
	if recurrencePattern != nil {
		m.RecurrencePattern = []byte(*recurrencePattern)
	}
	if settings != nil {
		m.Settings = []byte(*settings)
	}
	return m, nil
}

// GetMeetingOccurrenceByID returns a single occurrence row plus its parent meeting's
// ZoomMeetingID and Timezone. Returns ErrNotFound if absent.
func GetMeetingOccurrenceByID(ctx context.Context, pool *pgxpool.Pool, id string) (*OccurrenceDetail, error) {
	occ := &OccurrenceDetail{}
	var zoomOccurrenceID *string
	err := pool.QueryRow(ctx, `
		SELECT
			mo.id, mo.zoom_occurrence_id, mo.start_time, mo.duration_minutes,
			mo.meeting_id, sm.zoom_meeting_id, sm.topic, sm.timezone
		FROM zoom.meeting_occurrences mo
		JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
		WHERE mo.id = $1
	`, id).Scan(
		&occ.OccurrenceID, &zoomOccurrenceID, &occ.StartTime, &occ.DurationMinutes,
		&occ.MeetingID, &occ.ZoomMeetingID, &occ.Topic, &occ.Timezone,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if zoomOccurrenceID != nil {
		occ.ZoomOccurrenceID = *zoomOccurrenceID
	}
	return occ, nil
}

// DeleteScheduledMeeting hard-deletes a series and cascades to all its occurrences.
func DeleteScheduledMeeting(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM zoom.scheduled_meetings WHERE id = $1`, id)
	return err
}

// DeleteMeetingOccurrence hard-deletes a single occurrence row.
// If it was the last occurrence for the series, the series row is also deleted.
func DeleteMeetingOccurrence(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var meetingID string
	err = tx.QueryRow(ctx, `DELETE FROM zoom.meeting_occurrences WHERE id = $1 RETURNING meeting_id`, id).Scan(&meetingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}

	var remaining int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM zoom.meeting_occurrences WHERE meeting_id = $1`, meetingID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM zoom.scheduled_meetings WHERE id = $1`, meetingID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpdateScheduledMeeting replaces a series record and all its occurrences in a transaction.
// Encrypts Password (if non-empty) and StartURL before update.
// Deletes all existing occurrences for the series, then inserts new ones.
func UpdateScheduledMeeting(ctx context.Context, pool *pgxpool.Pool, encKey []byte, m *ScheduledMeeting, occurrences []MeetingOccurrence) error {
	encPassword := ""
	if m.Password != "" {
		var err error
		encPassword, err = Encrypt(encKey, m.Password)
		if err != nil {
			return err
		}
	}
	encStartURL, err := Encrypt(encKey, m.StartURL)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE zoom.scheduled_meetings SET
			topic = $2, agenda = $3, duration_minutes = $4, timezone = $5,
			password = $6, join_url = $7, start_url = $8,
			is_recurring = $9, is_private = $10, recurrence_pattern = $11, settings = $12,
			send_reminder = $13, skip_upload = $14, updated_at = NOW()
		WHERE id = $1
	`,
		m.ID, m.Topic, m.Agenda, m.DurationMinutes, m.Timezone,
		nullableString(encPassword), m.JoinURL, encStartURL,
		m.IsRecurring, m.IsPrivate, nullableBytes(m.RecurrencePattern), nullableBytes(m.Settings),
		m.SendReminder, m.SkipUpload,
	)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM zoom.meeting_occurrences WHERE meeting_id = $1`, m.ID); err != nil {
		return err
	}
	for _, o := range occurrences {
		_, err = tx.Exec(ctx, `
			INSERT INTO zoom.meeting_occurrences (
				meeting_id, zoom_occurrence_id, start_time, duration_minutes, status
			) VALUES ($1, $2, $3, $4, $5)
		`,
			m.ID,
			nullableString(o.ZoomOccurrenceID),
			o.StartTime,
			o.DurationMinutes,
			o.Status,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SyncMeetingOccurrences replaces all occurrence rows for a meeting in a single transaction.
// Used by the sync engine to update occurrences regardless of whether meeting metadata changed.
func SyncMeetingOccurrences(ctx context.Context, pool *pgxpool.Pool, meetingID string, occurrences []MeetingOccurrence) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM zoom.meeting_occurrences WHERE meeting_id = $1`, meetingID); err != nil {
		return err
	}
	for _, o := range occurrences {
		_, err = tx.Exec(ctx, `
			INSERT INTO zoom.meeting_occurrences (
				meeting_id, zoom_occurrence_id, start_time, duration_minutes, status
			) VALUES ($1, $2, $3, $4, $5)
		`,
			meetingID,
			nullableString(o.ZoomOccurrenceID),
			o.StartTime,
			o.DurationMinutes,
			o.Status,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpdateMeetingOccurrence updates start_time and duration_minutes for a single occurrence.
func UpdateMeetingOccurrence(ctx context.Context, pool *pgxpool.Pool, id string, startTime time.Time, durationMinutes int) error {
	_, err := pool.Exec(ctx, `
		UPDATE zoom.meeting_occurrences
		SET start_time = $2, duration_minutes = $3, updated_at = NOW()
		WHERE id = $1
	`, id, startTime, durationMinutes)
	return err
}

// ListScheduledMeetingsByIntegration returns all scheduled meetings for an integration.
// Only ID and ZoomMeetingID are populated; used for sync deletion detection.
func ListScheduledMeetingsByIntegration(ctx context.Context, pool *pgxpool.Pool, integrationID string) ([]ScheduledMeeting, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, zoom_meeting_id FROM zoom.scheduled_meetings WHERE integration_id = $1
	`, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ScheduledMeeting
	for rows.Next() {
		var m ScheduledMeeting
		if err := rows.Scan(&m.ID, &m.ZoomMeetingID); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// OccurrenceReminder is the minimal data needed to send a pre-meeting reminder.
type OccurrenceReminder struct {
	OccurrenceID string
	MeetingID    string
	Topic        string
	StartTime    time.Time
	Timezone     string
	StartURL     string // decrypted
	JoinURL      string
	CreatedByID  string // users.id of the scheduler; empty if sentinel (sync-imported)
	IsSyncImport bool
}

// ListOccurrencesDueForReminder returns occurrences whose start_time is between
// 55 and 65 minutes from now, where reminder_sent_at IS NULL, status = 'available',
// and send_reminder = TRUE on the parent meeting.
func ListOccurrencesDueForReminder(ctx context.Context, pool *pgxpool.Pool, encKey []byte, sentinelUserID string) ([]OccurrenceReminder, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			mo.id, sm.id, sm.topic, mo.start_time, sm.timezone,
			sm.start_url, sm.join_url, sm.created_by
		FROM zoom.meeting_occurrences mo
		JOIN zoom.scheduled_meetings sm ON sm.id = mo.meeting_id
		WHERE mo.start_time BETWEEN NOW() + INTERVAL '55 minutes'
		                        AND NOW() + INTERVAL '65 minutes'
		  AND mo.reminder_sent_at IS NULL
		  AND mo.status = 'available'
		  AND sm.send_reminder = TRUE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []OccurrenceReminder
	for rows.Next() {
		var r OccurrenceReminder
		var encStartURL *string
		if err := rows.Scan(
			&r.OccurrenceID, &r.MeetingID, &r.Topic, &r.StartTime, &r.Timezone,
			&encStartURL, &r.JoinURL, &r.CreatedByID,
		); err != nil {
			return nil, err
		}
		if encStartURL != nil {
			if dec, err := Decrypt(encKey, *encStartURL); err == nil {
				r.StartURL = dec
			}
		}
		r.IsSyncImport = r.CreatedByID == sentinelUserID
		results = append(results, r)
	}
	return results, rows.Err()
}

// MarkReminderSent sets reminder_sent_at = NOW() for the given occurrence.
func MarkReminderSent(ctx context.Context, pool *pgxpool.Pool, occurrenceID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE zoom.meeting_occurrences
		SET reminder_sent_at = NOW()
		WHERE id = $1
	`, occurrenceID)
	return err
}

// CheckOverlapConflict returns the start time of the first proposed meeting window that
// violates the overlap policy against a set of existing scheduled occurrences.
// Returns nil if no conflict is found.
// allowOverlap: if true, up to maxOverlap concurrent meetings are permitted.
func CheckOverlapConflict(proposedStart time.Time, proposedDuration int, existing []UpcomingOccurrence, allowOverlap bool, maxOverlap int) *time.Time {
	propEnd := proposedStart.Add(time.Duration(proposedDuration) * time.Minute)
	concurrent := 0
	for _, ex := range existing {
		exEnd := ex.StartTime.Add(time.Duration(ex.DurationMinutes) * time.Minute)
		if proposedStart.Before(exEnd) && ex.StartTime.Before(propEnd) {
			concurrent++
		}
	}
	var reject bool
	if !allowOverlap {
		reject = concurrent > 0
	} else {
		reject = concurrent > maxOverlap
	}
	if reject {
		return &proposedStart
	}
	return nil
}

// nullableString returns nil if s is empty, otherwise &s.
// Used for optional TEXT columns that should be NULL when not set.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableBytes returns nil if b is empty, otherwise b.
// Used for optional JSONB columns.
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
