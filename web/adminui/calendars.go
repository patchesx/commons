package adminui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/api"
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
		cals, sel, events, y, mo, grid, err := loadCalendarsPageData(ctx, d.Pool, calID, year, month)
		if err != nil {
			http.Error(w, "failed to load calendars", http.StatusInternalServerError)
			return
		}
		admintempl.CalendarsPage(cals, sel, y, mo, grid, events).Render(ctx, w)
	}
}

func (d Deps) CalendarsCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		cal := store.Calendar{
			Name:     r.FormValue("name"),
			Slug:     r.FormValue("slug"),
			Timezone: r.FormValue("timezone"),
		}
		if desc := r.FormValue("description"); desc != "" {
			cal.Description = &desc
		}
		if icalURL := r.FormValue("ical_url"); icalURL != "" {
			cal.IcalURL = &icalURL
		}
		if v, err := strconv.Atoi(r.FormValue("display_order")); err == nil && v >= 0 {
			cal.DisplayOrder = v
		}
		if cal.Name == "" || cal.Slug == "" {
			FragmentError(w, r, "name and slug required")
			return
		}
		if cal.Timezone == "" {
			cal.Timezone = util.DefaultTimezone(ctx, d.Pool, d.EncKey)
		}
		if err := store.CreateCalendar(ctx, d.Pool, &cal); err != nil {
			FragmentError(w, r, "failed to create calendar: "+err.Error())
			return
		}
		importedCount := 0
		if cal.IcalURL != nil {
			events, err := api.FetchICSEvents(*cal.IcalURL)
			if err == nil {
				importedCount, _ = store.BulkInsertCalendarEvents(ctx, d.Pool, cal.ID, events)
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
		cal := store.Calendar{
			ID:       id,
			Name:     r.FormValue("name"),
			Slug:     r.FormValue("slug"),
			Timezone: r.FormValue("timezone"),
		}
		if desc := r.FormValue("description"); desc != "" {
			cal.Description = &desc
		}
		if icalURL := r.FormValue("ical_url"); icalURL != "" {
			cal.IcalURL = &icalURL
		}
		if v, err := strconv.Atoi(r.FormValue("display_order")); err == nil && v >= 0 {
			cal.DisplayOrder = v
		}
		if cal.Timezone == "" {
			cal.Timezone = util.DefaultTimezone(ctx, d.Pool, d.EncKey)
		}
		if err := store.UpdateCalendar(ctx, d.Pool, &cal); err != nil {
			FragmentError(w, r, "failed to update calendar: "+err.Error())
			return
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
		events, err := api.FetchICSEvents(*cal.IcalURL)
		if err != nil {
			FragmentError(w, r, "failed to fetch feed: "+err.Error())
			return
		}
		imported, err := store.BulkInsertCalendarEvents(ctx, d.Pool, id, events)
		if err != nil {
			FragmentError(w, r, "failed to import events")
			return
		}
		cal.IcalURL = nil
		if err := store.UpdateCalendar(ctx, d.Pool, cal); err != nil {
			FragmentError(w, r, "failed to clear feed URL")
			return
		}
		store.LogAction(ctx, d.Pool, nil, "calendar.converted_to_manual", id, map[string]any{
			"imported": imported,
		})
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+id)
		w.WriteHeader(http.StatusNoContent)
	}
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
		events, err := store.ListCalendarEvents(ctx, d.Pool, calID)
		if err != nil {
			FragmentError(w, r, "failed to load events")
			return
		}
		grid := buildCalGrid(year, month)
		calLoc := calendarLocation(ctx, d.Pool, calID)
		cal, _ := store.GetCalendarByID(ctx, d.Pool, calID)
		readOnly := cal != nil && cal.IcalURL != nil && *cal.IcalURL != ""
		w.Header().Set("Content-Type", "text/html")
		admintempl.CalendarGridSection(calID, year, month, grid, events, calLoc, readOnly).Render(ctx, w)
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
		admintempl.EventForm(calID, ev, "").Render(r.Context(), w)
	}
}

func (d Deps) CalendarsEventEditForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		calID := r.PathValue("calId")
		eventID := r.PathValue("eventId")
		calLoc := calendarLocation(ctx, d.Pool, calID)
		events, err := store.ListCalendarEvents(ctx, d.Pool, calID)
		if err != nil {
			FragmentError(w, r, "failed to load events")
			return
		}
		for _, e := range events {
			if e.ID == eventID {
				e.StartTime = e.StartTime.In(calLoc)
				if e.EndTime != nil {
					local := e.EndTime.In(calLoc)
					e.EndTime = &local
				}
				w.Header().Set("Content-Type", "text/html")
				admintempl.EventForm(calID, &e, "").Render(ctx, w)
				return
			}
		}
		FragmentError(w, r, "event not found")
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
			admintempl.EventForm(calID, buildPartialEventFromForm(r, calLoc), errMsg).Render(ctx, w)
			return
		}
		evt.CalendarID = calID
		if err := store.CreateCalendarEvent(ctx, d.Pool, evt); err != nil {
			FragmentError(w, r, "failed to create event")
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
			partial := buildPartialEventFromForm(r, calLoc)
			partial.ID = eventID
			admintempl.EventForm(calID, partial, errMsg).Render(ctx, w)
			return
		}
		evt.ID = eventID
		if err := store.UpdateCalendarEvent(ctx, d.Pool, evt); err != nil {
			FragmentError(w, r, "failed to update event")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+calID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) CalendarsEventDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteCalendarEvent(r.Context(), d.Pool, r.PathValue("eventId")); err != nil {
			FragmentError(w, r, "failed to delete event")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/calendars?cal="+r.PathValue("calId"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func loadCalendarsPageData(ctx context.Context, pool *pgxpool.Pool, calID string, year, month int) ([]store.Calendar, *store.Calendar, []store.CalendarEvent, int, int, []admintempl.CalGridCell, error) {
	now := time.Now()
	if year == 0 {
		year = now.Year()
	}
	if month < 1 || month > 12 {
		month = int(now.Month())
	}
	calendars, err := store.ListCalendars(ctx, pool)
	if err != nil {
		return nil, nil, nil, year, month, nil, err
	}
	if calID == "" {
		return calendars, nil, nil, year, month, nil, nil
	}
	var selectedCal *store.Calendar
	for i := range calendars {
		if calendars[i].ID == calID {
			selectedCal = &calendars[i]
			break
		}
	}
	if selectedCal == nil {
		return calendars, nil, nil, year, month, nil, nil
	}
	var events []store.CalendarEvent
	if selectedCal.IcalURL != nil && *selectedCal.IcalURL != "" {
		events, _ = api.FetchICSEvents(*selectedCal.IcalURL)
	} else {
		var err error
		events, err = store.ListCalendarEvents(ctx, pool, calID)
		if err != nil {
			return nil, nil, nil, year, month, nil, err
		}
	}
	grid := buildCalGrid(year, month)
	return calendars, selectedCal, events, year, month, grid, nil
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
	return evt, ""
}
