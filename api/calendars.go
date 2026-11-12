package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/ical"
	"commons/internal/httpx"
	"commons/store"
	"commons/util"
)

// ListUpcomingEvents handles GET /api/calendar-events — upcoming occurrences
// across all calendars, recurring series expanded.
// Optional: ?limit=N (default 20, max 100).
func ListUpcomingEvents(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		// Expand over a 60-day horizon, then trim to the requested limit.
		occs, err := store.ListUpcomingCalendarOccurrences(r.Context(), pool, time.Now(), time.Now().AddDate(0, 0, 60))
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list events")
			return
		}
		if len(occs) > limit {
			occs = occs[:limit]
		}
		if occs == nil {
			occs = []store.EventOccurrence{}
		}
		httpx.WriteJSON(w, http.StatusOK, occs)
	}
}

var slugRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// calendarRequest wraps store.Calendar with composite membership, which is not part
// of the calendar row itself.
type calendarRequest struct {
	store.Calendar
	MemberIDs []string `json:"member_ids"`
}

// normalizeCalendarKind validates the requested kind. Personal calendars are created
// from Slack App Home only, never via the REST API.
func normalizeCalendarKind(kind string) (string, string) {
	switch kind {
	case "":
		return store.CalendarKindStandard, ""
	case store.CalendarKindStandard, store.CalendarKindComposite:
		return kind, ""
	default:
		return "", "kind must be standard or composite"
	}
}

func validateCompositeRequest(cal *store.Calendar, memberIDs []string) string {
	if cal.Kind != store.CalendarKindComposite {
		if len(memberIDs) > 0 {
			return "member_ids are only valid for composite calendars"
		}
		return ""
	}
	if cal.IcalURL != nil && *cal.IcalURL != "" {
		return "composite calendars cannot have an iCal URL"
	}
	if len(memberIDs) == 0 {
		return "composite calendars need at least one member"
	}
	return ""
}

// membershipError maps store membership errors to HTTP responses. Returns false when
// err is nil or not a membership error.
func membershipError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrNotComposite),
		errors.Is(err, store.ErrInvalidMember),
		errors.Is(err, store.ErrCycleDetected):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return true
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "calendar not found")
		return true
	}
	return false
}

// ListCalendars handles GET /api/calendars.
func ListCalendars(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calendars, err := store.ListCalendars(r.Context(), pool)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list calendars")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, calendars)
	}
}

// CreateCalendar handles POST /api/calendars. Accepts kind (standard|composite)
// and member_ids for composites.
func CreateCalendar(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req calendarRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" || req.Slug == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name and slug are required")
			return
		}
		if !slugRE.MatchString(req.Slug) {
			httpx.WriteError(w, http.StatusBadRequest, "slug must contain only lowercase letters, digits, and hyphens")
			return
		}
		kind, kindErr := normalizeCalendarKind(req.Kind)
		if kindErr != "" {
			httpx.WriteError(w, http.StatusBadRequest, kindErr)
			return
		}
		req.Kind = kind
		if msg := validateCompositeRequest(&req.Calendar, req.MemberIDs); msg != "" {
			httpx.WriteError(w, http.StatusBadRequest, msg)
			return
		}
		if req.Timezone == "" {
			req.Timezone = util.DefaultTimezone(ctx, pool, encKey)
		}

		if err := store.CreateCalendar(ctx, pool, &req.Calendar); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create calendar")
			return
		}
		if req.Kind == store.CalendarKindComposite {
			if err := store.AddCalendarMembers(ctx, pool, req.ID, req.MemberIDs); err != nil {
				store.DeleteCalendar(ctx, pool, req.ID) // roll back the orphan row
				if membershipError(w, err) {
					return
				}
				httpx.WriteError(w, http.StatusInternalServerError, "failed to add members")
				return
			}
		}

		store.LogAction(ctx, pool, nil, "calendar.created", req.ID, map[string]string{
			"name": req.Name,
			"slug": req.Slug,
			"kind": req.Kind,
		})

		httpx.WriteJSON(w, http.StatusCreated, req.Calendar)
	}
}

// UpdateCalendar handles PUT /api/calendars/{id}. Kind is immutable; member_ids
// (on a composite) replace the full membership.
func UpdateCalendar(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		existing, err := store.GetCalendarByID(ctx, pool, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				httpx.WriteError(w, http.StatusNotFound, "calendar not found")
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "failed to load calendar")
			return
		}

		var req calendarRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" || req.Slug == "" {
			httpx.WriteError(w, http.StatusBadRequest, "name and slug are required")
			return
		}
		if !slugRE.MatchString(req.Slug) {
			httpx.WriteError(w, http.StatusBadRequest, "slug must contain only lowercase letters, digits, and hyphens")
			return
		}
		if req.Kind != "" && req.Kind != existing.Kind {
			httpx.WriteError(w, http.StatusBadRequest, "kind cannot be changed after creation")
			return
		}
		req.Kind = existing.Kind
		if msg := validateCompositeRequest(&req.Calendar, req.MemberIDs); msg != "" {
			httpx.WriteError(w, http.StatusBadRequest, msg)
			return
		}
		req.ID = id

		if err := store.UpdateCalendar(ctx, pool, &req.Calendar); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update calendar")
			return
		}
		if req.Kind == store.CalendarKindComposite {
			if err := store.ReplaceCalendarMembers(ctx, pool, id, req.MemberIDs); err != nil {
				if membershipError(w, err) {
					return
				}
				httpx.WriteError(w, http.StatusInternalServerError, "failed to update members")
				return
			}
		}

		store.LogAction(ctx, pool, nil, "calendar.updated", id, map[string]string{
			"name": req.Name,
			"slug": req.Slug,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteCalendar handles DELETE /api/calendars/{id}.
func DeleteCalendar(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")

		if err := store.DeleteCalendar(ctx, pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete calendar")
			return
		}

		store.LogAction(ctx, pool, nil, "calendar.deleted", id, nil)

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ListCalendarEvents handles GET /api/calendars/{id}/events.
func ListCalendarEvents(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		events, err := store.ListCalendarEvents(r.Context(), pool, id)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list events")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, events)
	}
}

// CreateCalendarEvent handles POST /api/calendars/{id}/events.
func CreateCalendarEvent(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calendarID := r.PathValue("id")

		var evt store.CalendarEvent
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if evt.Title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title is required")
			return
		}
		evt.CalendarID = calendarID

		if err := store.CreateCalendarEvent(ctx, pool, &evt); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create event")
			return
		}

		store.LogAction(ctx, pool, nil, "calendar_event.created", evt.ID, map[string]string{
			"title":       evt.Title,
			"calendar_id": calendarID,
		})

		httpx.WriteJSON(w, http.StatusCreated, evt)
	}
}

// UpdateCalendarEvent handles PUT /api/calendars/{id}/events/{eventId}.
func UpdateCalendarEvent(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		eventID := r.PathValue("eventId")

		var evt store.CalendarEvent
		if err := json.NewDecoder(r.Body).Decode(&evt); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if evt.Title == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title is required")
			return
		}
		evt.ID = eventID

		if err := store.UpdateCalendarEvent(ctx, pool, &evt); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update event")
			return
		}

		store.LogAction(ctx, pool, nil, "calendar_event.updated", eventID, map[string]string{
			"title": evt.Title,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteCalendarEvent handles DELETE /api/calendars/{id}/events/{eventId}.
func DeleteCalendarEvent(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		eventID := r.PathValue("eventId")

		if err := store.DeleteCalendarEvent(ctx, pool, eventID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete event")
			return
		}

		store.LogAction(ctx, pool, nil, "calendar_event.deleted", eventID, nil)

		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ImportCalendar handles POST /api/calendars/{id}/import.
// Fetches an ICS URL, expands its events over the standard sync window, and
// inserts them (skipping duplicates). Insert-only — existing rows are never
// deleted; feed calendars reconcile via the sync job instead.
func ImportCalendar(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calendarID := r.PathValue("id")

		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			httpx.WriteError(w, http.StatusBadRequest, "url is required")
			return
		}

		loc, err := calendarTimezone(ctx, pool, calendarID)
		if err != nil {
			httpx.WriteError(w, http.StatusNotFound, "calendar not found")
			return
		}
		now := time.Now()
		events, err := ical.Fetch(ctx, body.URL, loc)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "failed to fetch or parse ICS feed: "+err.Error())
			return
		}
		occurrences := ical.ExpandAll(events, now.AddDate(-1, 0, 0), now.AddDate(2, 0, 0), loc)

		feedOccs := make([]store.FeedOccurrence, 0, len(occurrences))
		for _, occ := range occurrences {
			feedOccs = append(feedOccs, store.FeedOccurrence{
				UID:         occ.UID,
				Title:       occ.Title,
				Description: occ.Description,
				Location:    occ.Location,
				URL:         occ.URL,
				StartTime:   occ.StartTime,
				EndTime:     occ.EndTime,
				AllDay:      occ.AllDay,
			})
		}

		inserted, err := store.ImportFeedOccurrences(ctx, pool, calendarID, feedOccs)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to import events")
			return
		}

		store.LogAction(ctx, pool, nil, "calendar.imported", calendarID, map[string]any{
			"url":      body.URL,
			"imported": inserted,
		})

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"imported": inserted,
			"total":    len(occurrences),
		})
	}
}

// calendarTimezone loads a calendar's timezone location, defaulting to UTC.
func calendarTimezone(ctx context.Context, pool *pgxpool.Pool, calendarID string) (*time.Location, error) {
	var tz string
	err := pool.QueryRow(ctx, `SELECT timezone FROM calendars WHERE id = $1`, calendarID).Scan(&tz)
	if err != nil {
		return nil, err
	}
	loc, lerr := time.LoadLocation(tz)
	if lerr != nil || loc == nil {
		return time.UTC, nil
	}
	return loc, nil
}
