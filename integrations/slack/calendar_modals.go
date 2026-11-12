package slack

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// CalendarCreateEventModal returns a modal for creating a new calendar event,
// with recurrence options. calendarID is stored in PrivateMetadata.
func CalendarCreateEventModal(calendarID string) slacklib.ModalViewRequest {
	blks := calendarEventFormBlocks("", 0, 0, false, "", "", "", false, store.RecurrenceState{Freq: "none", End: "never"}, false)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Add Event", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Create Event", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDCalendarCreateEvent,
		PrivateMetadata: calendarID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// CalendarEventListModal returns a modal that lists upcoming occurrences of a
// calendar for selection. Replaces the create form in-place via UpdateViewContext.
func CalendarEventListModal(calendarID string, occs []store.EventOccurrence) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	if len(occs) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No events in the next 60 days._", false, false),
			nil, nil,
		))
	} else {
		for _, o := range occs {
			label := fmt.Sprintf("%s — %s", o.StartTime.Format("Mon, Jan 2, 2006 · 3:04 PM"), o.Title)
			if o.IsRecurring {
				label = "↻ " + label
			}
			editBtn := slacklib.NewButtonBlockElement(
				ActionCalendarEventEdit, occurrenceValue(o),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Edit", false, false),
			)
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, label, false, false),
				nil,
				slacklib.NewAccessory(editBtn),
			))
		}
	}

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Calendar Events", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID:      ModalIDCalendarEventList,
		PrivateMetadata: calendarID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// occurrenceValue packs the event ID and occurrence address into a button value.
// Occurrences of recurring series carry their slot as a unix timestamp.
func occurrenceValue(o store.EventOccurrence) string {
	if !o.IsRecurring {
		return o.ID + ":0"
	}
	return o.ID + ":" + strconv.FormatInt(o.OccurrenceStart.Unix(), 10)
}

// parseOccurrenceValue unpacks an eventID[:occurrenceUnix] button value.
// occurrence is zero for standalone events.
func parseOccurrenceValue(v string) (eventID string, occurrence time.Time) {
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return v, time.Time{}
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || unix == 0 {
		return parts[0], time.Time{}
	}
	return parts[0], time.Unix(unix, 0).UTC()
}

// CalendarEditEventModal returns a modal pre-populated with an event's effective
// values. Pass an occurrence (from the expanded list) to edit that instance of a
// recurring series; pass nil with a standalone event to edit it directly.
func CalendarEditEventModal(ev store.CalendarEvent, occ *store.EventOccurrence, calendarID string) slacklib.ModalViewRequest {
	title := ev.Title
	startUnix := ev.StartTime.Unix()
	endUnix := int64(0)
	hasEnd := ev.EndTime != nil
	if hasEnd {
		endUnix = ev.EndTime.Unix()
	}
	desc := DerefStrOr(ev.Description)
	loc := DerefStrOr(ev.Location)
	url := DerefStrOr(ev.URL)
	allDay := ev.AllDay
	occurrenceUnix := int64(0)

	if occ != nil {
		title = occ.Title
		startUnix = occ.StartTime.Unix()
		hasEnd = occ.EndTime != nil
		if hasEnd {
			endUnix = occ.EndTime.Unix()
		}
		desc = DerefStrOr(occ.Description)
		loc = DerefStrOr(occ.Location)
		url = DerefStrOr(occ.URL)
		allDay = occ.AllDay
		if occ.IsRecurring {
			occurrenceUnix = occ.OccurrenceStart.Unix()
		}
	}

	rec := store.ParseRRULE(ev.RRule, ev.StartTime)
	blks := calendarEventFormBlocks(title, startUnix, endUnix, hasEnd, desc, loc, url, allDay, rec, occ != nil && occ.IsRecurring)

	deleteBtn := slacklib.NewButtonBlockElement(
		ActionCalendarEventDelete, ev.ID+"|"+strconv.FormatInt(occurrenceUnix, 10),
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Delete Event", false, false),
	)
	deleteBtn.Style = "danger"
	blks = append(blks,
		slacklib.NewDividerBlock(),
		slacklib.NewActionBlock("ce_danger", deleteBtn),
	)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Edit Event", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Save Changes", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDCalendarEditEvent,
		PrivateMetadata: ev.ID + "|" + calendarID + "|" + strconv.FormatInt(occurrenceUnix, 10),
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

func DerefStrOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// recStateFromView extracts the recurrence editor's state from a modal
// submission. Missing optional blocks resolve to zero values.
func recStateFromView(sv map[string]map[string]slacklib.BlockAction) store.RecurrenceState {
	selVal := func(block, action string) string {
		if ba, ok := sv[block][action]; ok {
			return ba.SelectedOption.Value
		}
		return ""
	}
	intVal := func(block, action string) int {
		if ba, ok := sv[block][action]; ok && ba.Value != "" {
			n, _ := strconv.Atoi(ba.Value)
			return n
		}
		return 0
	}

	st := store.RecurrenceState{
		Freq:        selVal("ce_rec_freq", "ce_rec_freq_input"),
		Interval:    intVal("ce_rec_interval", "ce_rec_interval_input"),
		MonthlyMode: selVal("ce_rec_monthly", "ce_rec_monthly_input"),
		ByMonthDay:  intVal("ce_rec_monthday", "ce_rec_monthday_input"),
		NthWeekday:  selVal("ce_rec_nth_weekday", "ce_rec_nth_weekday_input"),
		End:         selVal("ce_rec_end", "ce_rec_end_input"),
		Count:       intVal("ce_rec_count", "ce_rec_count_input"),
	}
	if n, err := strconv.Atoi(selVal("ce_rec_nth_sel", "ce_rec_nth_input")); err == nil {
		st.Nth = n
	}
	if ba, ok := sv["ce_rec_byday"]["ce_rec_byday_input"]; ok {
		for _, o := range ba.SelectedOptions {
			st.ByDay = append(st.ByDay, o.Value)
		}
	}
	if ba, ok := sv["ce_rec_until"]["ce_rec_until_input"]; ok && ba.SelectedDate != "" {
		st.Until = ba.SelectedDate
	}
	return st
}

// MyCalendarModal returns a modal for choosing the member calendars of the user's
// personal composite calendar. selectedMemberIDs pre-checks options (edit flow).
// timezone is stashed in PrivateMetadata so a first-time creation uses the user's
// Slack timezone without a second API call on submission.
func MyCalendarModal(cals []store.Calendar, selectedMemberIDs map[string]bool, timezone string) slacklib.ModalViewRequest {
	var options []*slacklib.OptionBlockObject
	var initial []*slacklib.OptionBlockObject
	for _, c := range cals {
		opt := slacklib.NewOptionBlockObject(
			c.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, c.Name, false, false),
			nil,
		)
		options = append(options, opt)
		if selectedMemberIDs[c.ID] {
			initial = append(initial, opt)
		}
	}

	elem := &slacklib.CheckboxGroupsBlockElement{
		Type:           slacklib.METCheckboxGroups,
		ActionID:       "my_cal_members_input",
		Options:        options,
		InitialOptions: initial,
	}
	block := slacklib.NewInputBlock(
		"my_cal_members",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Calendars", false, false),
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Pick the calendars to merge into your My Calendar view", false, false),
		elem,
	)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "My Calendar", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Save", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDMyCalendar,
		PrivateMetadata: timezone,
		Blocks:          slacklib.Blocks{BlockSet: []slacklib.Block{block}},
	}
}

// calendarEventFormBlocks builds the shared input blocks used by both create and edit modals.
// Pass zero values for optional fields to leave them blank. hasEnd controls whether
// endUnix is set as the initial end-time value. rec prefills the recurrence editor;
// scopeSelect adds the edit-scope radio group (series occurrence edits only).
func calendarEventFormBlocks(title string, startUnix, endUnix int64, hasEnd bool, description, location, url string, allDay bool, rec store.RecurrenceState, scopeSelect bool) []slacklib.Block {
	// Title input.
	titleElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_title_input")
	if title != "" {
		titleElem.InitialValue = title
	}
	titleBlock := slacklib.NewInputBlock(
		"ce_title",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Title", false, false),
		nil,
		titleElem,
	)

	// Start date/time picker.
	startPicker := slacklib.NewDateTimePickerBlockElement("ce_start_picker")
	if startUnix != 0 {
		startPicker.InitialDateTime = startUnix
	}
	startBlock := slacklib.NewInputBlock(
		"ce_start",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Start Date/Time", false, false),
		nil,
		startPicker,
	)

	// End date/time picker (optional).
	endPicker := slacklib.NewDateTimePickerBlockElement("ce_end_picker")
	if hasEnd && endUnix != 0 {
		endPicker.InitialDateTime = endUnix
	}
	endBlock := slacklib.NewInputBlock(
		"ce_end",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "End Date/Time", false, false),
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Leave blank for no end time", false, false),
		endPicker,
	)
	endBlock.Optional = true

	// All-day checkbox.
	allDayOpt := slacklib.NewOptionBlockObject(
		"true",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "All-day event", false, false),
		nil,
	)
	allDayElem := &slacklib.CheckboxGroupsBlockElement{
		Type:     slacklib.METCheckboxGroups,
		ActionID: "ce_all_day_check",
		Options:  []*slacklib.OptionBlockObject{allDayOpt},
	}
	if allDay {
		allDayElem.InitialOptions = []*slacklib.OptionBlockObject{allDayOpt}
	}
	allDayBlock := slacklib.NewInputBlock(
		"ce_all_day",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Event Type", false, false),
		nil,
		allDayElem,
	)
	allDayBlock.Optional = true

	// Description (optional, multiline).
	descElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_description_input")
	descElem.Multiline = true
	if description != "" {
		descElem.InitialValue = description
	}
	descBlock := slacklib.NewInputBlock(
		"ce_description",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Description", false, false),
		nil,
		descElem,
	)
	descBlock.Optional = true

	// Location (optional).
	locElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_location_input")
	if location != "" {
		locElem.InitialValue = location
	}
	locBlock := slacklib.NewInputBlock(
		"ce_location",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Location", false, false),
		nil,
		locElem,
	)
	locBlock.Optional = true

	// URL (optional).
	urlElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_url_input")
	if url != "" {
		urlElem.InitialValue = url
	}
	urlBlock := slacklib.NewInputBlock(
		"ce_url",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "URL", false, false),
		nil,
		urlElem,
	)
	urlBlock.Optional = true

	blks := []slacklib.Block{titleBlock, startBlock, endBlock, allDayBlock, descBlock, locBlock, urlBlock}

	// Edit scope for series occurrences (before the recurrence editor, which only
	// applies to "all events" edits).
	if scopeSelect {
		blks = append(blks,
			slacklib.NewInputBlock(
				"ce_scope",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Applies to", false, false),
				nil,
				slacklib.NewRadioButtonsBlockElement("ce_scope_input", radioOptions(
					[]string{"one", "following", "all"},
					[]string{"This event", "This and following events", "All events (changes the repeat rule)"},
					"one",
				)...),
			),
		)
	}

	blks = append(blks, recurrenceBlocks(rec)...)
	return blks
}

// radioOptions builds radio button options with one selected.
func radioOptions(values, labels []string, selected string) []*slacklib.OptionBlockObject {
	opts := make([]*slacklib.OptionBlockObject, len(values))
	for i, v := range values {
		opts[i] = slacklib.NewOptionBlockObject(
			v,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, labels[i], false, false),
			nil,
		)
	}
	return opts
}

// recurrenceBlocks builds the recurrence editor: frequency, interval, weekdays
// (weekly), monthly mode, and end condition. All blocks are always rendered —
// Block Kit has no conditional visibility; users fill what applies.
func recurrenceBlocks(rec store.RecurrenceState) []slacklib.Block {
	freq := rec.Freq
	if freq == "" {
		freq = "none"
	}
	end := rec.End
	if end == "" {
		end = "never"
	}

	freqSel := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select frequency…", false, false),
		"ce_rec_freq_input",
		radioOptions(
			[]string{"none", "daily", "weekly", "monthly", "yearly"},
			[]string{"Does not repeat", "Daily", "Weekly", "Monthly", "Yearly"},
			freq,
		)...,
	)
	freqBlock := slacklib.NewInputBlock(
		"ce_rec_freq",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Repeat", false, false),
		nil,
		freqSel,
	)

	intervalElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_rec_interval_input")
	intervalElem.InitialValue = strconv.Itoa(recIntervalOr(rec.Interval))
	intervalBlock := slacklib.NewInputBlock(
		"ce_rec_interval",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Repeat every N (interval)", false, false),
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "e.g. 2 = every 2 days/weeks/months", false, false),
		intervalElem,
	)
	intervalBlock.Optional = true

	weekdayCodes := []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}
	weekdayLabels := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	var initialWeekdays []*slacklib.OptionBlockObject
	dayOpts := make([]*slacklib.OptionBlockObject, len(weekdayCodes))
	for i, code := range weekdayCodes {
		opt := slacklib.NewOptionBlockObject(
			code,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, weekdayLabels[i], false, false),
			nil,
		)
		dayOpts[i] = opt
		if containsStr(rec.ByDay, code) {
			initialWeekdays = append(initialWeekdays, opt)
		}
	}
	bydayElem := &slacklib.CheckboxGroupsBlockElement{
		Type:           slacklib.METCheckboxGroups,
		ActionID:       "ce_rec_byday_input",
		Options:        dayOpts,
		InitialOptions: initialWeekdays,
	}
	bydayBlock := slacklib.NewInputBlock(
		"ce_rec_byday",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "On days (weekly only)", false, false),
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Leave blank to repeat on the start day's weekday", false, false),
		bydayElem,
	)
	bydayBlock.Optional = true

	monthlyMode := rec.MonthlyMode
	if monthlyMode == "" {
		monthlyMode = "day"
	}
	monthlySel := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select mode…", false, false),
		"ce_rec_monthly_input",
		radioOptions([]string{"day", "nth"}, []string{"On a day of the month", "On the Nth weekday"}, monthlyMode)...,
	)
	monthlyBlock := slacklib.NewInputBlock(
		"ce_rec_monthly",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly mode (monthly only)", false, false),
		nil,
		monthlySel,
	)
	monthlyBlock.Optional = true

	monthDayElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_rec_monthday_input")
	monthDayElem.InitialValue = strconv.Itoa(rec.ByMonthDay)
	monthDayBlock := slacklib.NewInputBlock(
		"ce_rec_monthday",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day of month (1-31)", false, false),
		nil,
		monthDayElem,
	)
	monthDayBlock.Optional = true

	nthSel := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select ordinal…", false, false),
		"ce_rec_nth_input",
		radioOptions(
			[]string{"1", "2", "3", "4", "5"},
			[]string{"1st", "2nd", "3rd", "4th", "5th"},
			strconv.Itoa(nonZero(rec.Nth, 1)),
		)...,
	)
	nthBlock := slacklib.NewInputBlock(
		"ce_rec_nth_sel",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Nth (monthly, Nth-weekday mode)", false, false),
		nil,
		nthSel,
	)
	nthBlock.Optional = true

	nthWeekdaySel := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select weekday…", false, false),
		"ce_rec_nth_weekday_input",
		radioOptions(weekdayCodes, weekdayLabels, nonEmptyStr(rec.NthWeekday, "MO"))...,
	)
	nthWeekdayBlock := slacklib.NewInputBlock(
		"ce_rec_nth_weekday",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Weekday (monthly, Nth-weekday mode)", false, false),
		nil,
		nthWeekdaySel,
	)
	nthWeekdayBlock.Optional = true

	endSel := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select end…", false, false),
		"ce_rec_end_input",
		radioOptions([]string{"never", "until", "count"}, []string{"Never", "On date", "After N occurrences"}, end)...,
	)
	endBlock := slacklib.NewInputBlock(
		"ce_rec_end",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Ends", false, false),
		nil,
		endSel,
	)
	endBlock.Optional = true

	untilPicker := slacklib.NewDatePickerBlockElement("ce_rec_until_input")
	if rec.Until != "" {
		if t, err := time.Parse("2006-01-02", rec.Until); err == nil {
			untilPicker.InitialDate = t.Format("2006-01-02")
		}
	}
	untilBlock := slacklib.NewInputBlock(
		"ce_rec_until",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "End date (when Ends = On date)", false, false),
		nil,
		untilPicker,
	)
	untilBlock.Optional = true

	countElem := slacklib.NewPlainTextInputBlockElement(nil, "ce_rec_count_input")
	if rec.Count > 0 {
		countElem.InitialValue = strconv.Itoa(rec.Count)
	}
	countBlock := slacklib.NewInputBlock(
		"ce_rec_count",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Occurrences (when Ends = After N)", false, false),
		nil,
		countElem,
	)
	countBlock.Optional = true

	return []slacklib.Block{freqBlock, intervalBlock, bydayBlock, monthlyBlock, monthDayBlock, nthBlock, nthWeekdayBlock, endBlock, untilBlock, countBlock}
}

func recIntervalOr(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func nonZero(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func nonEmptyStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
