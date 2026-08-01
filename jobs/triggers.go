package jobs

import "commons/plugin"

func init() {
	plugin.RegisterTriggerType(&MeetingReminderTrigger{})
}

// MeetingReminderTrigger fires for each meeting occurrence ~1 hour before start.
type MeetingReminderTrigger struct{}

func (t *MeetingReminderTrigger) ID() string     { return "scheduler.meeting_reminder" }
func (t *MeetingReminderTrigger) Label() string  { return "Meeting Reminder" }
func (t *MeetingReminderTrigger) FireOnce() bool { return false }
func (t *MeetingReminderTrigger) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "user_slack_id", Label: "Slack User ID", Type: "string",
			Description: "Slack external ID of the meeting creator."},
		{Key: "topic", Label: "Meeting Topic", Type: "string"},
		{Key: "start_time", Label: "Start Time", Type: "string",
			Description: "Human-readable start time in the host's timezone."},
		{Key: "start_url", Label: "Start URL", Type: "string",
			Description: "Zoom host start link — do not share."},
		{Key: "join_url", Label: "Join URL", Type: "string"},
	}
}
