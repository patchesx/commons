package discord

import (
	"context"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/permissions"
	"commons/store"
)

// homeButton maps a permission key to the button label and custom ID shown in /home.
type homeButton struct {
	permKey  string
	label    string
	customID string
	style    discordgo.ButtonStyle
}

var homeButtons = []homeButton{
	{permissions.ResourcesView, "Resources", "discord:resources", discordgo.PrimaryButton},
	{permissions.LegislationView, "Legislation", "discord:legislation", discordgo.PrimaryButton},
	{permissions.CalendarView, "Calendar", "discord:calendar", discordgo.PrimaryButton},
	{permissions.MeetingsSchedule, "Schedule Meeting", "discord:schedule_meeting", discordgo.SecondaryButton},
	{permissions.MeetingsManage, "Manage Meetings", "discord:manage_meetings", discordgo.SecondaryButton},
	{"", "Request Role", "discord:role_request", discordgo.SecondaryButton},            // available to all
	{"", "Report Issue / Feedback", "discord:report_issue", discordgo.SecondaryButton}, // available to all
	{permissions.LibraryView, "Library", "discord:library", discordgo.PrimaryButton},
}

// handleHomeCommand handles the /home slash command. It returns an ephemeral
// message with permission-filtered action buttons.
func handleHomeCommand(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction) *discordgo.InteractionResponse {
	if i.Member == nil || i.Member.User == nil {
		return ephemeralText("This command can only be used in a server.")
	}

	discordUserID := i.Member.User.ID
	username := i.Member.User.Username
	displayName := i.Member.User.GlobalName
	if displayName == "" {
		displayName = i.Member.Nick
	}
	if displayName == "" {
		displayName = username
	}

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "discord", discordUserID, username, displayName)
	if err != nil {
		log.Printf("discord/apphome: upsert user %s: %v", discordUserID, err)
		return ephemeralText("Could not look up your account. Please try again.")
	}

	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil {
		log.Printf("discord/apphome: get permissions %s: %v", user.ID, err)
		return ephemeralText("Could not load your permissions. Please try again.")
	}
	permSet := make(map[string]bool, len(perms))
	for _, p := range perms {
		permSet[p] = true
	}

	var components []discordgo.MessageComponent
	var row []discordgo.MessageComponent
	for _, btn := range homeButtons {
		if btn.permKey != "" && !permSet[btn.permKey] {
			continue
		}
		row = append(row, discordgo.Button{
			Label:    btn.label,
			Style:    btn.style,
			CustomID: btn.customID,
		})
		// Discord allows up to 5 buttons per action row.
		if len(row) == 5 {
			components = append(components, discordgo.ActionsRow{Components: row})
			row = nil
		}
	}
	if len(row) > 0 {
		components = append(components, discordgo.ActionsRow{Components: row})
	}

	if len(components) == 0 {
		return ephemeralText("You don't have access to any features yet. Contact an admin to get a role assigned.")
	}

	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content:    "What would you like to do?",
			Flags:      discordgo.MessageFlagsEphemeral,
			Components: components,
		},
	}
}

// handleRoleRequestModal shows a role-request modal.
func handleRoleRequestButton(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction) *discordgo.InteractionResponse {
	_ = ctx
	_ = pool
	return buildRoleRequestModal()
}

// submitRoleRequest handles the modal submit for a role request.
func submitRoleRequest(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction) *discordgo.InteractionResponse {
	if i.Member == nil || i.Member.User == nil {
		return ephemeralText("Could not identify your account.")
	}

	data, ok := i.Data.(discordgo.ModalSubmitInteractionData)
	if !ok {
		return ephemeralText("Unexpected interaction data.")
	}

	var roleID, roleName, reason string
	for _, row := range data.Components {
		ar, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, comp := range ar.Components {
			ti, ok := comp.(*discordgo.TextInput)
			if !ok {
				continue
			}
			switch ti.CustomID {
			case "role_id":
				roleID = ti.Value
			case "role_name":
				roleName = ti.Value
			case "reason":
				reason = ti.Value
			}
		}
	}

	if roleID == "" || roleName == "" {
		return ephemeralText("Please provide both a role ID and role name.")
	}

	discordUserID := i.Member.User.ID
	username := i.Member.User.Username
	displayName := i.Member.User.GlobalName
	if displayName == "" {
		displayName = username
	}

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "discord", discordUserID, username, displayName)
	if err != nil {
		log.Printf("discord: submitRoleRequest: upsert user %s: %v", discordUserID, err)
		return ephemeralText("Could not look up your account. Please try again.")
	}

	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	req, err := CreateRoleRequest(ctx, pool, user.ID, roleID, roleName, reasonPtr)
	if err != nil {
		log.Printf("discord: submitRoleRequest: create %s: %v", user.ID, err)
		return ephemeralText("Could not submit your role request. Please try again.")
	}
	log.Printf("discord: role request %s submitted by user %s for role %s", req.ID, user.ID, roleName)

	return ephemeralText("Your role request has been submitted. An admin will review it shortly.")
}

// submitReportIssue handles the modal submit for a report/feedback form.
func submitReportIssue(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction) *discordgo.InteractionResponse {
	if i.Member == nil || i.Member.User == nil {
		return ephemeralText("Could not identify your account.")
	}

	data, ok := i.Data.(discordgo.ModalSubmitInteractionData)
	if !ok {
		return ephemeralText("Unexpected interaction data.")
	}

	var title, description string
	for _, row := range data.Components {
		ar, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, comp := range ar.Components {
			ti, ok := comp.(*discordgo.TextInput)
			if !ok {
				continue
			}
			switch ti.CustomID {
			case "title":
				title = ti.Value
			case "description":
				description = ti.Value
			}
		}
	}

	if title == "" {
		return ephemeralText("Please provide a title.")
	}

	discordUserID := i.Member.User.ID
	username := i.Member.User.Username
	displayName := i.Member.User.GlobalName
	if displayName == "" {
		displayName = username
	}

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "discord", discordUserID, username, displayName)
	if err != nil {
		log.Printf("discord: submitReportIssue: upsert user %s: %v", discordUserID, err)
		return ephemeralText("Could not look up your account. Please try again.")
	}

	descPtr := &description
	_, err = store.CreateWorkItem(ctx, pool, user.ID, "issue", title, descPtr)
	if err != nil {
		log.Printf("discord: submitReportIssue: create work item: %v", err)
		return ephemeralText("Could not submit your report. Please try again.")
	}

	return ephemeralText("Your report has been submitted. Thank you!")
}
