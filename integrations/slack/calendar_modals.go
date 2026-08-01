package slack

import (
	"fmt"

	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// CalendarCreateEventModal returns a modal for creating a new calendar event.
// calendarID is stored in PrivateMetadata for use on submission.
func CalendarCreateEventModal(calendarID string) slacklib.ModalViewRequest {
	blks := calendarEventFormBlocks("", 0, 0, false, "", "", "", false)

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

// CalendarEventListModal returns a modal that lists all events in a calendar for selection.
// Replaces the create form in-place via UpdateViewContext.
func CalendarEventListModal(calendarID string, events []store.CalendarEvent) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	if len(events) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No events yet._", false, false),
			nil, nil,
		))
	} else {
		for _, e := range events {
			label := fmt.Sprintf("%s — %s", e.StartTime.Format("Jan 2, 2006"), e.Title)
			editBtn := slacklib.NewButtonBlockElement(
				ActionCalendarEventEdit, e.ID,
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

// CalendarEditEventModal returns a modal pre-populated with an existing event's data.
// Pushed on top of the event list via PushViewContext so the user gets a native Back button.
func CalendarEditEventModal(event store.CalendarEvent) slacklib.ModalViewRequest {
	var endUnix int64
	if event.EndTime != nil {
		endUnix = event.EndTime.Unix()
	}
	desc := ""
	if event.Description != nil {
		desc = *event.Description
	}
	loc := ""
	if event.Location != nil {
		loc = *event.Location
	}
	url := ""
	if event.URL != nil {
		url = *event.URL
	}

	blks := calendarEventFormBlocks(event.Title, event.StartTime.Unix(), endUnix, event.EndTime != nil, desc, loc, url, event.AllDay)

	deleteBtn := slacklib.NewButtonBlockElement(
		ActionCalendarEventDelete, event.ID,
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
		PrivateMetadata: event.ID + "|" + event.CalendarID,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

// calendarEventFormBlocks builds the shared input blocks used by both create and edit modals.
// Pass zero values for optional fields to leave them blank.
// hasEnd controls whether endUnix is set as the initial end-time value.
func calendarEventFormBlocks(title string, startUnix, endUnix int64, hasEnd bool, description, location, url string, allDay bool) []slacklib.Block {
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

	return []slacklib.Block{titleBlock, startBlock, endBlock, allDayBlock, descBlock, locBlock, urlBlock}
}
