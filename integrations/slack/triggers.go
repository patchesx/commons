package slack

import "commons/plugin"

type teamJoinTrigger struct{}

func (t *teamJoinTrigger) ID() string    { return "slack.team_join" }
func (t *teamJoinTrigger) Label() string { return "Member joins workspace" }
func (t *teamJoinTrigger) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "user_id", Label: "Slack User ID", Type: "string"},
		{Key: "user_name", Label: "Full Name", Type: "string"},
		{Key: "display_name", Label: "Display Name", Type: "string"},
	}
}
func (t *teamJoinTrigger) FireOnce() bool { return true }

