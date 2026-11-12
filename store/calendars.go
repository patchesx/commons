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
	Kind         string
	Color        *string
	CreatedBy    *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
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
	// RRule is a non-empty iCal RRULE string (no DTSTART) when this event is a
	// recurring series; its StartTime is the first occurrence.
	RRule     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListCalendars returns all non-personal calendars ordered by display_order then name.
// Personal calendars are excluded — they render only for their owner in Slack App Home.
func ListCalendars(ctx context.Context, pool *pgxpool.Pool) ([]Calendar, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, COALESCE(slug, ''), description, ical_url, timezone, display_order, kind, color, created_by, created_at, updated_at
		FROM calendars
		WHERE kind <> 'personal'
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
		SELECT id, name, COALESCE(slug, ''), description, ical_url, timezone, display_order, kind, color, created_by, created_at, updated_at
		FROM calendars
		WHERE slug = $1
	`, slug).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.Kind, &c.Color, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
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
		SELECT id, name, COALESCE(slug, ''), description, ical_url, timezone, display_order, kind, color, created_by, created_at, updated_at
		FROM calendars
		WHERE id = $1
	`, id).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.Kind, &c.Color, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CreateCalendar inserts a new calendar and populates c.ID, c.CreatedAt, and c.UpdatedAt.
// An empty Kind defaults to standard. An empty slug is stored as NULL — slug has a
// UNIQUE constraint and slug-less rows (personal calendars) must not collide on "".
func CreateCalendar(ctx context.Context, pool *pgxpool.Pool, c *Calendar) error {
	if c.Kind == "" {
		c.Kind = CalendarKindStandard
	}
	var slug *string
	if c.Slug != "" {
		slug = &c.Slug
	}
	return pool.QueryRow(ctx, `
		INSERT INTO calendars (name, slug, description, ical_url, timezone, display_order, kind, color, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`, c.Name, slug, c.Description, c.IcalURL, c.Timezone, c.DisplayOrder, c.Kind, c.Color, c.CreatedBy,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

// UpdateCalendar updates name, slug, description, ical_url, timezone, display_order, and color
// for an existing calendar. Kind is immutable after creation and is not updated.
func UpdateCalendar(ctx context.Context, pool *pgxpool.Pool, c *Calendar) error {
	_, err := pool.Exec(ctx, `
		UPDATE calendars
		SET name = $1, slug = $2, description = $3, ical_url = $4, timezone = $5, display_order = $6,
		    color = $7, updated_at = NOW()
		WHERE id = $8
	`, c.Name, c.Slug, c.Description, c.IcalURL, c.Timezone, c.DisplayOrder, c.Color, c.ID)
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

// ListCalendarEvents returns all events for a calendar ordered by start_time ascending.
func ListCalendarEvents(ctx context.Context, pool *pgxpool.Pool, calendarID string) ([]CalendarEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, COALESCE(rrule, ''), created_at, updated_at
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

// CreateCalendarEvent inserts a new event and populates e.ID, e.CreatedAt, and e.UpdatedAt.
// Returns ErrNotEditable when the target calendar is a composite or personal calendar —
// those render only merged member events and never store their own.
func CreateCalendarEvent(ctx context.Context, pool *pgxpool.Pool, e *CalendarEvent) error {
	var kind string
	if err := pool.QueryRow(ctx, `SELECT kind FROM calendars WHERE id = $1`, e.CalendarID).Scan(&kind); err != nil {
		return err
	}
	if kind == CalendarKindComposite || kind == CalendarKindPersonal {
		return ErrNotEditable
	}
	return pool.QueryRow(ctx, `
		INSERT INTO calendar_events (calendar_id, title, description, location, url, start_time, end_time, all_day, rrule)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`, e.CalendarID, e.Title, e.Description, e.Location, e.URL, e.StartTime, e.EndTime, e.AllDay, nullStr(e.RRule),
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
}

// GetCalendarEventByID returns a single calendar event by its UUID. Returns ErrNotFound if absent.
func GetCalendarEventByID(ctx context.Context, pool *pgxpool.Pool, id string) (*CalendarEvent, error) {
	e := &CalendarEvent{}
	err := pool.QueryRow(ctx, `
		SELECT id, calendar_id, title, description, location, url,
		       start_time, end_time, all_day, COALESCE(rrule, ''), created_at, updated_at
		FROM calendar_events WHERE id = $1
	`, id).Scan(&e.ID, &e.CalendarID, &e.Title, &e.Description, &e.Location, &e.URL,
		&e.StartTime, &e.EndTime, &e.AllDay, &e.RRule, &e.CreatedAt, &e.UpdatedAt)
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
		    start_time = $5, end_time = $6, all_day = $7, rrule = $8, updated_at = NOW()
		WHERE id = $9
	`, e.Title, e.Description, e.Location, e.URL, e.StartTime, e.EndTime, e.AllDay, nullStr(e.RRule), e.ID)
	return err
}

// DeleteCalendarEvent removes an event by ID.
func DeleteCalendarEvent(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM calendar_events WHERE id = $1`, id)
	return err
}

func scanCalendars(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Calendar, error) {
	var calendars []Calendar
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.Kind, &c.Color, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
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
			&e.StartTime, &e.EndTime, &e.AllDay, &e.RRule, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// nullStr maps an empty string to a SQL NULL.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
