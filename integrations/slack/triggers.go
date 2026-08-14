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

// MemberUpsertedTrigger fires for each member upserted during sync.
// Non-fire-once: fires every sync for every member (unlike slack.team_join
// which is fire-once for first-join welcome flows).
type MemberUpsertedTrigger struct{}

func (t *MemberUpsertedTrigger) ID() string    { return "slack.member.upserted" }
func (t *MemberUpsertedTrigger) Label() string { return "Slack Member Upserted (per-sync)" }
func (t *MemberUpsertedTrigger) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "user_id", Label: "Slack User ID", Type: "string"},
		{Key: "user_name", Label: "Full Name", Type: "string"},
		{Key: "display_name", Label: "Display Name", Type: "string"},
		{Key: "email", Label: "Email", Type: "string"},
		{Key: "member_id", Label: "Commons Member ID", Type: "string"},
		{Key: "platform_status", Label: "Platform Status", Type: "string"},
		{Key: "sync_job_id", Label: "Sync Job ID", Type: "string"},
	}
}
func (t *MemberUpsertedTrigger) FireOnce() bool { return false }

// MemberDeactivatedTrigger fires when a member's Slack identity is marked
// deactivated (deleted user detected during sync).
type MemberDeactivatedTrigger struct{}

func (t *MemberDeactivatedTrigger) ID() string    { return "slack.member.deactivated" }
func (t *MemberDeactivatedTrigger) Label() string { return "Slack Member Deactivated" }
func (t *MemberDeactivatedTrigger) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "user_id", Label: "Slack User ID", Type: "string"},
		{Key: "sync_job_id", Label: "Sync Job ID", Type: "string"},
	}
}
func (t *MemberDeactivatedTrigger) FireOnce() bool { return false }

// MemberSyncCompletedTrigger fires once after SyncAllUsers finishes.
// Non-fire-once: fires every sync. Data includes summary counts.
type MemberSyncCompletedTrigger struct{}

func (t *MemberSyncCompletedTrigger) ID() string    { return "slack.member_sync.completed" }
func (t *MemberSyncCompletedTrigger) Label() string { return "Slack Member Sync Completed" }
func (t *MemberSyncCompletedTrigger) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "sync_job_id", Label: "Sync Job ID", Type: "string"},
		{Key: "members_upserted", Label: "Members Upserted", Type: "number"},
		{Key: "members_deactivated", Label: "Members Deactivated", Type: "number"},
		{Key: "bots_marked", Label: "Bots Marked", Type: "number"},
		{Key: "integration_id", Label: "Slack Integration ID", Type: "string"},
	}
}
func (t *MemberSyncCompletedTrigger) FireOnce() bool { return false }

