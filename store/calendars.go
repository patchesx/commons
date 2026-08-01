package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Calendar struct {
	ID           string
	Name         string
	Slug         string
	Description  *string
	IcalURL      *string
	Timezone     string
	DisplayOrder int
	CreatedBy    *string
	CreatedAt    time.Time
}

type CalendarEvent struct {
	ID          string
	CalendarID  string
	Title       string
	Description *string
	Location    *string
	URL         *string
	StartTime   time.Time
	EndTime     *time.Time
	AllDay      bool
	CreatedAt   time.Time
}

// ListCalendars returns all calendars ordered by display_order then name.
func ListCalendars(ctx context.Context, pool *pgxpool.Pool) ([]Calendar, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, slug, description, ical_url, timezone, display_order, created_by, created_at
		FROM calendars
		ORDER BY display_order, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendars(rows)
}

// GetCalendarBySlug returns a single calendar by its URL slug. Returns ErrNotFound if absent.
func GetCalendarBySlug(ctx context.Context, pool *pgxpool.Pool, slug string) (*Calendar, error) {
	c := &Calendar{}
	err := pool.QueryRow(ctx, `
		SELECT id, name, slug, description, ical_url, timezone, display_order, created_by, created_at
		FROM calendars
		WHERE slug = $1
	`, slug).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.CreatedBy, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// GetCalendarByID returns a single calendar by its UUID. Returns ErrNotFound if absent.
func GetCalendarByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Calendar, error) {
	c := &Calendar{}
	err := pool.QueryRow(ctx, `
		SELECT id, name, slug, description, ical_url, timezone, display_order, created_by, created_at
		FROM calendars
		WHERE id = $1
	`, id).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.CreatedBy, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CreateCalendar inserts a new calendar and populates c.ID and c.CreatedAt.
func CreateCalendar(ctx context.Context, pool *pgxpool.Pool, c *Calendar) error {
	return pool.QueryRow(ctx, `
		INSERT INTO calendars (name, slug, description, ical_url, timezone, display_order, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at
	`, c.Name, c.Slug, c.Description, c.IcalURL, c.Timezone, c.DisplayOrder, c.CreatedBy,
	).Scan(&c.ID, &c.CreatedAt)
}

// UpdateCalendar updates name, slug, description, ical_url, and timezone for an existing calendar.
func UpdateCalendar(ctx context.Context, pool *pgxpool.Pool, c *Calendar) error {
	_, err := pool.Exec(ctx, `
		UPDATE calendars
		SET name = $1, slug = $2, description = $3, ical_url = $4, timezone = $5, display_order = $6
		WHERE id = $7
	`, c.Name, c.Slug, c.Description, c.IcalURL, c.Timezone, c.DisplayOrder, c.ID)
	return err
}

// DeleteCalendar removes a calendar by ID (cascades to calendar_events).
func DeleteCalendar(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM calendars WHERE id = $1`, id)
	return err
}

// CountCalendars returns the total number of calendars.
func CountCalendars(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM calendars`).Scan(&count)
	return count, err
}

// CountUpcomingEvents returns the number of events starting within the next N days.
func CountUpcomingEvents(ctx context.Context, pool *pgxpool.Pool, withinDays int) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM calendar_events
		WHERE start_time >= NOW() AND start_time < NOW() + $1 * INTERVAL '1 day'
	`, withinDays).Scan(&count)
	return count, err
}

// ListUpcomingCalendarEvents returns future events across all calendars, ordered by start_time.
// Pass limit=0 for no limit.
func ListUpcomingCalendarEvents(ctx context.Context, pool *pgxpool.Pool, limit int) ([]CalendarEvent, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if limit > 0 {
		rows, err = pool.Query(ctx, `
			SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, created_at
			FROM calendar_events WHERE start_time >= NOW() ORDER BY start_time LIMIT $1
		`, limit)
	} else {
		rows, err = pool.Query(ctx, `
			SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, created_at
			FROM calendar_events WHERE start_time >= NOW() ORDER BY start_time
		`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendarEvents(rows)
}

// ListUpcomingEventsForCalendar returns future events for one calendar within the next withinDays days.
// Pass withinDays=0 for no upper bound.
func ListUpcomingEventsForCalendar(ctx context.Context, pool *pgxpool.Pool, calendarID string, withinDays int) ([]CalendarEvent, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if withinDays > 0 {
		rows, err = pool.Query(ctx, `
			SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, created_at
			FROM calendar_events
			WHERE calendar_id = $1
			  AND start_time >= NOW()
			  AND start_time < NOW() + $2 * INTERVAL '1 day'
			ORDER BY start_time
		`, calendarID, withinDays)
	} else {
		rows, err = pool.Query(ctx, `
			SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, created_at
			FROM calendar_events
			WHERE calendar_id = $1 AND start_time >= NOW()
			ORDER BY start_time
		`, calendarID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendarEvents(rows)
}

// ListCalendarEvents returns all events for a calendar ordered by start_time ascending.
func ListCalendarEvents(ctx context.Context, pool *pgxpool.Pool, calendarID string) ([]CalendarEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, created_at
		FROM calendar_events
		WHERE calendar_id = $1
		ORDER BY start_time
	`, calendarID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendarEvents(rows)
}

// CreateCalendarEvent inserts a new event and populates e.ID and e.CreatedAt.
func CreateCalendarEvent(ctx context.Context, pool *pgxpool.Pool, e *CalendarEvent) error {
	return pool.QueryRow(ctx, `
		INSERT INTO calendar_events (calendar_id, title, description, location, url, start_time, end_time, all_day)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at
	`, e.CalendarID, e.Title, e.Description, e.Location, e.URL, e.StartTime, e.EndTime, e.AllDay,
	).Scan(&e.ID, &e.CreatedAt)
}

// GetCalendarEventByID returns a single calendar event by its UUID. Returns ErrNotFound if absent.
func GetCalendarEventByID(ctx context.Context, pool *pgxpool.Pool, id string) (*CalendarEvent, error) {
	e := &CalendarEvent{}
	err := pool.QueryRow(ctx, `
		SELECT id, calendar_id, title, description, location, url,
		       start_time, end_time, all_day, created_at
		FROM calendar_events WHERE id = $1
	`, id).Scan(&e.ID, &e.CalendarID, &e.Title, &e.Description, &e.Location, &e.URL,
		&e.StartTime, &e.EndTime, &e.AllDay, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// UpdateCalendarEvent updates all mutable fields for an existing event.
func UpdateCalendarEvent(ctx context.Context, pool *pgxpool.Pool, e *CalendarEvent) error {
	_, err := pool.Exec(ctx, `
		UPDATE calendar_events
		SET title = $1, description = $2, location = $3, url = $4,
		    start_time = $5, end_time = $6, all_day = $7
		WHERE id = $8
	`, e.Title, e.Description, e.Location, e.URL, e.StartTime, e.EndTime, e.AllDay, e.ID)
	return err
}

// DeleteCalendarEvent removes an event by ID.
func DeleteCalendarEvent(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM calendar_events WHERE id = $1`, id)
	return err
}

// BulkInsertCalendarEvents inserts a slice of events, skipping duplicates on (calendar_id, title, start_time).
// Returns the count of rows actually inserted.
func BulkInsertCalendarEvents(ctx context.Context, pool *pgxpool.Pool, calendarID string, events []CalendarEvent) (int, error) {
	inserted := 0
	for i := range events {
		events[i].CalendarID = calendarID
		tag, err := pool.Exec(ctx, `
			INSERT INTO calendar_events (calendar_id, title, description, location, url, start_time, end_time, all_day)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (calendar_id, title, start_time) DO NOTHING
		`, calendarID, events[i].Title, events[i].Description, events[i].Location, events[i].URL,
			events[i].StartTime, events[i].EndTime, events[i].AllDay)
		if err != nil {
			return inserted, err
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

func scanCalendars(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Calendar, error) {
	var calendars []Calendar
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		calendars = append(calendars, c)
	}
	return calendars, rows.Err()
}

func scanCalendarEvents(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]CalendarEvent, error) {
	var events []CalendarEvent
	for rows.Next() {
		var e CalendarEvent
		if err := rows.Scan(&e.ID, &e.CalendarID, &e.Title, &e.Description, &e.Location, &e.URL,
			&e.StartTime, &e.EndTime, &e.AllDay, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
