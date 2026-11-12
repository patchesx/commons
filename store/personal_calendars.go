package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PersonalCalendarName is the fixed display name of every user's personal calendar.
const PersonalCalendarName = "My Calendar"

// GetPersonalCalendar returns the user's personal composite calendar, or nil when
// the user has none. Personal calendars are private: they are excluded from
// ListCalendars and never have a slug, so only this lookup (and GetCalendarByID
// for an already-selected view) can reach them.
func GetPersonalCalendar(ctx context.Context, pool *pgxpool.Pool, userID string) (*Calendar, error) {
	c := &Calendar{}
	err := pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(slug, ''), description, ical_url, timezone, display_order, kind, color, created_by, created_at, updated_at
		FROM calendars
		WHERE kind = 'personal' AND created_by = $1
	`, userID).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.IcalURL, &c.Timezone, &c.DisplayOrder, &c.Kind, &c.Color, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// CreatePersonalCalendar creates the user's personal composite calendar with the
// given members. Fails if the user already has one (one per user, DB-enforced).
// The calendar is removed again if a member fails validation.
func CreatePersonalCalendar(ctx context.Context, pool *pgxpool.Pool, userID string, memberIDs []string, timezone string) (*Calendar, error) {
	cal := &Calendar{
		Name:      PersonalCalendarName,
		Timezone:  timezone,
		Kind:      CalendarKindPersonal,
		CreatedBy: &userID,
	}
	if err := CreateCalendar(ctx, pool, cal); err != nil {
		return nil, err
	}
	if err := AddCalendarMembers(ctx, pool, cal.ID, memberIDs); err != nil {
		_ = DeleteCalendar(ctx, pool, cal.ID)
		return nil, err
	}
	return cal, nil
}
