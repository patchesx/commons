package discord

import "github.com/bwmarrin/discordgo"

// buildRoleRequestModal returns an interaction response that opens a role request modal.
func buildRoleRequestModal() *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "discord:role_request_submit",
			Title:    "Request a Role",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "role_name",
							Label:       "Role Name",
							Style:       discordgo.TextInputShort,
							Placeholder: "e.g. Organizer",
							Required:    true,
							MaxLength:   100,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "role_id",
							Label:       "Role ID (from Discord)",
							Style:       discordgo.TextInputShort,
							Placeholder: "e.g. 123456789012345678",
							Required:    true,
							MaxLength:   20,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "reason",
							Label:       "Reason (optional)",
							Style:       discordgo.TextInputParagraph,
							Placeholder: "Why do you need this role?",
							Required:    false,
							MaxLength:   500,
						},
					},
				},
			},
		},
	}
}

// buildReportIssueModal returns an interaction response that opens a report/feedback modal.
func buildReportIssueModal() *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "discord:report_issue_submit",
			Title:    "Report an Issue or Give Feedback",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "title",
							Label:       "Title",
							Style:       discordgo.TextInputShort,
							Placeholder: "Brief summary",
							Required:    true,
							MaxLength:   200,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "description",
							Label:       "Description",
							Style:       discordgo.TextInputParagraph,
							Placeholder: "More detail (optional)",
							Required:    false,
							MaxLength:   2000,
						},
					},
				},
			},
		},
	}
}

// ephemeralText returns an ephemeral channel message response with the given text.
func ephemeralText(text string) *discordgo.InteractionResponse {
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: text,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	}
}
