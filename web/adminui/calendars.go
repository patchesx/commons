package adminui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/jobs"
	"commons/store"
	"commons/util"
	admintempl "commons/web/templ"
)

func (d Deps) CalendarsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.URL.Query().Get("cal")
		year, _ := strconv.Atoi(r.URL.Query().Get("year"))
		month, _ := strconv.Atoi(r.URL.Query().Get("month"))
		view := r.URL.Query().Get("view")

		cals, sel, events, y, mo, grid, membersByCal, err := loadCalendarsPageData(ctx, d.Pool, calID, year, month)
		if err != nil {
			http.Error(w, "failed to load calendars", http.StatusInternalServerError)
			return
		}

		var lastSynced *time.Time
		var reminders []store.CalendarReminder
		var agendaDays []admintempl.AgendaDay
		var colors map[string]string
		agendaStart := time.Now()
		if sel != nil {
			if sel.Kind != store.CalendarKindPersonal {
				if sel.IcalURL != nil && *sel.IcalURL != "" {
					if ts, ok := store.GetCalendarLastSynced(ctx, d.Pool, sel.ID); ok {
						lastSynced = &ts
					}
				}
				reminders, _ = store.ListCalendarReminders(ctx, d.Pool, sel.ID)
			}
			if view == "agenda" {
				agendaStart = parseAgendaStart(r.URL.Query().Get("start"))
				loc := calendarLocation(ctx, d.Pool, sel.ID)
				occs, aerr := loadCalendarOccurrences(ctx, d.Pool, sel, agendaStart, agendaStart.AddDate(0, 0, 28))
				if aerr != nil {
					http.Error(w, "failed to load agenda", http.StatusInternalServerError)
					return
				}
				agendaDays = admintempl.GroupByDay(occs, loc)
				colors, _ = store.CalendarColorMap(ctx, d.Pool)
			}
		}
		admintempl.CalendarsPage(cals, sel, y, mo, grid, events, membersByCal, lastSynced, reminders, view, agendaDays, colors, agendaStart, embedBaseURL(r)).Render(ctx, w)
	}
}

// parseAgendaStart parses a YYYY-MM-DD agenda window start, defaulting to today.
func parseAgendaStart(s string) time.Time {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Now()
}

func (d Deps) CalendarsCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		kind := r.FormValue("kind")
		if kind == "" {
			kind = store.CalendarKindStandard
		}
		if kind != store.CalendarKindStandard && kind != store.CalendarKindComposite {
			FragmentError(w, r, "invalid calendar type")
			return
		}
		cal := store.Calendar{
			Name:     r.FormValue("name"),
			Slug:     r.FormValue("slug"),
			Timezone: r.FormValue("timezone"),
			Kind:     kind,
		}
		if desc := r.FormValue("description"); desc != "" {
			cal.Description = &desc
		}
		if color := r.FormValue("color"); color != "" {
			cal.Color = &color
		}
		if v, err := strconv.Atoi(r.FormValue("display_order")); err == nil && v >= 0 {
			cal.DisplayOrder = v
		}
		members := r.Form["members"]
		if cal.Name == "" || cal.Slug == "" {
			FragmentError(w, r, "name and slug required")
			return
		}
		if cal.Timezone == "" {
			cal.Timezone = util.DefaultTimezone(ctx, d.Pool, d.EncKey)
		}
		if kind == store.CalendarKindComposite {
			// Composites never carry a feed URL — a stale form value is ignored, not an error.
			cal.IcalURL = nil
			if len(members) == 0 {
				FragmentError(w, r, "composite calendars need at least one member calendar")
				return
			}
		} else if icalURL := r.FormValue("ical_url"); icalURL != "" {
			cal.IcalURL = &icalURL
		}
		if err := store.CreateCalendar(ctx, d.Pool, &cal); err != nil {
			FragmentError(w, r, "failed to create calendar: "+err.Error())
			return
		}
		if kind == store.CalendarKindComposite {
			if err := store.AddCalendarMembers(ctx, d.Pool, cal.ID, members); err != nil {
				store.DeleteCalendar(ctx, d.Pool, cal.ID) // roll back the orphan row
				FragmentError(w, r, "failed to add members: "+err.Error())
				return
			}
			w.Header().Set("HX-Redirect", "/admin/calendars?cal="+cal.ID)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		importedCount := 0
		if cal.IcalURL != nil {
			// Initial import doubles as the first sync: uid-aware so the sync
			// job can reconcile later runs.
			if res, err := jobs.SyncFeedCalendar(ctx, d.Pool, &cal); err == nil {
				importedCount = res.Inserted
			}
		}
		redirect := "/admin/calendars?cal=" + cal.ID
		if importedCount > 0 {
			redirect += "&imported=" + strconv.Itoa(importedCount)
		}
		w.Header().Set("HX-Redirect", redirect)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) CalendarsUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		existing, err := store.GetCalendarByID(ctx, d.Pool, id)
		if err != nil {
			FragmentError(w, r, "calendar not found")
			return
		}
		cal := store.Calendar{
			ID:       id,
			Name:     r.FormValue("name"),
			Slug:     r.FormValue("slug"),
			Timezone: r.FormValue("timezone"),
			Kind:     existing.Kind, // kind is immutable; store ignores it on update anyway
		}
		if desc := r.FormValue("description"); desc != "" {
			cal.Description = &desc
		}
		if color := r.FormValue("color"); color != "" {
			cal.Color = &color
		}
		if v, err := strconv.Atoi(r.FormValue("display_order")); err == nil && v >= 0 {
			cal.DisplayOrder = v
		}
		if cal.Timezone == "" {
			cal.Timezone = util.DefaultTimezone(ctx, d.Pool, d.EncKey)
		}
		if existing.Kind == store.CalendarKindComposite {
			cal.IcalURL = nil // composites never carry a feed URL
		} else if icalURL := r.FormValue("ical_url"); icalURL != "" {
			cal.IcalURL = &icalURL
		}
		if err := store.UpdateCalendar(ctx, d.Pool, &cal); err != nil {
			FragmentError(w, r, "failed to update calendar: "+err.Error())
			return
		}
		if existing.Kind == store.CalendarKindComposite {
			if err := store.ReplaceCalendarMembers(ctx, d.Pool, id, r.Form["members"]); err != nil {
				FragmentError(w, r, "failed to update members: "+err.Error())
				return
			}
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) CalendarsDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteCalendar(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete calendar")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) CalendarsConvertToManual() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		cal, err := store.GetCalendarByID(ctx, d.Pool, id)
		if err != nil || cal.IcalURL == nil || *cal.IcalURL == "" {
			FragmentError(w, r, "calendar not found or has no feed URL")
			return
		}
		// Final reconcile against the feed, then the rows become manual data.
		res, err := jobs.SyncFeedCalendar(ctx, d.Pool, cal)
		if err != nil {
			FragmentError(w, r, "failed to fetch feed: "+err.Error())
			return
		}
		cal.IcalURL = nil
		if err := store.UpdateCalendar(ctx, d.Pool, cal); err != nil {
			FragmentError(w, r, "failed to clear feed URL")
			return
		}
		store.LogAction(ctx, d.Pool, nil, "calendar.converted_to_manual", id, map[string]any{
			"imported": res.Inserted,
		})
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// CalendarsSyncNow handles POST /admin/fragments/calendars/{id}/sync-now —
// runs an out-of-band feed sync for one calendar and redirects back.
func (d Deps) CalendarsSyncNow() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		cal, err := store.GetCalendarByID(ctx, d.Pool, id)
		if err != nil || cal.IcalURL == nil || *cal.IcalURL == "" {
			FragmentError(w, r, "calendar not found or has no feed URL")
			return
		}
		if _, err := jobs.SyncFeedCalendar(ctx, d.Pool, cal); err != nil {
			FragmentError(w, r, "sync failed: "+err.Error())
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// CalendarsAgenda handles GET /admin/fragments/calendars/{calId}/agenda —
// the HTMX-swappable 4-week agenda pane with prev/next paging.
func (d Deps) CalendarsAgenda() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		start := parseAgendaStart(r.URL.Query().Get("start"))

		cal, err := store.GetCalendarByID(ctx, d.Pool, calID)
		if err != nil {
			FragmentError(w, r, "calendar not found")
			return
		}
		loc := calendarLocation(ctx, d.Pool, calID)
		occs, err := loadCalendarOccurrences(ctx, d.Pool, cal, start, start.AddDate(0, 0, 28))
		if err != nil {
			FragmentError(w, r, "failed to load events")
			return
		}
		colors, _ := store.CalendarColorMap(ctx, d.Pool)
		days := admintempl.GroupByDay(occs, loc)

		w.Header().Set("Content-Type", "text/html")
		admintempl.AgendaSection(calID, start, days, colors, calendarIsReadOnly(cal)).Render(ctx, w)
	}
}

// CalendarsSaveReminders handles POST /admin/fragments/calendars/{id}/reminders.
// Form fields: channel_id_N / lead_minutes_N row pairs (N = 0,1,2,…), plus a
// channel select for adding rows. Empty channels are dropped.
func (d Deps) CalendarsSaveReminders() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		if _, err := store.GetCalendarByID(ctx, d.Pool, id); err != nil {
			FragmentError(w, r, "calendar not found")
			return
		}

		var reminders []store.CalendarReminder
		for i := 0; ; i++ {
			channelID := r.FormValue(fmt.Sprintf("channel_id_%d", i))
			if channelID == "" {
				if i > 0 {
					break // rows are contiguous; first empty index ends the set
				}
				continue
			}
			lead := 60
			if v, err := strconv.Atoi(r.FormValue(fmt.Sprintf("lead_minutes_%d", i))); err == nil && v > 0 {
				lead = v
			}
			reminders = append(reminders, store.CalendarReminder{
				CalendarID:  id,
				ChannelID:   channelID,
				LeadMinutes: lead,
			})
		}

		if err := store.SetCalendarReminders(ctx, d.Pool, id, reminders); err != nil {
			FragmentError(w, r, "failed to save reminders: "+err.Error())
			return
		}
		store.LogAction(ctx, d.Pool, nil, "calendar.reminders_saved", id, map[string]any{
			"count": len(reminders),
		})
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// monthWindow returns [first of month, first of next month) in loc.
func monthWindow(year, month int, loc *time.Location) (time.Time, time.Time) {
	from := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 1, 0)
}

// loadCalendarOccurrences returns the display occurrences for a calendar within
// [from, to): merged member occurrences for composites/personal, expanded stored
// events for standard calendars — manual ones and syndicated ones alike (feed
// calendars' rows are kept fresh by the sync job).
func loadCalendarOccurrences(ctx context.Context, pool *pgxpool.Pool, cal *store.Calendar, from, to time.Time) ([]store.EventOccurrence, error) {
	if cal.Kind == store.CalendarKindComposite || cal.Kind == store.CalendarKindPersonal {
		return store.ListCompositeOccurrences(ctx, pool, cal.ID, from, to)
	}
	return store.ListCalendarOccurrences(ctx, pool, cal.ID, from, to)
}

// calendarIsReadOnly mirrors templ's calReadOnly for handler-side decisions.
func calendarIsReadOnly(cal *store.Calendar) bool {
	if cal == nil {
		return false
	}
	if cal.IcalURL != nil && *cal.IcalURL != "" {
		return true
	}
	return cal.Kind == store.CalendarKindComposite || cal.Kind == store.CalendarKindPersonal
}

func (d Deps) CalendarsGrid() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		year, _ := strconv.Atoi(r.URL.Query().Get("year"))
		month, _ := strconv.Atoi(r.URL.Query().Get("month"))
		now := time.Now()
		if year == 0 {
			year = now.Year()
		}
		if month < 1 || month > 12 {
			month = int(now.Month())
		}
		cal, err := store.GetCalendarByID(ctx, d.Pool, calID)
		if err != nil {
			FragmentError(w, r, "calendar not found")
			return
		}
		calLoc := calendarLocation(ctx, d.Pool, calID)
		from, to := monthWindow(year, month, calLoc)
		events, err := loadCalendarOccurrences(ctx, d.Pool, cal, from, to)
		if err != nil {
			FragmentError(w, r, "failed to load events")
			return
		}
		grid := buildCalGrid(year, month)
		w.Header().Set("Content-Type", "text/html")
		admintempl.CalendarGridSection(calID, year, month, grid, events, calLoc, calendarIsReadOnly(cal)).Render(ctx, w)
	}
}

func (d Deps) CalendarsEventForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		var ev *store.CalendarEvent
		if dateVal := r.URL.Query().Get("date"); dateVal != "" {
			calLoc := calendarLocation(ctx, d.Pool, calID)
			if t, err := time.ParseInLocation("2006-01-02", dateVal, calLoc); err == nil {
				ev = &store.CalendarEvent{StartTime: t}
			}
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.EventForm(admintempl.EventFormView{CalendarID: calID, Ev: ev, Recurrence: store.RecurrenceState{Freq: "none", End: "never"}}).Render(r.Context(), w)
	}
}

func (d Deps) CalendarsEventEditForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		eventID := r.PathValue("eventId")
		calLoc := calendarLocation(ctx, d.Pool, calID)

		ev, err := store.GetCalendarEventByID(ctx, d.Pool, eventID)
		if err != nil {
			FragmentError(w, r, "event not found")
			return
		}

		view := admintempl.EventFormView{
			CalendarID: calID,
			EventID:    eventID,
			Ev:         ev,
			Recurrence: store.ParseRRULE(ev.RRule, ev.StartTime),
		}

		if ev.RRule != "" {
			view.IsRecurring = true
			// A series chip always passes ?occurrence=; a template-level open
			// (no param) edits the whole series.
			if occStr := r.URL.Query().Get("occurrence"); occStr != "" {
				view.Occurrence = occStr
				occ, perr := time.Parse(time.RFC3339, occStr)
				if perr != nil {
					FragmentError(w, r, "invalid occurrence")
					return
				}
				// Prefill effective values: override fields if edited, else the
				// template's fields applied to this occurrence's slot.
				prefill := *ev
				prefill.StartTime = occ
				if ev.EndTime != nil {
					dur := ev.EndTime.Sub(ev.StartTime)
					end := occ.Add(dur)
					prefill.EndTime = &end
				}
				if ov, oerr := store.GetEventOverride(ctx, d.Pool, eventID, occ); oerr == nil && ov != nil {
					applyOverrideToEvent(&prefill, *ov)
				}
				prefill.StartTime = prefill.StartTime.In(calLoc)
				if prefill.EndTime != nil {
					local := prefill.EndTime.In(calLoc)
					prefill.EndTime = &local
				}
				view.Ev = &prefill
			}
		} else {
			ev.StartTime = ev.StartTime.In(calLoc)
			if ev.EndTime != nil {
				local := ev.EndTime.In(calLoc)
				ev.EndTime = &local
			}
		}

		w.Header().Set("Content-Type", "text/html")
		admintempl.EventForm(view).Render(ctx, w)
	}
}

// applyOverrideToEvent copies an override's full-replacement fields onto an event.
func applyOverrideToEvent(ev *store.CalendarEvent, ov store.EventOverride) {
	if ov.Title != nil {
		ev.Title = *ov.Title
	}
	ev.Description = ov.Description
	ev.Location = ov.Location
	ev.URL = ov.URL
	if ov.StartTime != nil {
		ev.StartTime = *ov.StartTime
	}
	ev.EndTime = ov.EndTime
	if ov.AllDay != nil {
		ev.AllDay = *ov.AllDay
	}
}

func (d Deps) CalendarsEventCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		calLoc := calendarLocation(ctx, d.Pool, calID)
		evt, errMsg := parseEventForm(r, calLoc)
		if errMsg != "" {
			w.Header().Set("Content-Type", "text/html")
			admintempl.EventForm(eventFormViewFromForm(r, calID, "", errMsg)).Render(ctx, w)
			return
		}
		evt.CalendarID = calID
		if err := store.CreateCalendarEvent(ctx, d.Pool, evt); err != nil {
			FragmentError(w, r, "failed to create event: "+err.Error())
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+calID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) CalendarsEventUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		eventID := r.PathValue("eventId")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		calLoc := calendarLocation(ctx, d.Pool, calID)
		evt, errMsg := parseEventForm(r, calLoc)
		if errMsg != "" {
			w.Header().Set("Content-Type", "text/html")
			admintempl.EventForm(eventFormViewFromForm(r, calID, eventID, errMsg)).Render(ctx, w)
			return
		}
		scope, err := store.ParseEventScope(r.FormValue("scope"))
		if err != nil {
			FragmentError(w, r, "invalid scope")
			return
		}
		var occurrence time.Time
		if occStr := r.FormValue("occurrence"); occStr != "" {
			occurrence, err = time.Parse(time.RFC3339, occStr)
			if err != nil {
				FragmentError(w, r, "invalid occurrence")
				return
			}
		}
		if err := store.ApplyEventEdit(ctx, d.Pool, eventID, occurrence, scope, evt); err != nil {
			FragmentError(w, r, "failed to update event: "+err.Error())
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+calID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) CalendarsEventDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		eventID := r.PathValue("eventId")
		scope, err := store.ParseEventScope(r.URL.Query().Get("scope"))
		if err != nil {
			FragmentError(w, r, "invalid scope")
			return
		}
		var occurrence time.Time
		if occStr := r.URL.Query().Get("occurrence"); occStr != "" {
			occurrence, err = time.Parse(time.RFC3339, occStr)
			if err != nil {
				FragmentError(w, r, "invalid occurrence")
				return
			}
		}
		if err := store.DeleteEventWithScope(ctx, d.Pool, eventID, occurrence, scope); err != nil {
			FragmentError(w, r, "failed to delete event")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+r.PathValue("calId"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func loadCalendarsPageData(ctx context.Context, pool *pgxpool.Pool, calID string, year, month int) ([]store.Calendar, *store.Calendar, []store.EventOccurrence, int, int, []admintempl.CalGridCell, map[string][]string, error) {
	now := time.Now()
	if year == 0 {
		year = now.Year()
	}
	if month < 1 || month > 12 {
		month = int(now.Month())
	}
	calendars, err := store.ListCalendars(ctx, pool)
	if err != nil {
		return nil, nil, nil, year, month, nil, nil, err
	}
	// Pre-check member IDs for every composite so the edit dialogs can render them.
	membersByCal := map[string][]string{}
	for _, cal := range calendars {
		if cal.Kind != store.CalendarKindComposite {
			continue
		}
		members, err := store.ListCalendarMembers(ctx, pool, cal.ID)
		if err != nil {
			return nil, nil, nil, year, month, nil, nil, err
		}
		ids := make([]string, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.ID)
		}
		membersByCal[cal.ID] = ids
	}
	if calID == "" {
		return calendars, nil, nil, year, month, nil, membersByCal, nil
	}
	var selectedCal *store.Calendar
	for i := range calendars {
		if calendars[i].ID == calID {
			selectedCal = &calendars[i]
			break
		}
	}
	if selectedCal == nil {
		return calendars, nil, nil, year, month, nil, membersByCal, nil
	}
	calLoc := calendarLocation(ctx, pool, selectedCal.ID)
	from, to := monthWindow(year, month, calLoc)
	events, err := loadCalendarOccurrences(ctx, pool, selectedCal, from, to)
	if err != nil {
		return nil, nil, nil, year, month, nil, nil, err
	}
	grid := buildCalGrid(year, month)
	return calendars, selectedCal, events, year, month, grid, membersByCal, nil
}

func buildCalGrid(year, month int) []admintempl.CalGridCell {
	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	startWeekday := int(firstDay.Weekday())
	cells := make([]admintempl.CalGridCell, 0, startWeekday+daysInMonth)
	for i := 0; i < startWeekday; i++ {
		cells = append(cells, admintempl.CalGridCell{})
	}
	for d := 1; d <= daysInMonth; d++ {
		cells = append(cells, admintempl.CalGridCell{
			Day:    d,
			DateID: fmt.Sprintf("%04d-%02d-%02d", year, month, d),
		})
	}
	return cells
}

func calendarLocation(ctx context.Context, pool *pgxpool.Pool, calID string) *time.Location {
	cal, err := store.GetCalendarByID(ctx, pool, calID)
	if err != nil || cal == nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(cal.Timezone)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

// recurrenceStateFromForm collects the recurrence editor's fields.
func recurrenceStateFromForm(r *http.Request) store.RecurrenceState {
	intVal := func(name string) int {
		v, _ := strconv.Atoi(r.FormValue(name))
		return v
	}
	return store.RecurrenceState{
		Freq:        r.FormValue("repeat"),
		Interval:    intVal("interval"),
		ByDay:       r.Form["byday"],
		MonthlyMode: r.FormValue("monthly_mode"),
		ByMonthDay:  intVal("bymonthday"),
		Nth:         intVal("nth"),
		NthWeekday:  r.FormValue("nth_weekday"),
		End:         r.FormValue("end"),
		Until:       r.FormValue("until"),
		Count:       intVal("count"),
	}
}

func buildPartialEventFromForm(r *http.Request, loc *time.Location) *store.CalendarEvent {
	evt := &store.CalendarEvent{
		Title:  r.FormValue("title"),
		AllDay: r.FormValue("all_day") == "true",
	}
	if s, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("start_time"), loc); err == nil {
		evt.StartTime = s
	}
	if s, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("end_time"), loc); err == nil {
		evt.EndTime = &s
	}
	if v := r.FormValue("description"); v != "" {
		evt.Description = &v
	}
	if v := r.FormValue("location"); v != "" {
		evt.Location = &v
	}
	return evt
}

// eventFormViewFromForm rebuilds the form view after a validation error so the
// user's input (including recurrence selections) is preserved.
func eventFormViewFromForm(r *http.Request, calID, eventID, errMsg string) admintempl.EventFormView {
	ev := buildPartialEventFromForm(r, time.UTC)
	return admintempl.EventFormView{
		CalendarID: calID,
		EventID:    eventID,
		Occurrence: r.FormValue("occurrence"),
		// The occurrence hidden input is only present on series-occurrence edits.
		IsRecurring: r.FormValue("occurrence") != "",
		Ev:          ev,
		Recurrence:  recurrenceStateFromForm(r),
		ErrMsg:      errMsg,
	}
}

func parseEventForm(r *http.Request, loc *time.Location) (*store.CalendarEvent, string) {
	title := r.FormValue("title")
	if title == "" {
		return nil, "Title is required"
	}
	startStr := r.FormValue("start_time")
	if startStr == "" {
		return nil, "Start time is required"
	}
	startTime, err := time.ParseInLocation("2006-01-02T15:04", startStr, loc)
	if err != nil {
		return nil, "Invalid start time format"
	}
	evt := &store.CalendarEvent{
		Title:     title,
		AllDay:    r.FormValue("all_day") == "true",
		StartTime: startTime,
	}
	if endStr := r.FormValue("end_time"); endStr != "" {
		endTime, err := time.ParseInLocation("2006-01-02T15:04", endStr, loc)
		if err == nil {
			evt.EndTime = &endTime
		}
	}
	if d := r.FormValue("description"); d != "" {
		evt.Description = &d
	}
	if locStr := r.FormValue("location"); locStr != "" {
		evt.Location = &locStr
	}
	if u := r.FormValue("url"); u != "" {
		evt.URL = &u
	}
	// Recurrence: built from the editor state, anchored at the start time.
	if rule, rerr := store.BuildRRULE(recurrenceStateFromForm(r), startTime); rerr != nil {
		return nil, "Invalid recurrence: " + rerr.Error()
	} else {
		evt.RRule = rule
	}
	return evt, ""
}
