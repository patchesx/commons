package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Calendar kinds. Kind is immutable after creation.
const (
	CalendarKindStandard  = "standard"
	CalendarKindComposite = "composite"
	CalendarKindPersonal  = "personal"
)

// Errors returned by calendar membership and event operations.
// Handlers map these to user-friendly messages.
var (
	// ErrNotComposite is returned when members are added to a calendar that cannot hold members.
	ErrNotComposite = errors.New("calendar is not a composite")
	// ErrInvalidMember is returned when a candidate member cannot be a member
	// (unknown, personal, or the composite itself).
	ErrInvalidMember = errors.New("invalid member calendar")
	// ErrCycleDetected is returned when adding a member would create a membership cycle.
	ErrCycleDetected = errors.New("adding this member would create a cycle")
	// ErrNotEditable is returned when events are added directly to a calendar that
	// only renders merged member events (composite or personal).
	ErrNotEditable = errors.New("this calendar does not support direct events")
)

// ICSFetcher fetches events from a remote ICS feed for a syndicated member calendar.
// Injected so the store layer stays free of HTTP concerns.
type ICSFetcher func(ctx context.Context, url string) ([]CalendarEvent, error)

// ListCalendarMembers returns the member calendars of a composite, ordered by display_order then name.
func ListCalendarMembers(ctx context.Context, pool *pgxpool.Pool, compositeID string) ([]Calendar, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.name, c.slug, c.description, c.ical_url, c.timezone, c.display_order, c.kind, c.color, c.created_by, c.created_at, c.updated_at
		FROM calendar_members m
		JOIN calendars c ON c.id = m.member_id
		WHERE m.composite_id = $1
		ORDER BY c.display_order, c.name
	`, compositeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendars(rows)
}

// AddCalendarMembers adds members to a composite after validating each candidate.
// Idempotent: members already present are skipped.
func AddCalendarMembers(ctx context.Context, pool *pgxpool.Pool, compositeID string, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return nil
	}
	comp, err := GetCalendarByID(ctx, pool, compositeID)
	if err != nil {
		return err
	}
	if comp.Kind != CalendarKindComposite && comp.Kind != CalendarKindPersonal {
		return ErrNotComposite
	}
	for _, memberID := range memberIDs {
		if err := validateMember(ctx, pool, compositeID, memberID); err != nil {
			return err
		}
	}
	for _, memberID := range memberIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO calendar_members (composite_id, member_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, compositeID, memberID); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceCalendarMembers atomically replaces the full membership of a composite.
func ReplaceCalendarMembers(ctx context.Context, pool *pgxpool.Pool, compositeID string, memberIDs []string) error {
	comp, err := GetCalendarByID(ctx, pool, compositeID)
	if err != nil {
		return err
	}
	if comp.Kind != CalendarKindComposite && comp.Kind != CalendarKindPersonal {
		return ErrNotComposite
	}
	for _, memberID := range memberIDs {
		if err := validateMember(ctx, pool, compositeID, memberID); err != nil {
			return err
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM calendar_members WHERE composite_id = $1`, compositeID); err != nil {
		return err
	}
	for _, memberID := range memberIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO calendar_members (composite_id, member_id) VALUES ($1, $2)`, compositeID, memberID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func validateMember(ctx context.Context, pool *pgxpool.Pool, compositeID, memberID string) error {
	if memberID == compositeID {
		return fmt.Errorf("%w: a calendar cannot contain itself", ErrInvalidMember)
	}
	member, err := GetCalendarByID(ctx, pool, memberID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidMember, memberID)
	}
	if member.Kind == CalendarKindPersonal {
		return fmt.Errorf("%w: personal calendars cannot be members", ErrInvalidMember)
	}
	cycles, err := WouldCreateCycle(ctx, pool, compositeID, memberID)
	if err != nil {
		return err
	}
	if cycles {
		return fmt.Errorf("%w: %s already contains this calendar", ErrCycleDetected, member.Name)
	}
	return nil
}

// WouldCreateCycle reports whether compositeID is reachable from candidateID via
// membership edges — i.e. whether adding candidateID as a member would create a cycle.
func WouldCreateCycle(ctx context.Context, pool *pgxpool.Pool, compositeID, candidateID string) (bool, error) {
	visited := map[string]bool{}
	var walk func(calID string) (bool, error)
	walk = func(calID string) (bool, error) {
		if calID == compositeID {
			return true, nil
		}
		if visited[calID] {
			return false, nil
		}
		visited[calID] = true
		rows, err := pool.Query(ctx, `SELECT member_id FROM calendar_members WHERE composite_id = $1`, calID)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		var memberIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return false, err
			}
			memberIDs = append(memberIDs, id)
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
		for _, id := range memberIDs {
			hit, err := walk(id)
			if err != nil {
				return false, err
			}
			if hit {
				return true, nil
			}
		}
		return false, nil
	}
	return walk(candidateID)
}

// ListCompositeEvents returns the merged, deduplicated events of a composite calendar
// (or personal calendar). Members are expanded recursively; syndicated members are
// fetched via fetchICS (nil skips them). A failing feed is logged and skipped so one
// dead member does not blank the whole view.
func ListCompositeEvents(ctx context.Context, pool *pgxpool.Pool, compositeID string, fetchICS ICSFetcher) ([]CalendarEvent, error) {
	var merged []CalendarEvent
	seen := map[string]bool{}
	var walk func(calID string) error
	walk = func(calID string) error {
		if seen[calID] {
			return nil // defense in depth against cycles
		}
		seen[calID] = true
		members, err := ListCalendarMembers(ctx, pool, calID)
		if err != nil {
			return err
		}
		for _, m := range members {
			switch {
			case m.Kind == CalendarKindComposite:
				if err := walk(m.ID); err != nil {
					return err
				}
			case m.IcalURL != nil && *m.IcalURL != "":
				if fetchICS == nil {
					continue
				}
				evts, err := fetchICS(ctx, *m.IcalURL)
				if err != nil {
					log.Printf("store: composite member feed %s (%s): %v", m.Name, m.ID, err)
					continue
				}
				merged = append(merged, evts...)
			default:
				evts, err := ListCalendarEvents(ctx, pool, m.ID)
				if err != nil {
					return err
				}
				merged = append(merged, evts...)
			}
		}
		return nil
	}
	if err := walk(compositeID); err != nil {
		return nil, err
	}
	return dedupeSortEvents(merged), nil
}

// dedupeSortEvents collapses events sharing (title, start_time) — the same natural key
// as the calendar_events UNIQUE constraint — and sorts by start time.
func dedupeSortEvents(events []CalendarEvent) []CalendarEvent {
	type key struct {
		title string
		start time.Time
	}
	seen := make(map[key]bool, len(events))
	out := make([]CalendarEvent, 0, len(events))
	for _, e := range events {
		k := key{title: e.Title, start: e.StartTime}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out
}
