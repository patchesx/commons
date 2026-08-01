package slack

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	slacklib "github.com/slack-go/slack"

	"commons/integrations/slack/blocks"
	"commons/platform"
	"commons/store"
	"commons/util"
)

// schedulingTimezones is the curated list of IANA timezone names shown in the
// schedule meeting modal. Americas-focused for initial deployment.
var schedulingTimezones = []string{
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
	"America/Anchorage", "America/Honolulu", "America/Phoenix", "America/Puerto_Rico",
	"America/Toronto", "America/Vancouver", "America/Halifax", "America/St_Johns",
	"America/Winnipeg", "America/Regina", "America/Edmonton",
	"America/Sao_Paulo", "America/Argentina/Buenos_Aires", "America/Santiago",
	"America/Lima", "America/Bogota", "America/Caracas", "America/Mexico_City",
	"America/Monterrey", "America/Tijuana", "UTC",
}

// ScheduleMeetingModal returns a modal for scheduling a Zoom meeting.
// defaultTimezone is pre-selected if it appears in schedulingTimezones;
// otherwise util.FallbackTimezone is used.
func ScheduleMeetingModal(defaultTimezone string) slacklib.ModalViewRequest {
	// Resolve initial timezone selection.
	initialTZ := util.FallbackTimezone
	for _, tz := range schedulingTimezones {
		if tz == defaultTimezone {
			initialTZ = defaultTimezone
			break
		}
	}

	// Build timezone options.
	var tzOptions []*slacklib.OptionBlockObject
	var tzInitial *slacklib.OptionBlockObject
	for _, tz := range schedulingTimezones {
		opt := slacklib.NewOptionBlockObject(
			tz,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, tz, false, false),
			nil,
		)
		tzOptions = append(tzOptions, opt)
		if tz == initialTZ {
			tzInitial = opt
		}
	}

	// Build day-of-week options (used for both weekly_days and monthly_weekday).
	type dayOpt struct{ value, label string }
	weekdays := []dayOpt{
		{"2", "Monday"}, {"3", "Tuesday"}, {"4", "Wednesday"},
		{"5", "Thursday"}, {"6", "Friday"}, {"7", "Saturday"}, {"1", "Sunday"},
	}
	var weekdayOptions []*slacklib.OptionBlockObject
	for _, d := range weekdays {
		weekdayOptions = append(weekdayOptions, slacklib.NewOptionBlockObject(
			d.value,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, d.label, false, false),
			nil,
		))
	}

	// Week-of-month options for monthly_weekday recurrence.
	weekOfMonthOpts := []*slacklib.OptionBlockObject{
		slacklib.NewOptionBlockObject("1", slacklib.NewTextBlockObject(slacklib.PlainTextType, "First", false, false), nil),
		slacklib.NewOptionBlockObject("2", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Second", false, false), nil),
		slacklib.NewOptionBlockObject("3", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Third", false, false), nil),
		slacklib.NewOptionBlockObject("4", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Fourth", false, false), nil),
		slacklib.NewOptionBlockObject("-1", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Last", false, false), nil),
	}

	// Recurrence type options.
	recurrenceOpts := []*slacklib.OptionBlockObject{
		slacklib.NewOptionBlockObject("none", slacklib.NewTextBlockObject(slacklib.PlainTextType, "None", false, false), nil),
		slacklib.NewOptionBlockObject("weekly", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Weekly", false, false), nil),
		slacklib.NewOptionBlockObject("monthly_date", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by date)", false, false), nil),
		slacklib.NewOptionBlockObject("monthly_weekday", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by weekday, e.g. first Thursday)", false, false), nil),
	}
	noneOpt := recurrenceOpts[0]

	// Default start time: now + 1 hour.
	defaultStart := time.Now().Add(time.Hour).Unix()

	// Timezone select element.
	tzSelectElem := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select timezone…", false, false),
		"timezone_input",
		tzOptions...,
	)
	tzSelectElem.InitialOption = tzInitial

	// Datetimepicker.
	dtPicker := slacklib.NewDateTimePickerBlockElement("start_time_picker")
	dtPicker.InitialDateTime = defaultStart

	// Duration number input.
	durationInput := slacklib.NewNumberInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "e.g. 60", false, false),
		"duration_input",
		false,
	).WithInitialValue("60")

	// Recurrence type select.
	recTypeSelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select…", false, false),
		"recurrence_type",
		recurrenceOpts...,
	)
	recTypeSelect.InitialOption = noneOpt

	// Weekly days multi-select.
	weeklyDaysSelect := slacklib.NewOptionsMultiSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select day(s)…", false, false),
		"weekly_days",
		weekdayOptions...,
	)

	// Monthly week select.
	monthlyWeekSelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select…", false, false),
		"monthly_week",
		weekOfMonthOpts...,
	)

	// Monthly weekday select.
	monthlyWeekdaySelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select day…", false, false),
		"monthly_weekday",
		weekdayOptions...,
	)

	blks := []slacklib.Block{
		slacklib.NewInputBlock(
			"topic_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Topic", false, false),
			nil,
			slacklib.NewPlainTextInputBlockElement(nil, "topic_input"),
		),
		slacklib.NewInputBlock(
			"agenda_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Agenda", false, false),
			nil,
			func() *slacklib.PlainTextInputBlockElement {
				e := slacklib.NewPlainTextInputBlockElement(nil, "agenda_input")
				e.Multiline = true
				return e
			}(),
		),
		slacklib.NewInputBlock(
			"start_time_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Start Time", false, false),
			nil,
			dtPicker,
		),
		func() *slacklib.InputBlock {
			reminderOpt := slacklib.NewOptionBlockObject(
				"true",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Send reminder ~1 hour before start", false, false),
				nil,
			)
			checkboxElem := &slacklib.CheckboxGroupsBlockElement{
				Type:           slacklib.METCheckboxGroups,
				ActionID:       "send_reminder",
				Options:        []*slacklib.OptionBlockObject{reminderOpt},
				InitialOptions: []*slacklib.OptionBlockObject{reminderOpt},
			}
			b := slacklib.NewInputBlock(
				"reminder_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Reminders", false, false),
				nil,
				checkboxElem,
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			skipOpt := slacklib.NewOptionBlockObject(
				"true",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Skip recording upload", false, false),
				nil,
			)
			checkboxElem := &slacklib.CheckboxGroupsBlockElement{
				Type:     slacklib.METCheckboxGroups,
				ActionID: "skip_upload",
				Options:  []*slacklib.OptionBlockObject{skipOpt},
			}
			b := slacklib.NewInputBlock(
				"skip_upload_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Recording Upload", false, false),
				nil,
				checkboxElem,
			)
			b.Optional = true
			return b
		}(),
		slacklib.NewInputBlock(
			"duration_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Duration (minutes)", false, false),
			nil,
			durationInput,
		),
		slacklib.NewInputBlock(
			"timezone_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Timezone", false, false),
			nil,
			tzSelectElem,
		),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"password_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Password", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Leave blank for no password", false, false),
				slacklib.NewPlainTextInputBlockElement(nil, "password_input"),
			)
			b.Optional = true
			return b
		}(),
		slacklib.NewDividerBlock(),
		slacklib.NewInputBlock(
			"recurrence_type_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Recurrence", false, false),
			nil,
			recTypeSelect,
		),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"recurrence_interval_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Repeat every", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Number of weeks/months between occurrences. Default: 1", false, false),
				slacklib.NewNumberInputBlockElement(nil, "recurrence_interval", false),
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"weekly_days_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day(s) of week", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Weekly recurrence only", false, false),
				weeklyDaysSelect,
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"monthly_week_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Week of month", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by weekday) only", false, false),
				monthlyWeekSelect,
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"monthly_weekday_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day of week", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by weekday) only", false, false),
				monthlyWeekdaySelect,
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"monthly_day_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day of month", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by date) only. Use 1–28 to avoid month-end issues.", false, false),
				slacklib.NewNumberInputBlockElement(nil, "monthly_day", false),
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock(
				"end_times_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Repeat N times", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Required for recurring meetings. Max 60.", false, false),
				slacklib.NewNumberInputBlockElement(nil, "end_times", false),
			)
			b.Optional = true
			return b
		}(),
	}

	// Mark agenda_block optional.
	if ib, ok := blks[1].(*slacklib.InputBlock); ok {
		ib.Optional = true
	}

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Schedule Meeting", false, false),
		Submit:     slacklib.NewTextBlockObject(slacklib.PlainTextType, "Schedule", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID: ModalIDScheduleMeeting,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// ResourceCategoryModal returns a modal with a category selector.
// Selecting a category triggers ActionSelectResourceCategory which updates this modal.
func ResourceCategoryModal(categories []string) slacklib.ModalViewRequest {
	var blockSet []slacklib.Block

	if len(categories) == 0 {
		blockSet = []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "No resource categories are available.", false, false),
				nil, nil,
			),
		}
	} else {
		var options []*slacklib.OptionBlockObject
		for _, cat := range categories {
			options = append(options, slacklib.NewOptionBlockObject(
				cat,
				slacklib.NewTextBlockObject(slacklib.PlainTextType, cat, false, false),
				nil,
			))
		}
		placeholder := slacklib.NewTextBlockObject(slacklib.PlainTextType, "Choose a category…", false, false)
		selectElem := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeStatic, placeholder, ActionSelectResourceCategory, options...)
		blockSet = []slacklib.Block{slacklib.NewActionBlock("category_picker", selectElem)}
	}

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Resources", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDResourceBrowser,
		Blocks:     slacklib.Blocks{BlockSet: blockSet},
	}
}

// ResourceLinksModal returns an updated resource browser modal showing links for a category.
func ResourceLinksModal(category string, resources []store.Resource) slacklib.ModalViewRequest {
	header := slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType, "*"+category+"*", false, false),
		nil, nil,
	)

	blks := []slacklib.Block{header, slacklib.NewDividerBlock()}
	blks = append(blks, blocks.ResourceBlocks(resources)...)

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Resources", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDResourceBrowser,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// QuickLinksModal returns a full-screen view listing active quick links as URL buttons.
func QuickLinksModal(links []store.QuickLink) slacklib.ModalViewRequest {
	var blks []slacklib.Block
	if len(links) == 0 {
		blks = []slacklib.Block{slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No quick links configured yet._", false, false),
			nil, nil,
		)}
	} else {
		for i := 0; i < len(links); i += 5 {
			end := i + 5
			if end > len(links) {
				end = len(links)
			}
			var btns []slacklib.BlockElement
			for _, l := range links[i:end] {
				btn := slacklib.NewButtonBlockElement(
					fmt.Sprintf("ql_%s", l.ID), "",
					slacklib.NewTextBlockObject(slacklib.PlainTextType, l.Label, false, false),
				)
				btn.URL = l.URL
				btns = append(btns, btn)
			}
			blks = append(blks, slacklib.NewActionBlock(fmt.Sprintf("ql_row_%d", i), btns...))
		}
	}
	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Quick Links", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDQuickLinks,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// ContactsModal returns a full-screen view listing contacts with profile photos.
// profilePics maps Slack user ID → image URL; missing entries show without a photo.
// teamID is used to build a deep-link Message button for each contact; pass "" to omit buttons.
func ContactsModal(contacts []store.Contact, profilePics map[string]string, teamID string) slacklib.ModalViewRequest {
	var blks []slacklib.Block
	if len(contacts) == 0 {
		blks = []slacklib.Block{slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No contacts configured yet._", false, false),
			nil, nil,
		)}
	} else {
		for _, c := range contacts {
			// Plain text — no @mention to avoid the profile-card-behind-modal UX problem.
			text := fmt.Sprintf("*%s*\n%s", c.Title, c.Username)
			var accessory *slacklib.Accessory
			if url, ok := profilePics[c.ExternalID]; ok && url != "" {
				accessory = slacklib.NewAccessory(slacklib.NewImageBlockElement(url, c.Username))
			}
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, accessory,
			))
			if teamID != "" {
				msgBtn := slacklib.NewButtonBlockElement(
					"msg_"+c.ExternalID, "",
					slacklib.NewTextBlockObject(slacklib.PlainTextType, "Message", false, false),
				)
				msgBtn.URL = fmt.Sprintf("slack://user?team=%s&id=%s", teamID, c.ExternalID)
				blks = append(blks, slacklib.NewActionBlock("msg_actions_"+c.ExternalID, msgBtn))
			}
		}
	}
	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Contacts", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDContacts,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// PendingRequestsModal returns a full-screen view listing pending channel access requests.
func PendingRequestsModal(displays []blocks.ChannelRequestDisplay) slacklib.ModalViewRequest {
	var blks []slacklib.Block
	blks = append(blks, blocks.PendingRequestsBlocks(displays)...)
	if len(blks) == 0 {
		blks = []slacklib.Block{slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No pending requests._", false, false),
			nil, nil,
		)}
	}
	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Pending Requests", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDPendingRequests,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// ManageMeetingsModal returns a modal listing upcoming meetings.
// Each meeting is a single section block with an overflow menu accessory.
func ManageMeetingsModal(meetings []store.MeetingSummary) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	if len(meetings) == 0 {
		blks = []slacklib.Block{slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No upcoming meetings scheduled._", false, false),
			nil, nil,
		)}
	} else {
		for _, m := range meetings {
			loc, err := time.LoadLocation(m.Timezone)
			if err != nil {
				loc = time.UTC
			}

			var text string
			var opts []*slacklib.OptionBlockObject

			opt := func(label, value string) *slacklib.OptionBlockObject {
				return slacklib.NewOptionBlockObject(value,
					slacklib.NewTextBlockObject(slacklib.PlainTextType, label, false, false), nil)
			}

			if m.IsRecurring && len(m.Occurrences) > 0 {
				occ := m.Occurrences[0]
				text = fmt.Sprintf("*%s*\n%s _(recurring)_", m.Topic,
					occ.StartTime.In(loc).Format("Mon Jan 2, 2006 at 3:04 PM MST"))
				opts = []*slacklib.OptionBlockObject{
					opt("Edit Next Occurrence", "edit_occurrence:"+occ.ID),
					opt("Cancel Next Occurrence", "cancel_occurrence:"+occ.ID),
					opt("Cancel Series", "cancel_meeting:"+m.ID),
				}
			} else if m.IsRecurring {
				text = fmt.Sprintf("*%s*\n_No upcoming occurrences_", m.Topic)
				opts = []*slacklib.OptionBlockObject{
					opt("Cancel Series", "cancel_meeting:"+m.ID),
				}
			} else {
				text = fmt.Sprintf("*%s*\n%s", m.Topic,
					m.NextStart.In(loc).Format("Mon Jan 2, 2006 at 3:04 PM MST"))
				opts = []*slacklib.OptionBlockObject{
					opt("Edit", "edit_meeting:"+m.ID),
					opt("Cancel", "cancel_meeting:"+m.ID),
				}
			}

			overflow := slacklib.NewOverflowBlockElement(ActionMeetingOverflow, opts...)
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, slacklib.NewAccessory(overflow),
			))
		}
	}

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Upcoming Meetings", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDManageMeetings,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// EditMeetingModal returns a pre-populated modal for editing an existing meeting series.
// It uses the same block/action IDs as ScheduleMeetingModal so the same parsing code applies.
func EditMeetingModal(m *store.ScheduledMeeting, firstOccStart time.Time, defaultTimezone string) slacklib.ModalViewRequest {
	// Resolve timezone.
	tz := m.Timezone
	if tz == "" {
		tz = defaultTimezone
	}
	if tz == "" {
		tz = util.FallbackTimezone
	}

	// Build timezone options.
	var tzOptions []*slacklib.OptionBlockObject
	var tzInitial *slacklib.OptionBlockObject
	for _, t := range schedulingTimezones {
		opt := slacklib.NewOptionBlockObject(
			t,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, t, false, false),
			nil,
		)
		tzOptions = append(tzOptions, opt)
		if t == tz {
			tzInitial = opt
		}
	}
	if tzInitial == nil && len(tzOptions) > 0 {
		tzInitial = tzOptions[0]
	}

	// Decode recurrence pattern.
	var recPat *platform.RecurrencePattern
	if m.RecurrencePattern != nil {
		var rp platform.RecurrencePattern
		if json.Unmarshal(m.RecurrencePattern, &rp) == nil && rp.Type != 0 {
			recPat = &rp
		}
	}

	// Determine recurrence type string and initial option.
	recTypeStr := "none"
	if recPat != nil {
		switch recPat.Type {
		case 2:
			recTypeStr = "weekly"
		case 3:
			if recPat.MonthlyDay > 0 {
				recTypeStr = "monthly_date"
			} else {
				recTypeStr = "monthly_weekday"
			}
		}
	}

	type dayOpt struct{ value, label string }
	weekdays := []dayOpt{
		{"2", "Monday"}, {"3", "Tuesday"}, {"4", "Wednesday"},
		{"5", "Thursday"}, {"6", "Friday"}, {"7", "Saturday"}, {"1", "Sunday"},
	}
	var weekdayOptions []*slacklib.OptionBlockObject
	for _, d := range weekdays {
		weekdayOptions = append(weekdayOptions, slacklib.NewOptionBlockObject(
			d.value,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, d.label, false, false),
			nil,
		))
	}

	weekOfMonthOpts := []*slacklib.OptionBlockObject{
		slacklib.NewOptionBlockObject("1", slacklib.NewTextBlockObject(slacklib.PlainTextType, "First", false, false), nil),
		slacklib.NewOptionBlockObject("2", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Second", false, false), nil),
		slacklib.NewOptionBlockObject("3", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Third", false, false), nil),
		slacklib.NewOptionBlockObject("4", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Fourth", false, false), nil),
		slacklib.NewOptionBlockObject("-1", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Last", false, false), nil),
	}

	recurrenceOpts := []*slacklib.OptionBlockObject{
		slacklib.NewOptionBlockObject("none", slacklib.NewTextBlockObject(slacklib.PlainTextType, "None", false, false), nil),
		slacklib.NewOptionBlockObject("weekly", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Weekly", false, false), nil),
		slacklib.NewOptionBlockObject("monthly_date", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by date)", false, false), nil),
		slacklib.NewOptionBlockObject("monthly_weekday", slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by weekday, e.g. first Thursday)", false, false), nil),
	}
	var recTypeInitial *slacklib.OptionBlockObject
	for _, o := range recurrenceOpts {
		if o.Value == recTypeStr {
			recTypeInitial = o
			break
		}
	}
	if recTypeInitial == nil {
		recTypeInitial = recurrenceOpts[0]
	}

	// Start time: use first upcoming occurrence if available, else now+1h.
	startUnix := time.Now().Add(time.Hour).Unix()
	if !firstOccStart.IsZero() {
		startUnix = firstOccStart.Unix()
	}

	// Timezone select.
	tzSelectElem := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select timezone…", false, false),
		"timezone_input",
		tzOptions...,
	)
	tzSelectElem.InitialOption = tzInitial

	// Datetimepicker.
	dtPicker := slacklib.NewDateTimePickerBlockElement("start_time_picker")
	dtPicker.InitialDateTime = startUnix

	// Duration.
	durationInput := slacklib.NewNumberInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "e.g. 60", false, false),
		"duration_input",
		false,
	).WithInitialValue(strconv.Itoa(m.DurationMinutes))

	// Recurrence type select.
	recTypeSelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select…", false, false),
		"recurrence_type",
		recurrenceOpts...,
	)
	recTypeSelect.InitialOption = recTypeInitial

	// Weekly days multi-select (pre-select if weekly).
	weeklyDaysSelect := slacklib.NewOptionsMultiSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select day(s)…", false, false),
		"weekly_days",
		weekdayOptions...,
	)
	if recPat != nil && recPat.Type == 2 && recPat.WeeklyDays != "" {
		selectedDays := strings.Split(recPat.WeeklyDays, ",")
		var initDays []*slacklib.OptionBlockObject
		for _, sd := range selectedDays {
			sd = strings.TrimSpace(sd)
			for _, opt := range weekdayOptions {
				if opt.Value == sd {
					initDays = append(initDays, opt)
					break
				}
			}
		}
		weeklyDaysSelect.InitialOptions = initDays
	}

	// Monthly week select.
	monthlyWeekSelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select…", false, false),
		"monthly_week",
		weekOfMonthOpts...,
	)
	if recPat != nil && recPat.Type == 3 && recPat.MonthlyWeek != 0 {
		weekStr := strconv.Itoa(recPat.MonthlyWeek)
		for _, o := range weekOfMonthOpts {
			if o.Value == weekStr {
				monthlyWeekSelect.InitialOption = o
				break
			}
		}
	}

	// Monthly weekday select.
	monthlyWeekdaySelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select day…", false, false),
		"monthly_weekday",
		weekdayOptions...,
	)
	if recPat != nil && recPat.Type == 3 && recPat.MonthlyWeekDay != 0 {
		wdStr := strconv.Itoa(recPat.MonthlyWeekDay)
		for _, o := range weekdayOptions {
			if o.Value == wdStr {
				monthlyWeekdaySelect.InitialOption = o
				break
			}
		}
	}

	// Topic input (pre-populated).
	topicInput := slacklib.NewPlainTextInputBlockElement(nil, "topic_input")
	topicInput.InitialValue = m.Topic

	// Agenda input (pre-populated).
	agendaInput := slacklib.NewPlainTextInputBlockElement(nil, "agenda_input")
	agendaInput.Multiline = true
	agendaInput.InitialValue = m.Agenda

	// Password input (pre-populated with decrypted value).
	passwordInput := slacklib.NewPlainTextInputBlockElement(nil, "password_input")
	passwordInput.InitialValue = m.Password

	blks := []slacklib.Block{
		slacklib.NewInputBlock("topic_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Topic", false, false),
			nil, topicInput),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock("agenda_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Agenda", false, false),
				nil, agendaInput)
			b.Optional = true
			return b
		}(),
		slacklib.NewInputBlock("start_time_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Start Time", false, false),
			nil, dtPicker),
		func() *slacklib.InputBlock {
			reminderOpt := slacklib.NewOptionBlockObject(
				"true",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Send reminder ~1 hour before start", false, false),
				nil,
			)
			var initialOpts []*slacklib.OptionBlockObject
			if m.SendReminder {
				initialOpts = []*slacklib.OptionBlockObject{reminderOpt}
			}
			checkboxElem := &slacklib.CheckboxGroupsBlockElement{
				Type:           slacklib.METCheckboxGroups,
				ActionID:       "send_reminder",
				Options:        []*slacklib.OptionBlockObject{reminderOpt},
				InitialOptions: initialOpts,
			}
			b := slacklib.NewInputBlock(
				"reminder_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Reminders", false, false),
				nil,
				checkboxElem,
			)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			skipOpt := slacklib.NewOptionBlockObject(
				"true",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Skip recording upload", false, false),
				nil,
			)
			var initialOpts []*slacklib.OptionBlockObject
			if m.SkipUpload {
				initialOpts = []*slacklib.OptionBlockObject{skipOpt}
			}
			checkboxElem := &slacklib.CheckboxGroupsBlockElement{
				Type:           slacklib.METCheckboxGroups,
				ActionID:       "skip_upload",
				Options:        []*slacklib.OptionBlockObject{skipOpt},
				InitialOptions: initialOpts,
			}
			b := slacklib.NewInputBlock(
				"skip_upload_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Recording Upload", false, false),
				nil,
				checkboxElem,
			)
			b.Optional = true
			return b
		}(),
		slacklib.NewInputBlock("duration_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Duration (minutes)", false, false),
			nil, durationInput),
		slacklib.NewInputBlock("timezone_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Timezone", false, false),
			nil, tzSelectElem),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock("password_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Password", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Leave blank for no password", false, false),
				passwordInput)
			b.Optional = true
			return b
		}(),
		slacklib.NewDividerBlock(),
		slacklib.NewInputBlock("recurrence_type_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Recurrence", false, false),
			nil, recTypeSelect),
		func() *slacklib.InputBlock {
			var initVal string
			if recPat != nil && recPat.RepeatInterval > 1 {
				initVal = strconv.Itoa(recPat.RepeatInterval)
			}
			elem := slacklib.NewNumberInputBlockElement(nil, "recurrence_interval", false)
			if initVal != "" {
				elem = elem.WithInitialValue(initVal)
			}
			b := slacklib.NewInputBlock("recurrence_interval_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Repeat every", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Number of weeks/months between occurrences. Default: 1", false, false),
				elem)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock("weekly_days_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day(s) of week", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Weekly recurrence only", false, false),
				weeklyDaysSelect)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock("monthly_week_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Week of month", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by weekday) only", false, false),
				monthlyWeekSelect)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			b := slacklib.NewInputBlock("monthly_weekday_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day of week", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by weekday) only", false, false),
				monthlyWeekdaySelect)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			var initVal string
			if recPat != nil && recPat.MonthlyDay > 0 {
				initVal = strconv.Itoa(recPat.MonthlyDay)
			}
			elem := slacklib.NewNumberInputBlockElement(nil, "monthly_day", false)
			if initVal != "" {
				elem = elem.WithInitialValue(initVal)
			}
			b := slacklib.NewInputBlock("monthly_day_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Day of month", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Monthly (by date) only. Use 1–28 to avoid month-end issues.", false, false),
				elem)
			b.Optional = true
			return b
		}(),
		func() *slacklib.InputBlock {
			var initVal string
			if recPat != nil && recPat.EndTimes > 0 {
				initVal = strconv.Itoa(recPat.EndTimes)
			}
			elem := slacklib.NewNumberInputBlockElement(nil, "end_times", false)
			if initVal != "" {
				elem = elem.WithInitialValue(initVal)
			}
			b := slacklib.NewInputBlock("end_times_block",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Repeat N times", false, false),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Required for recurring meetings. Max 60.", false, false),
				elem)
			b.Optional = true
			return b
		}(),
	}

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Edit Meeting", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Save", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDEditMeeting,
		PrivateMetadata: m.ID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// EditOccurrenceModal returns a modal for editing a single occurrence's time and duration.
func EditOccurrenceModal(occ *store.OccurrenceDetail) slacklib.ModalViewRequest {
	loc, err := time.LoadLocation(occ.Timezone)
	if err != nil {
		loc = time.UTC
	}
	localTime := occ.StartTime.In(loc)

	datePicker := slacklib.NewDatePickerBlockElement("date_picker")
	datePicker.InitialDate = localTime.Format("2006-01-02")

	timePicker := slacklib.NewTimePickerBlockElement("time_picker")
	timePicker.InitialTime = localTime.Format("15:04")

	durationInput := slacklib.NewNumberInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "e.g. 60", false, false),
		"duration_input",
		false,
	).WithInitialValue(strconv.Itoa(occ.DurationMinutes))

	blks := []slacklib.Block{
		slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType,
				fmt.Sprintf("*%s*\nTimezone: %s", occ.Topic, occ.Timezone), false, false),
			nil, nil,
		),
		slacklib.NewInputBlock("date_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Date", false, false),
			nil, datePicker),
		slacklib.NewInputBlock("time_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Time", false, false),
			nil, timePicker),
		slacklib.NewInputBlock("duration_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Duration (minutes)", false, false),
			nil, durationInput),
	}

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Edit Occurrence", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Save", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDEditOccurrence,
		PrivateMetadata: occ.OccurrenceID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// ChannelRequestModal returns a modal for submitting a channel access request.
func ChannelRequestModal(channels []slacklib.Channel) slacklib.ModalViewRequest {
	if len(channels) == 0 {
		// No channels available (missing scope or workspace has no private channels).
		msg := "_No private channels available._"
		// If you want to differentiate missing scope, you'd need to pass that info separately.
		blocks := []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, msg, false, false),
				nil, nil,
			),
		}
		return slacklib.ModalViewRequest{
			Type:       slacklib.VTModal,
			Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Request Channel Access", false, false),
			Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
			CallbackID: ModalIDChannelRequest,
			Blocks:     slacklib.Blocks{BlockSet: blocks},
		}
	}

	var options []*slacklib.OptionBlockObject
	for _, ch := range channels {
		name := ch.Name
		if name == "" {
			name = ch.ID
		}
		options = append(options, slacklib.NewOptionBlockObject(
			ch.ID+"|"+ch.Name,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "#"+name, false, false),
			nil,
		))
	}

	placeholder := slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select a channel…", false, false)
	selectElem := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeStatic, placeholder, "selected_channel", options...)
	inputBlock := slacklib.NewInputBlock(
		"channel_input",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Channel", false, false),
		nil,
		selectElem,
	)

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Request Channel Access", false, false),
		Submit:     slacklib.NewTextBlockObject(slacklib.PlainTextType, "Submit", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID: ModalIDChannelRequest,
		Blocks:     slacklib.Blocks{BlockSet: []slacklib.Block{inputBlock}},
	}
}

// LibraryBrowseModal returns a paginated book catalog modal with per-user action buttons.
// userAtMaxCheckouts suppresses "Check Out" buttons across all books when true.
func LibraryBrowseModal(books []store.BookSummary, page, totalPages int, search string, userAtMaxCheckouts bool) slacklib.ModalViewRequest {
	const perPage = 10

	var blks []slacklib.Block

	// Page header.
	pageInfo := fmt.Sprintf("*Library* — Page %d of %d", page, totalPages)
	if totalPages == 0 {
		pageInfo = "*Library*"
	}
	blks = append(blks, slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType, pageInfo, false, false),
		nil, nil,
	))

	// Search input (dispatch on enter).
	searchElem := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Search by title or author…", false, false),
		ActionLibrarySearch,
	)
	searchElem.InitialValue = search
	searchBlock := slacklib.NewInputBlock(
		"library_search_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Search", false, false),
		nil,
		searchElem,
	)
	searchBlock.Optional = true
	searchBlock.DispatchAction = true
	blks = append(blks, searchBlock)

	blks = append(blks, slacklib.NewDividerBlock())

	if len(books) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No books found._", false, false),
			nil, nil,
		))
	} else {
		for _, b := range books {
			// Availability label.
			var availLabel string
			if b.AvailableCopies > 0 {
				availLabel = fmt.Sprintf("%d available", b.AvailableCopies)
			} else if b.HoldsCount > 0 {
				availLabel = fmt.Sprintf("All checked out — %d on hold", b.HoldsCount)
			} else {
				availLabel = "All checked out"
			}

			// Per-user state suffix.
			var statusSuffix string
			if b.UserHasCheckout {
				statusSuffix = " _(checked out)_"
			} else if b.UserHasHold {
				statusSuffix = " _(on hold)_"
			}

			text := fmt.Sprintf("*%s*\n_%s_\n%s%s", b.Title, b.Author, availLabel, statusSuffix)

			// Accessory button selection.
			var accessory *slacklib.Accessory
			if b.UserHasCheckout || b.UserHasHold {
				// No action available; status shown inline.
			} else if b.AvailableCopies > 0 && !userAtMaxCheckouts {
				btn := slacklib.NewButtonBlockElement(
					ActionLibraryCheckout, b.ID,
					slacklib.NewTextBlockObject(slacklib.PlainTextType, "Check Out", false, false),
				)
				accessory = slacklib.NewAccessory(btn)
			} else if b.AvailableCopies == 0 {
				btn := slacklib.NewButtonBlockElement(
					ActionLibraryHold, b.ID,
					slacklib.NewTextBlockObject(slacklib.PlainTextType, "Join Hold Queue", false, false),
				)
				accessory = slacklib.NewAccessory(btn)
			}

			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, accessory,
			))
		}
	}

	// Pagination + My Library footer.
	var footerBtns []slacklib.BlockElement
	if totalPages > 1 {
		if page > 1 {
			prevBtn := slacklib.NewButtonBlockElement(
				ActionLibraryPaginate, fmt.Sprintf("page:%d", page-1),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "← Prev", false, false),
			)
			footerBtns = append(footerBtns, prevBtn)
		}
		if page < totalPages {
			nextBtn := slacklib.NewButtonBlockElement(
				ActionLibraryPaginate, fmt.Sprintf("page:%d", page+1),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Next →", false, false),
			)
			footerBtns = append(footerBtns, nextBtn)
		}
	}
	myBooksBtn := slacklib.NewButtonBlockElement(
		ActionLibraryMyBooks, "open",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "My Library", false, false),
	)
	footerBtns = append(footerBtns, myBooksBtn)
	blks = append(blks, slacklib.NewActionBlock("library_footer", footerBtns...))

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Library", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID:      ModalIDLibraryBrowse,
		PrivateMetadata: fmt.Sprintf("%d|%s", page, search),
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryMyBooksModal returns a modal showing a user's active checkouts and holds.
func LibraryMyBooksModal(checkouts []store.Checkout, holds []store.Hold) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	// Checkouts section.
	blks = append(blks, slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType, "*Active Checkouts*", false, false),
		nil, nil,
	))
	if len(checkouts) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No active checkouts._", false, false),
			nil, nil,
		))
	} else {
		for _, co := range checkouts {
			var dateStr string
			if co.DueDate != nil {
				dateStr = fmt.Sprintf(" — due %s", co.DueDate.Format("Jan 2, 2006"))
			}
			statusBadge := map[string]string{
				"pending": "⏳ Pending approval",
				"active":  "✅ Active",
			}[co.Status]
			if statusBadge == "" {
				statusBadge = co.Status
			}
			text := fmt.Sprintf("*%s*%s\n%s", co.BookTitle, dateStr, statusBadge)
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, nil,
			))
		}
	}

	blks = append(blks, slacklib.NewDividerBlock())

	// Holds section.
	blks = append(blks, slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType, "*My Holds*", false, false),
		nil, nil,
	))
	if len(holds) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No active holds._", false, false),
			nil, nil,
		))
	} else {
		for _, h := range holds {
			text := fmt.Sprintf("*%s*\n#%d in queue", h.BookTitle, h.Position)
			cancelBtn := slacklib.NewButtonBlockElement(
				ActionLibraryCancelHold, h.ID,
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel Hold", false, false),
			)
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, slacklib.NewAccessory(cancelBtn),
			))
		}
	}

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "My Library", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDLibraryMyBooks,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryCheckoutConfirmModal returns a confirmation modal for requesting a checkout.
// The book ID is stored in PrivateMetadata so it survives the submission round-trip.
func LibraryCheckoutConfirmModal(book *store.BookDetail) slacklib.ModalViewRequest {
	text := fmt.Sprintf("*%s*\n_%s_", book.Title, book.Author)
	if book.AvailableCopies == 1 {
		text += "\n1 copy available"
	} else {
		text += fmt.Sprintf("\n%d copies available", book.AvailableCopies)
	}

	blks := []slacklib.Block{
		slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil, nil,
		),
		slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType,
				"_An admin will confirm your request and arrange pickup._", false, false),
			nil, nil,
		),
	}

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Request Checkout", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Request Checkout", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDLibraryCheckout,
		PrivateMetadata: book.ID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// ---- Library admin modals ----

// LibraryAdminData holds per-tab data for the Manage Library admin modal.
type LibraryAdminData struct {
	Tab           string
	Requests      []store.Checkout // pending
	Active        []store.Checkout // active
	Holds         []store.Hold     // all active holds
	Overdue       []store.Checkout // active + past due_date
	Catalog       []store.BookSummary
	CatalogSearch string
	CatalogPage   int
	CatalogPages  int
}

// libraryAdminMeta is stored in PrivateMetadata to survive modal updates.
type libraryAdminMeta struct {
	Tab    string `json:"tab"`
	Search string `json:"search"`
	Page   int    `json:"page"`
}

var adminTabDefs = []struct{ id, label string }{
	{"requests", "Requests"},
	{"active", "Active"},
	{"holds", "Holds"},
	{"overdue", "Overdue"},
	{"catalog", "Catalog"},
}

// LibraryAdminModal builds the multi-tab Manage Library admin modal.
func LibraryAdminModal(data LibraryAdminData) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	// Tab bar.
	var tabBtns []slacklib.BlockElement
	tabActionIDs := map[string]string{
		"requests": ActionLibraryAdminTabRequests,
		"active":   ActionLibraryAdminTabActive,
		"holds":    ActionLibraryAdminTabHolds,
		"overdue":  ActionLibraryAdminTabOverdue,
		"catalog":  ActionLibraryAdminTabCatalog,
	}
	for _, t := range adminTabDefs {
		btn := slacklib.NewButtonBlockElement(
			tabActionIDs[t.id], t.id,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, t.label, false, false),
		)
		if t.id == data.Tab {
			btn.Style = slacklib.StylePrimary
		}
		tabBtns = append(tabBtns, btn)
	}
	blks = append(blks, slacklib.NewActionBlock("library_admin_tabs", tabBtns...))
	blks = append(blks, slacklib.NewDividerBlock())

	switch data.Tab {
	case "requests":
		blks = append(blks, libraryAdminRequestsBlocks(data.Requests)...)
	case "active":
		blks = append(blks, libraryAdminActiveBlocks(data.Active, false)...)
	case "holds":
		blks = append(blks, libraryAdminHoldsBlocks(data.Holds)...)
	case "overdue":
		blks = append(blks, libraryAdminActiveBlocks(data.Overdue, true)...)
	case "catalog":
		blks = append(blks, libraryAdminCatalogBlocks(data)...)
	}

	meta := libraryAdminMeta{Tab: data.Tab, Search: data.CatalogSearch, Page: data.CatalogPage}
	metaBytes, _ := json.Marshal(meta)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Manage Library", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID:      ModalIDLibraryAdmin,
		PrivateMetadata: string(metaBytes),
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

func libraryAdminRequestsBlocks(checkouts []store.Checkout) []slacklib.Block {
	if len(checkouts) == 0 {
		return []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No pending requests._", false, false),
				nil, nil,
			),
		}
	}
	var blks []slacklib.Block
	for _, co := range checkouts {
		text := fmt.Sprintf("*%s*\nRequested by: %s\nRequested: %s",
			co.BookTitle, co.UserName, co.RequestedAt.Format("Jan 2, 2006"))
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil, nil,
		))
		approveBtn := slacklib.NewButtonBlockElement(
			ActionLibraryApprove, co.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Approve", false, false),
		)
		approveBtn.Style = slacklib.StylePrimary
		denyBtn := slacklib.NewButtonBlockElement(
			ActionLibraryDeny, co.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Deny", false, false),
		)
		denyBtn.Style = slacklib.StyleDanger
		blks = append(blks, slacklib.NewActionBlock(
			fmt.Sprintf("library_req_%s", co.ID[:8]), approveBtn, denyBtn,
		))
	}
	return blks
}

func libraryAdminActiveBlocks(checkouts []store.Checkout, overdueMode bool) []slacklib.Block {
	if len(checkouts) == 0 {
		msg := "_No active checkouts._"
		if overdueMode {
			msg = "_No overdue items._"
		}
		return []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, msg, false, false),
				nil, nil,
			),
		}
	}
	var blks []slacklib.Block
	for _, co := range checkouts {
		dueStr := "No due date"
		if co.DueDate != nil {
			dueStr = co.DueDate.Format("Jan 2, 2006")
		}
		barcodeStr := "—"
		if co.CopyBarcode != nil {
			barcodeStr = *co.CopyBarcode
		}
		prefix := ""
		if overdueMode {
			prefix = ":warning: "
		}
		overdueNote := ""
		if overdueMode && co.DueDate != nil {
			overdueNote = " _(overdue)_"
		}
		text := fmt.Sprintf("%s*%s*\nBorrower: %s | Due: %s%s | Copy: %s",
			prefix, co.BookTitle, co.UserName, dueStr, overdueNote, barcodeStr)
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil, nil,
		))
		returnBtn := slacklib.NewButtonBlockElement(
			ActionLibraryMarkReturned, co.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Mark Returned", false, false),
		)
		extendBtn := slacklib.NewButtonBlockElement(
			ActionLibraryExtendDue, co.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Extend Due Date", false, false),
		)
		blks = append(blks, slacklib.NewActionBlock(
			fmt.Sprintf("library_active_%s", co.ID[:8]), returnBtn, extendBtn,
		))
	}
	return blks
}

func libraryAdminHoldsBlocks(holds []store.Hold) []slacklib.Block {
	if len(holds) == 0 {
		return []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No holds in queue._", false, false),
				nil, nil,
			),
		}
	}
	var blks []slacklib.Block
	for _, h := range holds {
		notifiedStr := "Not notified"
		if h.NotifiedAt != nil {
			notifiedStr = "Notified " + h.NotifiedAt.Format("Jan 2")
		}
		text := fmt.Sprintf("*%s*\nMember: %s | Queue #%d | Requested: %s | %s",
			h.BookTitle, h.UserName, h.Position, h.RequestedAt.Format("Jan 2, 2006"), notifiedStr)
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil, nil,
		))
		notifyBtn := slacklib.NewButtonBlockElement(
			ActionLibraryNotifyHold, h.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Notify Available", false, false),
		)
		cancelBtn := slacklib.NewButtonBlockElement(
			ActionLibraryCancelHoldAdmin, h.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel Hold", false, false),
		)
		cancelBtn.Style = slacklib.StyleDanger
		blks = append(blks, slacklib.NewActionBlock(
			fmt.Sprintf("library_hold_%s", h.ID[:8]), notifyBtn, cancelBtn,
		))
	}
	return blks
}

func libraryAdminCatalogBlocks(data LibraryAdminData) []slacklib.Block {
	var blks []slacklib.Block

	// Add Book button.
	addBookBtn := slacklib.NewButtonBlockElement(
		ActionLibraryAddBook, "open",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "+ Add Book", false, false),
	)
	addBookBtn.Style = slacklib.StylePrimary
	blks = append(blks, slacklib.NewActionBlock("library_catalog_header", addBookBtn))

	// Search input.
	searchElem := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Search by title or author…", false, false),
		ActionLibraryAdminSearch,
	)
	searchElem.InitialValue = data.CatalogSearch
	searchBlock := slacklib.NewInputBlock(
		"library_admin_search_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Search", false, false),
		nil,
		searchElem,
	)
	searchBlock.Optional = true
	searchBlock.DispatchAction = true
	blks = append(blks, searchBlock)
	blks = append(blks, slacklib.NewDividerBlock())

	if len(data.Catalog) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No books found._", false, false),
			nil, nil,
		))
	} else {
		for _, b := range data.Catalog {
			availStr := fmt.Sprintf("%d/%d available", b.AvailableCopies, b.TotalCopies)
			text := fmt.Sprintf("*%s*\n_%s_\n%s", b.Title, b.Author, availStr)
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, nil,
			))
			editBtn := slacklib.NewButtonBlockElement(
				ActionLibraryEditBook, b.ID,
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Edit", false, false),
			)
			copiesBtn := slacklib.NewButtonBlockElement(
				ActionLibraryManageCopies, b.ID,
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Copies", false, false),
			)
			blks = append(blks, slacklib.NewActionBlock(
				fmt.Sprintf("library_catalog_%s", b.ID[:8]), editBtn, copiesBtn,
			))
		}
	}

	// Pagination.
	if data.CatalogPages > 1 {
		var pageBtns []slacklib.BlockElement
		if data.CatalogPage > 1 {
			prevBtn := slacklib.NewButtonBlockElement(
				ActionLibraryAdminPaginate, fmt.Sprintf("page:%d", data.CatalogPage-1),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "← Prev", false, false),
			)
			pageBtns = append(pageBtns, prevBtn)
		}
		if data.CatalogPage < data.CatalogPages {
			nextBtn := slacklib.NewButtonBlockElement(
				ActionLibraryAdminPaginate, fmt.Sprintf("page:%d", data.CatalogPage+1),
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Next →", false, false),
			)
			pageBtns = append(pageBtns, nextBtn)
		}
		if len(pageBtns) > 0 {
			blks = append(blks, slacklib.NewActionBlock("library_catalog_pages", pageBtns...))
		}
	}

	return blks
}

// LibraryApproveModal returns a modal for approving a checkout request.
// PrivateMetadata: "checkoutID|adminViewID"
func LibraryApproveModal(checkoutID, memberName, bookTitle, adminViewID string, defaultDueDate time.Time) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	blks = append(blks, slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("Approving *%s* for *%s*", bookTitle, memberName), false, false),
		nil, nil,
	))

	datePicker := slacklib.NewDatePickerBlockElement("due_date_picker")
	datePicker.InitialDate = defaultDueDate.Format("2006-01-02")
	dateBlock := slacklib.NewInputBlock(
		"due_date_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Due Date", false, false),
		nil,
		datePicker,
	)
	blks = append(blks, dateBlock)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Approve Checkout", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Approve", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDLibraryApprove,
		PrivateMetadata: checkoutID + "|" + adminViewID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryDenyModal returns a modal for denying a checkout request.
// PrivateMetadata: "checkoutID|adminViewID"
func LibraryDenyModal(checkoutID, memberName, bookTitle, adminViewID string) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	blks = append(blks, slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("Denying *%s*'s request for *%s*", memberName, bookTitle), false, false),
		nil, nil,
	))

	reasonInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Reason for denial (optional)…", false, false),
		"reason_input",
	)
	reasonInput.Multiline = true
	reasonBlock := slacklib.NewInputBlock(
		"reason_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Reason", false, false),
		nil,
		reasonInput,
	)
	reasonBlock.Optional = true
	blks = append(blks, reasonBlock)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Deny Request", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Deny", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDLibraryDeny,
		PrivateMetadata: checkoutID + "|" + adminViewID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryAddBookModal returns a modal for adding a new book to the catalog.
// PrivateMetadata: adminViewID (to refresh the admin modal after submit).
func LibraryAddBookModal(adminViewID string) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	isbnInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "e.g. 9780062316097", false, false),
		"isbn_input",
	)
	isbnBlock := slacklib.NewInputBlock(
		"isbn_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "ISBN (triggers metadata lookup if title left blank)", false, false),
		nil,
		isbnInput,
	)
	isbnBlock.Optional = true
	blks = append(blks, isbnBlock)

	titleInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Book title", false, false),
		"title_input",
	)
	titleBlock := slacklib.NewInputBlock(
		"title_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Title", false, false),
		nil,
		titleInput,
	)
	titleBlock.Optional = true
	blks = append(blks, titleBlock)

	authorInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Author name", false, false),
		"author_input",
	)
	authorBlock := slacklib.NewInputBlock(
		"author_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Author", false, false),
		nil,
		authorInput,
	)
	authorBlock.Optional = true
	blks = append(blks, authorBlock)

	descInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Brief description…", false, false),
		"description_input",
	)
	descInput.Multiline = true
	descBlock := slacklib.NewInputBlock(
		"description_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Description", false, false),
		nil,
		descInput,
	)
	descBlock.Optional = true
	blks = append(blks, descBlock)

	copiesInput := slacklib.NewNumberInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "1", false, false),
		"copies_input",
		false,
	).WithInitialValue("1")
	blks = append(blks, slacklib.NewInputBlock(
		"copies_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Number of Copies", false, false),
		nil,
		copiesInput,
	))

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Add Book", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Add Book", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDLibraryAddBook,
		PrivateMetadata: adminViewID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryEditBookModal returns a modal for editing a book's metadata.
// PrivateMetadata: "bookID|adminViewID"
func LibraryEditBookModal(book *store.BookDetail, adminViewID string) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	titleInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Book title", false, false),
		"title_input",
	)
	titleInput.InitialValue = book.Title
	blks = append(blks, slacklib.NewInputBlock(
		"title_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Title", false, false),
		nil,
		titleInput,
	))

	authorInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Author name", false, false),
		"author_input",
	)
	authorInput.InitialValue = book.Author
	blks = append(blks, slacklib.NewInputBlock(
		"author_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Author", false, false),
		nil,
		authorInput,
	))

	descInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Brief description…", false, false),
		"description_input",
	)
	descInput.Multiline = true
	if book.Description != nil {
		descInput.InitialValue = *book.Description
	}
	descBlock := slacklib.NewInputBlock(
		"description_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Description", false, false),
		nil,
		descInput,
	)
	descBlock.Optional = true
	blks = append(blks, descBlock)

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Edit Book", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Save", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDLibraryEditBook,
		PrivateMetadata: book.ID + "|" + adminViewID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryManageCopiesModal returns a modal for managing physical copies of a book.
func LibraryManageCopiesModal(book *store.BookDetail) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	blks = append(blks, slacklib.NewSectionBlock(
		slacklib.NewTextBlockObject(slacklib.MarkdownType,
			fmt.Sprintf("*%s* — %d %s", book.Title, book.TotalCopies, pluralize(book.TotalCopies, "copy", "copies")),
			false, false),
		nil, nil,
	))
	blks = append(blks, slacklib.NewDividerBlock())

	for _, c := range book.Copies {
		condStr := c.Condition
		if len(condStr) > 0 {
			condStr = strings.ToUpper(condStr[:1]) + condStr[1:]
		}
		if !c.IsActive {
			condStr += " (deactivated)"
		}
		barcodeStr := "No barcode"
		if c.Barcode != nil {
			barcodeStr = "Barcode: " + *c.Barcode
		}
		loanStr := "Default loan period"
		if c.LoanPeriodDays != nil {
			loanStr = fmt.Sprintf("%d-day loan", *c.LoanPeriodDays)
		}
		statusStr := ":white_check_mark: Available"
		if !c.IsActive {
			statusStr = ":grey_question: Inactive"
		} else if !c.IsAvailable {
			statusStr = ":x: Checked out"
		}
		text := fmt.Sprintf("*%s* | %s | %s\n%s", condStr, barcodeStr, loanStr, statusStr)
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil, nil,
		))
		if c.IsActive && c.IsAvailable {
			deactivateBtn := slacklib.NewButtonBlockElement(
				ActionLibraryDeactivateCopy, c.ID+"|"+book.ID,
				slacklib.NewTextBlockObject(slacklib.PlainTextType, "Deactivate", false, false),
			)
			deactivateBtn.Style = slacklib.StyleDanger
			blks = append(blks, slacklib.NewActionBlock(
				fmt.Sprintf("library_copy_%s", c.ID[:8]), deactivateBtn,
			))
		}
	}

	blks = append(blks, slacklib.NewDividerBlock())
	addCopyBtn := slacklib.NewButtonBlockElement(
		ActionLibraryAddCopy, book.ID,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "+ Add Copy", false, false),
	)
	addCopyBtn.Style = slacklib.StylePrimary
	blks = append(blks, slacklib.NewActionBlock("library_add_copy_btn", addCopyBtn))

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Manage Copies", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID:      ModalIDLibraryManageCopies,
		PrivateMetadata: book.ID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// LibraryExtendDueDateModal returns a modal for extending a checkout's due date.
// PrivateMetadata: "checkoutID|adminViewID"
func LibraryExtendDueDateModal(checkoutID, adminViewID string, currentDueDate *time.Time) slacklib.ModalViewRequest {
	datePicker := slacklib.NewDatePickerBlockElement("new_due_date_picker")
	if currentDueDate != nil {
		datePicker.InitialDate = currentDueDate.Format("2006-01-02")
	} else {
		datePicker.InitialDate = time.Now().AddDate(0, 0, 14).Format("2006-01-02")
	}

	blks := []slacklib.Block{
		slacklib.NewInputBlock(
			"new_due_date_block",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "New Due Date", false, false),
			nil,
			datePicker,
		),
	}

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Extend Due Date", false, false),
		Submit:          slacklib.NewTextBlockObject(slacklib.PlainTextType, "Extend", false, false),
		Close:           slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID:      ModalIDLibraryExtendDue,
		PrivateMetadata: checkoutID + "|" + adminViewID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// pluralize returns singular if n == 1, otherwise plural.
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// ReportIssueModal returns a modal for submitting an issue or feature request.
func ReportIssueModal() slacklib.ModalViewRequest {
	typeOptions := []*slacklib.OptionBlockObject{
		slacklib.NewOptionBlockObject(
			"issue",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Bug Report", false, false),
			nil,
		),
		slacklib.NewOptionBlockObject(
			"feature_request",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Feature Request", false, false),
			nil,
		),
	}

	typeSelect := slacklib.NewOptionsSelectBlockElement(
		slacklib.OptTypeStatic,
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select type...", false, false),
		"type_select",
		typeOptions...,
	)
	typeBlock := slacklib.NewInputBlock(
		"type_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Type", false, false),
		nil,
		typeSelect,
	)

	titleInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Brief summary...", false, false),
		"title_input",
	)
	titleInput.MaxLength = 150
	titleBlock := slacklib.NewInputBlock(
		"title_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Title", false, false),
		nil,
		titleInput,
	)

	descInput := slacklib.NewPlainTextInputBlockElement(
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "More detail (optional)...", false, false),
		"description_input",
	)
	descInput.Multiline = true
	descBlock := slacklib.NewInputBlock(
		"description_block",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "Description", false, false),
		nil,
		descInput,
	)
	descBlock.Optional = true

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Report Issue", false, false),
		Submit:     slacklib.NewTextBlockObject(slacklib.PlainTextType, "Submit", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Cancel", false, false),
		CallbackID: ModalIDReportIssue,
		Blocks:     slacklib.Blocks{BlockSet: []slacklib.Block{typeBlock, titleBlock, descBlock}},
	}
}
