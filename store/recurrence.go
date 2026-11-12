package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// EventScope identifies which part of a recurring series an edit or delete targets.
type EventScope string

const (
	// ScopeOne applies to a single occurrence (stored as an override).
	ScopeOne EventScope = "one"
	// ScopeFollowing applies to an occurrence and everything after it
	// (implemented by splitting the series).
	ScopeFollowing EventScope = "following"
	// ScopeAll applies to the entire series template (clears overrides).
	ScopeAll EventScope = "all"
)

// ParseEventScope validates a scope string from a form or modal. The empty
// string maps to ScopeOne — the least destructive default.
func ParseEventScope(s string) (EventScope, error) {
	switch s {
	case "", string(ScopeOne):
		return ScopeOne, nil
	case string(ScopeFollowing):
		return ScopeFollowing, nil
	case string(ScopeAll):
		return ScopeAll, nil
	default:
		return "", fmt.Errorf("invalid scope %q", s)
	}
}

// EventOverride is a per-occurrence modification of a series: either a full
// field replacement (edited occurrence) or a skip (deleted = true, EXDATE).
// OccurrenceStart addresses the series occurrence being overridden.
type EventOverride struct {
	ID              string
	SeriesID        string
	OccurrenceStart time.Time
	Title           *string
	Description     *string
	Location        *string
	URL             *string
	StartTime       *time.Time
	EndTime         *time.Time
	AllDay          *bool
	Deleted         bool
	CreatedAt       time.Time
}

// EventOccurrence is one materialized occurrence of an event: a standalone
// event, or a single instance generated from a series with any override applied.
type EventOccurrence struct {
	// ID is the standalone event's ID or the series template's ID. Combined
	// with OccurrenceStart it addresses this occurrence for edits and deletes.
	ID string
	// CalendarID is the owning calendar — occurrences merge views and the
	// dashboard need it to attribute events.
	CalendarID string
	// OccurrenceStart is the series-computed start of this occurrence
	// (RECURRENCE-ID semantics). Equal to StartTime for standalone events.
	OccurrenceStart time.Time
	StartTime       time.Time
	EndTime         *time.Time
	Title           string
	Description     *string
	Location        *string
	URL             *string
	AllDay          bool
	// IsRecurring reports whether this occurrence belongs to a series (drives
	// the edit/delete scope prompt).
	IsRecurring bool
	// IsOverride reports whether this occurrence was individually edited.
	IsOverride bool
}

// expandKey normalizes an instant for override addressing. Second precision —
// all occurrence starts originate from minute-precision form input.
func expandKey(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// ExpandEvents materializes the occurrences of events within [from, to).
// Standalone events (no RRule) yield one occurrence when their start falls in
// the window. Series are expanded in loc — the calendar's timezone — so local
// start times hold across DST changes. Overrides are applied per occurrence:
// deleted occurrences are skipped, edited occurrences use the override's fields.
// Pure function; results are sorted by effective start time.
func ExpandEvents(events []CalendarEvent, overrides map[string][]EventOverride, from, to time.Time, loc *time.Location) []EventOccurrence {
	return expandEvents(events, overrides, from, to, loc, true)
}

// expandEvents is ExpandEvents with control over standalone clipping. Callers
// that re-filter by window afterwards (composite views) pass clipStandalone=false
// so finite standalone events pass through unclipped; only series expansion and
// the callers' own filtering bound the result.
func expandEvents(events []CalendarEvent, overrides map[string][]EventOverride, from, to time.Time, loc *time.Location, clipStandalone bool) []EventOccurrence {
	if loc == nil {
		loc = time.UTC
	}
	ovBySeries := make(map[string]map[string]EventOverride, len(overrides))
	for seriesID, list := range overrides {
		m := make(map[string]EventOverride, len(list))
		for _, o := range list {
			m[expandKey(o.OccurrenceStart)] = o
		}
		ovBySeries[seriesID] = m
	}

	var out []EventOccurrence
	for _, ev := range events {
		if ev.RRule == "" {
			if clipStandalone && (ev.StartTime.Before(from) || !ev.StartTime.Before(to)) {
				continue
			}
			out = append(out, standaloneOccurrence(ev))
			continue
		}
		for _, occ := range expandSeries(ev, loc, from, to) {
			ov, ok := ovBySeries[ev.ID][expandKey(occ)]
			if !ok {
				out = append(out, seriesOccurrence(ev, occ))
				continue
			}
			if ov.Deleted {
				continue
			}
			out = append(out, overrideOccurrence(ev, ov))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartTime.Before(out[j].StartTime) })
	return out
}

// expandSeries generates the series-computed start instants of a recurring
// event within [from, to). Expansion happens in the calendar's local timezone.
func expandSeries(ev CalendarEvent, loc *time.Location, from, to time.Time) []time.Time {
	opt, err := rrule.StrToROption(ev.RRule)
	if err != nil {
		// Unparseable rule — degrade to a standalone event rather than hiding data.
		if !ev.StartTime.Before(from) && ev.StartTime.Before(to) {
			return []time.Time{ev.StartTime}
		}
		return nil
	}
	dtstart := ev.StartTime.In(loc)
	opt.Dtstart = dtstart
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil
	}
	occurrences := r.Between(from.In(loc), to.In(loc), true)
	out := make([]time.Time, 0, len(occurrences))
	for _, occ := range occurrences {
		out = append(out, occ.UTC())
	}
	return out
}

func standaloneOccurrence(ev CalendarEvent) EventOccurrence {
	return EventOccurrence{
		ID:              ev.ID,
		CalendarID:      ev.CalendarID,
		OccurrenceStart: ev.StartTime,
		StartTime:       ev.StartTime,
		EndTime:         ev.EndTime,
		Title:           ev.Title,
		Description:     ev.Description,
		Location:        ev.Location,
		URL:             ev.URL,
		AllDay:          ev.AllDay,
		IsRecurring:     ev.RRule != "",
	}
}

// seriesOccurrence materializes an unedited occurrence at the series-computed slot.
func seriesOccurrence(ev CalendarEvent, slot time.Time) EventOccurrence {
	occ := standaloneOccurrence(ev)
	occ.OccurrenceStart = slot
	occ.StartTime = slot
	occ.IsRecurring = true
	return occ
}

func overrideOccurrence(ev CalendarEvent, ov EventOverride) EventOccurrence {
	occ := standaloneOccurrence(ev)
	occ.OccurrenceStart = ov.OccurrenceStart
	occ.IsRecurring = true
	occ.IsOverride = true
	if ov.Title != nil {
		occ.Title = *ov.Title
	}
	occ.Description = ov.Description
	occ.Location = ov.Location
	occ.URL = ov.URL
	if ov.StartTime != nil {
		occ.StartTime = *ov.StartTime
	}
	occ.EndTime = ov.EndTime
	if ov.AllDay != nil {
		occ.AllDay = *ov.AllDay
	}
	return occ
}

// ---------------------------------------------------------------------------
// Form <-> RRULE translation, shared by the admin UI and Slack modals.
// ---------------------------------------------------------------------------

// RecurrenceState is the structured, UI-friendly representation of an RRULE.
type RecurrenceState struct {
	Freq        string   // none | daily | weekly | monthly | yearly
	Interval    int      // every N
	ByDay       []string // weekly: selected weekdays (MO,TU,WE,TH,FR,SA,SU)
	MonthlyMode string   // monthly: day | nth
	ByMonthDay  int      // monthly day-of-month (1..31)
	Nth         int      // monthly nth-weekday ordinal (1..5, -1 = last)
	NthWeekday  string   // monthly nth-weekday day (MO..SU)
	End         string   // never | until | count
	Until       string   // end mode until: YYYY-MM-DD
	Count       int      // end mode count: N occurrences
}

// ParseRecurrenceState converts a RecurrenceState into an RRULE string.
// dtstart seeds defaults (weekly falls back to its own weekday; monthly
// day-of-month falls back to dtstart's day). Returns "" for freq=none.
func BuildRRULE(st RecurrenceState, dtstart time.Time) (string, error) {
	switch st.Freq {
	case "", "none":
		return "", nil
	case "daily", "weekly", "monthly", "yearly":
	default:
		return "", fmt.Errorf("invalid frequency %q", st.Freq)
	}

	opt := &rrule.ROption{Interval: st.Interval}
	if opt.Interval < 1 {
		opt.Interval = 1
	}
	switch st.Freq {
	case "daily":
		opt.Freq = rrule.DAILY
	case "weekly":
		opt.Freq = rrule.WEEKLY
		if len(st.ByDay) > 0 {
			wdays, err := parseWeekdays(st.ByDay)
			if err != nil {
				return "", err
			}
			opt.Byweekday = wdays
		} else {
			// No weekdays picked: repeat on dtstart's weekday.
			opt.Byweekday = []rrule.Weekday{rruleWeekday(dtstart.Weekday())}
		}
	case "monthly":
		opt.Freq = rrule.MONTHLY
		if st.MonthlyMode == "nth" && st.NthWeekday != "" {
			wdays, err := parseWeekdays([]string{st.NthWeekday})
			if err != nil {
				return "", err
			}
			nth := st.Nth
			if nth == 0 {
				nth = 1
			}
			opt.Byweekday = []rrule.Weekday{wdays[0].Nth(nth)}
		} else {
			day := st.ByMonthDay
			if day == 0 {
				day = dtstart.Day()
			}
			if day < 1 || day > 31 {
				return "", fmt.Errorf("day of month must be 1-31")
			}
			opt.Bymonthday = []int{day}
		}
	case "yearly":
		opt.Freq = rrule.YEARLY
	}

	switch st.End {
	case "", "never":
	case "until":
		if st.Until == "" {
			return "", fmt.Errorf("end date is required")
		}
		d, err := time.Parse("2006-01-02", st.Until)
		if err != nil {
			return "", fmt.Errorf("invalid end date")
		}
		// End of that day, inclusive of all times on it.
		opt.Until = d.Add(24*time.Hour - time.Second)
	case "count":
		if st.Count < 1 {
			return "", fmt.Errorf("occurrence count must be at least 1")
		}
		opt.Count = st.Count
	default:
		return "", fmt.Errorf("invalid end mode %q", st.End)
	}
	return opt.RRuleString(), nil
}

// ParseRRULE best-effort parses an RRULE string back into form state for
// prefilling. Unknown or unsupported constructs collapse to freq=none.
func ParseRRULE(rule string, dtstart time.Time) RecurrenceState {
	st := RecurrenceState{Freq: "none", End: "never"}
	if rule == "" {
		return st
	}
	opt, err := rrule.StrToROption(rule)
	if err != nil {
		return st
	}
	st.Interval = opt.Interval
	if st.Interval < 1 {
		st.Interval = 1
	}
	if opt.Count > 0 {
		st.End = "count"
		st.Count = opt.Count
	} else if !opt.Until.IsZero() {
		st.End = "until"
		st.Until = opt.Until.Format("2006-01-02")
	}
	switch opt.Freq {
	case rrule.DAILY:
		st.Freq = "daily"
	case rrule.WEEKLY:
		st.Freq = "weekly"
		for _, wd := range opt.Byweekday {
			st.ByDay = append(st.ByDay, weekdayCodeByIndex(wd.Day()))
		}
	case rrule.MONTHLY:
		st.Freq = "monthly"
		if len(opt.Byweekday) == 1 && opt.Byweekday[0].N() != 0 {
			st.MonthlyMode = "nth"
			st.Nth = opt.Byweekday[0].N()
			st.NthWeekday = weekdayCodeByIndex(opt.Byweekday[0].Day())
		} else if len(opt.Bymonthday) == 1 {
			st.MonthlyMode = "day"
			st.ByMonthDay = opt.Bymonthday[0]
		} else {
			st.MonthlyMode = "day"
			st.ByMonthDay = dtstart.Day()
		}
	case rrule.YEARLY:
		st.Freq = "yearly"
	}
	return st
}

var weekdayByName = map[string]rrule.Weekday{
	"MO": rrule.MO, "TU": rrule.TU, "WE": rrule.WE, "TH": rrule.TH,
	"FR": rrule.FR, "SA": rrule.SA, "SU": rrule.SU,
}

func parseWeekdays(codes []string) ([]rrule.Weekday, error) {
	out := make([]rrule.Weekday, 0, len(codes))
	for _, c := range codes {
		wd, ok := weekdayByName[strings.ToUpper(c)]
		if !ok {
			return nil, fmt.Errorf("invalid weekday %q", c)
		}
		out = append(out, wd)
	}
	return out, nil
}

func rruleWeekday(d time.Weekday) rrule.Weekday {
	return weekdayByName[weekdayCode(d)]
}

func weekdayCode(d time.Weekday) string {
	return [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}[d]
}

// weekdayCodeByIndex maps an rrule day index (0=MO … 6=SU) to its two-letter code.
func weekdayCodeByIndex(i int) string {
	return [...]string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}[i]
}
