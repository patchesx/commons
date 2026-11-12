package adminui

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"commons/store"
	admintempl "commons/web/templ"
)

// EmbedCalendar handles GET /embed/{slug} — public, unauthenticated, standalone
// calendar page for iframe embedding. Agenda view (default) shows the next N
// weeks from today; month view shows a static read-only grid with plain-link
// paging. Standard and composite calendars are embeddable; personal calendars
// are not (defensive — they have no slug and are unreachable by it).
func (d Deps) EmbedCalendar() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		slug := r.PathValue("slug")

		cal, err := store.GetCalendarBySlug(ctx, d.Pool, slug)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if cal.Kind == store.CalendarKindPersonal {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=60")

		view := r.URL.Query().Get("view")
		if view != "month" {
			view = "agenda"
		}
		details := r.URL.Query().Get("details") != "0"

		loc := calendarLocation(ctx, d.Pool, cal.ID)
		colors, _ := store.CalendarColorMap(ctx, d.Pool)

		var (
			agendaDays  []admintempl.AgendaDay
			grid        []admintempl.CalGridCell
			occs        []store.EventOccurrence
			year, month int
		)

		switch view {
		case "month":
			now := time.Now()
			year, _ = strconv.Atoi(r.URL.Query().Get("year"))
			month, _ = strconv.Atoi(r.URL.Query().Get("month"))
			if year == 0 {
				year = now.Year()
			}
			if month < 1 || month > 12 {
				month = int(now.Month())
			}
			from, to := monthWindow(year, month, loc)
			occs, err = loadCalendarOccurrences(ctx, d.Pool, cal, from, to)
			grid = buildCalGrid(year, month)
		default: // agenda
			weeks := 4
			if v, perr := strconv.Atoi(r.URL.Query().Get("weeks")); perr == nil && v >= 1 && v <= 12 {
				weeks = v
			}
			from := time.Now()
			occs, err = loadCalendarOccurrences(ctx, d.Pool, cal, from, from.AddDate(0, 0, 7*weeks))
			agendaDays = admintempl.GroupByDay(occs, loc)
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		admintempl.EmbedCalendarPage(*cal, view, details, agendaDays, colors, year, month, grid, occs, loc).Render(ctx, w)
	}
}

// embedBaseURL derives the external origin (scheme + host) from a request,
// honoring the standard proxy forwarding header.
func embedBaseURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host
}
