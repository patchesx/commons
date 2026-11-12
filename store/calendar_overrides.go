package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teambition/rrule-go"
)

// ListCalendarOccurrences returns the expanded occurrences of a calendar's
// events within [from, to): standalone events in the window plus recurring
// series expanded in the calendar's timezone with overrides applied.
func ListCalendarOccurrences(ctx context.Context, pool *pgxpool.Pool, calendarID string, from, to time.Time) ([]EventOccurrence, error) {
	cal, err := GetCalendarByID(ctx, pool, calendarID)
	if err != nil {
		return nil, err
	}
	events, err := listEventsForWindow(ctx, pool, calendarID, from, to)
	if err != nil {
		return nil, err
	}
	overrides, err := ListEventOverridesByIDs(ctx, pool, eventIDs(events))
	if err != nil {
		return nil, err
	}
	return ExpandEvents(events, overrides, from, to, calendarLocation(cal.Timezone)), nil
}

// ListUpcomingCalendarOccurrences returns expanded occurrences across all
// calendars within [from, to) (dashboard view). Series are expanded in their
// own calendar's timezone.
func ListUpcomingCalendarOccurrences(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) ([]EventOccurrence, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.id, e.calendar_id, e.title, e.description, e.location, e.url,
		       e.start_time, e.end_time, e.all_day, COALESCE(e.rrule, ''), e.created_at, e.updated_at,
		       COALESCE(c.timezone, 'UTC')
		FROM calendar_events e
		JOIN calendars c ON c.id = e.calendar_id
		WHERE e.rrule IS NOT NULL OR (e.start_time >= $1 AND e.start_time < $2)
		ORDER BY e.start_time
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type calEvents struct {
		events []CalendarEvent
		loc    *time.Location
	}
	byCal := map[string]*calEvents{}
	var allIDs []string
	for rows.Next() {
		var e CalendarEvent
		var tz string
		if err := rows.Scan(&e.ID, &e.CalendarID, &e.Title, &e.Description, &e.Location, &e.URL,
			&e.StartTime, &e.EndTime, &e.AllDay, &e.RRule, &e.CreatedAt, &e.UpdatedAt, &tz); err != nil {
			return nil, err
		}
		ce, ok := byCal[e.CalendarID]
		if !ok {
			ce = &calEvents{loc: calendarLocation(tz)}
			byCal[e.CalendarID] = ce
		}
		ce.events = append(ce.events, e)
		allIDs = append(allIDs, e.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	overrides, err := ListEventOverridesByIDs(ctx, pool, allIDs)
	if err != nil {
		return nil, err
	}

	var out []EventOccurrence
	for _, ce := range byCal {
		out = append(out, ExpandEvents(ce.events, overrides, from, to, ce.loc)...)
	}
	sortOccurrences(out)
	return out, nil
}

// listEventsForWindow loads a calendar's events relevant to [from, to):
// every series template (its start may precede the window) plus standalone
// events starting within it.
func listEventsForWindow(ctx context.Context, pool *pgxpool.Pool, calendarID string, from, to time.Time) ([]CalendarEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, calendar_id, title, description, location, url, start_time, end_time, all_day, COALESCE(rrule, ''), created_at, updated_at
		FROM calendar_events
		WHERE calendar_id = $1 AND (rrule IS NOT NULL OR (start_time >= $2 AND start_time < $3))
		ORDER BY start_time
	`, calendarID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func eventIDs(events []CalendarEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

func calendarLocation(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

func sortOccurrences(occs []EventOccurrence) {
	sort.SliceStable(occs, func(i, j int) bool { return occs[i].StartTime.Before(occs[j].StartTime) })
}

// ListEventOverridesByIDs returns overrides for the given series IDs,
// keyed by series ID.
func ListEventOverridesByIDs(ctx context.Context, pool *pgxpool.Pool, seriesIDs []string) (map[string][]EventOverride, error) {
	out := map[string][]EventOverride{}
	if len(seriesIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT id, series_id, occurrence_start, title, description, location, url,
		       start_time, end_time, all_day, deleted, created_at
		FROM calendar_event_overrides
		WHERE series_id = ANY($1)
		ORDER BY occurrence_start
	`, seriesIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o EventOverride
		if err := rows.Scan(&o.ID, &o.SeriesID, &o.OccurrenceStart, &o.Title, &o.Description, &o.Location, &o.URL,
			&o.StartTime, &o.EndTime, &o.AllDay, &o.Deleted, &o.CreatedAt); err != nil {
			return nil, err
		}
		out[o.SeriesID] = append(out[o.SeriesID], o)
	}
	return out, rows.Err()
}

// GetEventOverride returns the override for one occurrence, or nil when absent.
func GetEventOverride(ctx context.Context, pool *pgxpool.Pool, seriesID string, occurrenceStart time.Time) (*EventOverride, error) {
	o := &EventOverride{}
	err := pool.QueryRow(ctx, `
		SELECT id, series_id, occurrence_start, title, description, location, url,
		       start_time, end_time, all_day, deleted, created_at
		FROM calendar_event_overrides
		WHERE series_id = $1 AND occurrence_start = $2
	`, seriesID, occurrenceStart).Scan(&o.ID, &o.SeriesID, &o.OccurrenceStart, &o.Title, &o.Description, &o.Location, &o.URL,
		&o.StartTime, &o.EndTime, &o.AllDay, &o.Deleted, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// UpsertEventOverride inserts or replaces the override for one occurrence.
func UpsertEventOverride(ctx context.Context, pool *pgxpool.Pool, o *EventOverride) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO calendar_event_overrides
			(series_id, occurrence_start, title, description, location, url, start_time, end_time, all_day, deleted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (series_id, occurrence_start) DO UPDATE SET
			title = EXCLUDED.title, description = EXCLUDED.description,
			location = EXCLUDED.location, url = EXCLUDED.url,
			start_time = EXCLUDED.start_time, end_time = EXCLUDED.end_time,
			all_day = EXCLUDED.all_day, deleted = EXCLUDED.deleted
	`, o.SeriesID, o.OccurrenceStart, o.Title, o.Description, o.Location, o.URL, o.StartTime, o.EndTime, o.AllDay, o.Deleted)
	return err
}

// ApplyEventEdit applies an edit to a series occurrence with the given scope.
//
//   - ScopeOne: stores a full-replacement override at occurrenceStart.
//   - ScopeFollowing: splits the series — the original rule is truncated before
//     occurrenceStart (carrying its end conditions), later overrides move to a
//     new series created from the edited values, and the new series' first
//     occurrence is the edited one.
//   - ScopeAll: updates the template (including its rule) and clears overrides.
//
// For standalone events (no rrule) every scope is equivalent to a plain update.
func ApplyEventEdit(ctx context.Context, pool *pgxpool.Pool, seriesID string, occurrenceStart time.Time, scope EventScope, edited *CalendarEvent) error {
	ev, err := GetCalendarEventByID(ctx, pool, seriesID)
	if err != nil {
		return err
	}
	if ev.RRule == "" {
		edited.ID = seriesID
		edited.CalendarID = ev.CalendarID
		if err := DeleteEventOverrides(ctx, pool, seriesID); err != nil { // defensive: standalone has none
			return err
		}
		return UpdateCalendarEvent(ctx, pool, edited)
	}

	switch scope {
	case ScopeOne:
		return UpsertEventOverride(ctx, pool, overrideFromEvent(seriesID, occurrenceStart, edited))
	case ScopeAll:
		if err := DeleteEventOverrides(ctx, pool, seriesID); err != nil {
			return err
		}
		edited.ID = seriesID
		edited.CalendarID = ev.CalendarID
		return UpdateCalendarEvent(ctx, pool, edited)
	case ScopeFollowing:
		return splitSeries(ctx, pool, ev, occurrenceStart, edited)
	default:
		return errors.New("unsupported scope")
	}
}

// DeleteEventWithScope deletes an occurrence or series with the given scope.
// ScopeOne skips the occurrence (EXDATE override); ScopeFollowing truncates the
// series before the occurrence and drops later overrides; ScopeAll removes the
// template (overrides cascade).
func DeleteEventWithScope(ctx context.Context, pool *pgxpool.Pool, seriesID string, occurrenceStart time.Time, scope EventScope) error {
	ev, err := GetCalendarEventByID(ctx, pool, seriesID)
	if err != nil {
		return err
	}
	if ev.RRule == "" {
		return DeleteCalendarEvent(ctx, pool, seriesID)
	}
	switch scope {
	case ScopeOne:
		return UpsertEventOverride(ctx, pool, &EventOverride{
			SeriesID:        seriesID,
			OccurrenceStart: occurrenceStart,
			Deleted:         true,
		})
	case ScopeAll:
		return DeleteCalendarEvent(ctx, pool, seriesID)
	case ScopeFollowing:
		truncated, err := truncateRuleBefore(ev.RRule, occurrenceStart)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `
			UPDATE calendar_events SET rrule = $1, updated_at = NOW() WHERE id = $2
		`, truncated, seriesID); err != nil {
			return err
		}
		_, err = pool.Exec(ctx, `
			DELETE FROM calendar_event_overrides
			WHERE series_id = $1 AND occurrence_start >= $2
		`, seriesID, occurrenceStart)
		return err
	default:
		return errors.New("unsupported scope")
	}
}

// DeleteEventOverrides removes every override of a series (used by ScopeAll).
func DeleteEventOverrides(ctx context.Context, pool *pgxpool.Pool, seriesID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM calendar_event_overrides WHERE series_id = $1`, seriesID)
	return err
}

// overrideFromEvent builds a full-replacement override from edited event fields.
func overrideFromEvent(seriesID string, occurrenceStart time.Time, edited *CalendarEvent) *EventOverride {
	return &EventOverride{
		SeriesID:        seriesID,
		OccurrenceStart: occurrenceStart,
		Title:           &edited.Title,
		Description:     edited.Description,
		Location:        edited.Location,
		URL:             edited.URL,
		StartTime:       &edited.StartTime,
		EndTime:         edited.EndTime,
		AllDay:          &edited.AllDay,
	}
}

// truncateRuleBefore returns the rule with UNTIL set to just before
// occurrenceStart, dropping COUNT (a truncated series is bounded by UNTIL).
func truncateRuleBefore(rule string, occurrenceStart time.Time) (string, error) {
	opt, err := rrule.StrToROption(rule)
	if err != nil {
		return "", err
	}
	opt.Count = 0
	opt.Until = occurrenceStart.Add(-time.Second)
	return opt.RRuleString(), nil
}

// splitSeries implements ScopeFollowing: truncate the original rule before the
// split occurrence, then create a new series from the edited values carrying
// the original rule's frequency/interval/day selection and its end conditions
// (a COUNT is reduced by the occurrences already elapsed). Overrides for
// occurrences at or after the split move to the new series, shifted by the
// time delta so they keep addressing the new series' slots.
func splitSeries(ctx context.Context, pool *pgxpool.Pool, ev *CalendarEvent, occurrenceStart time.Time, edited *CalendarEvent) error {
	cal, err := GetCalendarByID(ctx, pool, ev.CalendarID)
	if err != nil {
		return err
	}
	loc := calendarLocation(cal.Timezone)

	newRule, err := carryRuleForward(ev.RRule, ev.StartTime, occurrenceStart, loc)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	truncated, err := truncateRuleBefore(ev.RRule, occurrenceStart)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE calendar_events SET rrule = $1, updated_at = NOW() WHERE id = $2`, truncated, ev.ID); err != nil {
		return err
	}

	newSeries := *edited
	newSeries.ID = ""
	newSeries.CalendarID = ev.CalendarID
	newSeries.RRule = newRule
	if err := createEventTx(ctx, tx, &newSeries); err != nil {
		return err
	}

	// Move later overrides to the new series, shifting their addressing by the
	// same delta as the split edit so they land on the new series' slots.
	delta := newSeries.StartTime.Sub(occurrenceStart)
	if _, err := tx.Exec(ctx, `
		UPDATE calendar_event_overrides
		SET series_id = $1, occurrence_start = occurrence_start + $2
		WHERE series_id = $3 AND occurrence_start >= $4
	`, newSeries.ID, delta, ev.ID, occurrenceStart); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// carryRuleForward derives the new series' rule from the original: same
// frequency, interval, and day selection; UNTIL kept; COUNT reduced by the
// occurrences elapsed before the split point.
func carryRuleForward(rule string, dtstart, splitAt time.Time, loc *time.Location) (string, error) {
	opt, err := rrule.StrToROption(rule)
	if err != nil {
		return "", err
	}
	if opt.Count > 0 {
		countOpt := *opt
		countOpt.Dtstart = dtstart.In(loc)
		countOpt.Count = 0
		countOpt.Until = time.Time{}
		r, err := rrule.NewRRule(countOpt)
		if err != nil {
			return "", err
		}
		// Occurrences of the unbounded rule in [dtstart, splitAt): include dtstart
		// itself (Between with inc=false drops boundary matches) but not the split.
		elapsed := len(r.Between(dtstart.In(loc).Add(-time.Second), splitAt.In(loc), false))
		opt.Count -= elapsed
		if opt.Count < 1 {
			opt.Count = 1
		}
	}
	return opt.RRuleString(), nil
}

// createEventTx inserts a calendar event inside a transaction.
func createEventTx(ctx context.Context, tx pgx.Tx, e *CalendarEvent) error {
	return tx.QueryRow(ctx, `
		INSERT INTO calendar_events (calendar_id, title, description, location, url, start_time, end_time, all_day, rrule)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`, e.CalendarID, e.Title, e.Description, e.Location, e.URL, e.StartTime, e.EndTime, e.AllDay, nullStr(e.RRule),
	).Scan(&e.ID, &e.CreatedAt, &e.UpdatedAt)
}
