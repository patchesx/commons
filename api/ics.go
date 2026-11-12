package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// ServeICS handles GET /cal/{slug} — public, no auth required.
// Generates and returns a valid iCalendar feed for the named calendar.
// Composites serve their merged, deduplicated member occurrences.
func ServeICS(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		slug := strings.TrimSuffix(r.PathValue("slug"), ".ics")

		cal, err := store.GetCalendarBySlug(ctx, pool, slug)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if cal.Kind == store.CalendarKindPersonal {
			// Personal calendars have no slug (NULL), so this is unreachable today —
			// guarded explicitly in case slug assignment changes.
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.ics"`, slug))

		now := time.Now().UTC()
		var sb strings.Builder
		sb.WriteString("BEGIN:VCALENDAR\r\n")
		sb.WriteString("VERSION:2.0\r\n")
		sb.WriteString("PRODID:-//Commons//EN\r\n")
		sb.WriteString("CALSCALE:GREGORIAN\r\n")
		sb.WriteString("METHOD:PUBLISH\r\n")
		sb.WriteString(icsLine("X-WR-CALNAME", cal.Name))
		sb.WriteString(icsLine("X-WR-TIMEZONE", cal.Timezone))

		if cal.Kind == store.CalendarKindComposite {
			// Composites export flat, expanded occurrences (series members expanded
			// over the same ±1/+2-year window the feed parser uses).
			from := now.AddDate(-1, 0, 0)
			to := now.AddDate(2, 0, 0)
			occs, err := store.ListCompositeOccurrences(ctx, pool, cal.ID, from, to)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, occ := range occs {
				sb.WriteString("BEGIN:VEVENT\r\n")
				sb.WriteString(icsLine("UID", icsOccUID(occ)+"@commons"))
				sb.WriteString(icsLine("DTSTAMP", now.Format("20060102T150405Z")))
				writeOccurrenceTimes(&sb, occ)
				sb.WriteString(icsLine("SUMMARY", icsEscape(occ.Title)))
				if occ.Description != nil && *occ.Description != "" {
					sb.WriteString(icsLine("DESCRIPTION", icsEscape(*occ.Description)))
				}
				if occ.Location != nil && *occ.Location != "" {
					sb.WriteString(icsLine("LOCATION", icsEscape(*occ.Location)))
				}
				if occ.URL != nil && *occ.URL != "" {
					sb.WriteString(icsLine("URL", *occ.URL))
				}
				sb.WriteString("END:VEVENT\r\n")
			}
			sb.WriteString("END:VCALENDAR\r\n")
			fmt.Fprint(w, sb.String())
			return
		}

		events, err := store.ListCalendarEvents(ctx, pool, cal.ID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		overrides, err := store.ListEventOverridesByIDs(ctx, pool, icsEventIDs(events))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		for _, e := range events {
			// Master VEVENT: the series template (with its rule) or the standalone event.
			sb.WriteString("BEGIN:VEVENT\r\n")
			sb.WriteString(icsLine("UID", e.ID+"@commons"))
			sb.WriteString(icsLine("DTSTAMP", now.Format("20060102T150405Z")))
			writeEventTimes(&sb, e)
			sb.WriteString(icsLine("SUMMARY", icsEscape(e.Title)))
			if e.Description != nil && *e.Description != "" {
				sb.WriteString(icsLine("DESCRIPTION", icsEscape(*e.Description)))
			}
			if e.Location != nil && *e.Location != "" {
				sb.WriteString(icsLine("LOCATION", icsEscape(*e.Location)))
			}
			if e.URL != nil && *e.URL != "" {
				sb.WriteString(icsLine("URL", *e.URL))
			}
			if e.RRule != "" {
				sb.WriteString(icsLine("RRULE", e.RRule))
				// Skipped occurrences become EXDATEs on the master.
				for _, ov := range overrides[e.ID] {
					if ov.Deleted {
						sb.WriteString(icsLine("EXDATE", ov.OccurrenceStart.UTC().Format("20060102T150405Z")))
					}
				}
			}
			sb.WriteString("END:VEVENT\r\n")

			// Edited occurrences: override VEVENTs sharing the series UID and
			// addressed by RECURRENCE-ID (the original series slot).
			for _, ov := range overrides[e.ID] {
				if ov.Deleted {
					continue
				}
				sb.WriteString("BEGIN:VEVENT\r\n")
				sb.WriteString(icsLine("UID", e.ID+"@commons"))
				sb.WriteString(icsLine("DTSTAMP", now.Format("20060102T150405Z")))
				sb.WriteString(icsLine("RECURRENCE-ID", ov.OccurrenceStart.UTC().Format("20060102T150405Z")))
				allDay := e.AllDay
				if ov.AllDay != nil {
					allDay = *ov.AllDay
				}
				start := e.StartTime
				if ov.StartTime != nil {
					start = *ov.StartTime
				}
				end := e.EndTime
				if ov.EndTime != nil {
					end = ov.EndTime
				}
				writeICSRange(&sb, start, end, allDay)
				title := e.Title
				if ov.Title != nil {
					title = *ov.Title
				}
				sb.WriteString(icsLine("SUMMARY", icsEscape(title)))
				desc := e.Description
				if ov.Description != nil {
					desc = ov.Description
				}
				if desc != nil && *desc != "" {
					sb.WriteString(icsLine("DESCRIPTION", icsEscape(*desc)))
				}
				loc := e.Location
				if ov.Location != nil {
					loc = ov.Location
				}
				if loc != nil && *loc != "" {
					sb.WriteString(icsLine("LOCATION", icsEscape(*loc)))
				}
				url := e.URL
				if ov.URL != nil {
					url = ov.URL
				}
				if url != nil && *url != "" {
					sb.WriteString(icsLine("URL", *url))
				}
				sb.WriteString("END:VEVENT\r\n")
			}
		}

		sb.WriteString("END:VCALENDAR\r\n")
		fmt.Fprint(w, sb.String())
	}
}

func icsEventIDs(events []store.CalendarEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

// icsOccUID builds a stable unique UID for a flat-exported occurrence.
func icsOccUID(occ store.EventOccurrence) string {
	if occ.ID == "" {
		// Feed-sourced occurrence without a DB ID — derive from content.
		return fmt.Sprintf("%x", occ.Title+occ.StartTime.UTC().Format(time.RFC3339))
	}
	return occ.ID + "-" + occ.OccurrenceStart.UTC().Format("20060102T150405Z")
}

func writeEventTimes(sb *strings.Builder, e store.CalendarEvent) {
	writeICSRange(sb, e.StartTime, e.EndTime, e.AllDay)
}

func writeOccurrenceTimes(sb *strings.Builder, occ store.EventOccurrence) {
	writeICSRange(sb, occ.StartTime, occ.EndTime, occ.AllDay)
}

func writeICSRange(sb *strings.Builder, start time.Time, end *time.Time, allDay bool) {
	if allDay {
		sb.WriteString(icsLine("DTSTART;VALUE=DATE", start.UTC().Format("20060102")))
		if end != nil {
			sb.WriteString(icsLine("DTEND;VALUE=DATE", end.UTC().Format("20060102")))
		}
		return
	}
	sb.WriteString(icsLine("DTSTART", start.UTC().Format("20060102T150405Z")))
	if end != nil {
		sb.WriteString(icsLine("DTEND", end.UTC().Format("20060102T150405Z")))
	}
}

// icsLine formats a single iCalendar property line with CRLF termination.
func icsLine(key, value string) string {
	return key + ":" + value + "\r\n"
}

// icsEscape escapes special characters in iCalendar text values.
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
