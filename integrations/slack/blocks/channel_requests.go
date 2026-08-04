package blocks

import (
	"fmt"
	"time"

	slacklib "github.com/slack-go/slack"
)

// ChannelRequestDisplay carries the display data needed to render a pending request.
// Populated by callers who have access to user lookups.
type ChannelRequestDisplay struct {
	ID               string
	RequesterName    string
	SlackChannelName string
	RequestedAt      time.Time
}

// PendingRequestsBlocks returns Block Kit blocks for a list of pending channel access requests.
// Each request gets a text section and an approve/decline actions block.
func PendingRequestsBlocks(requests []ChannelRequestDisplay) []slacklib.Block {
	if len(requests) == 0 {
		return []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No pending requests._", false, false),
				nil, nil,
			),
		}
	}

	var blks []slacklib.Block
	for _, req := range requests {
		elapsed := time.Since(req.RequestedAt).Round(time.Minute)
		text := fmt.Sprintf("*%s* wants to join *#%s*\n_%s ago_",
			req.RequesterName, req.SlackChannelName, formatDuration(elapsed))

		approveBtn := slacklib.NewButtonBlockElement(
			"approve_channel_request",
			req.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Approve", false, false),
		)
		approveBtn.Style = slacklib.StylePrimary

		declineBtn := slacklib.NewButtonBlockElement(
			"decline_channel_request",
			req.ID,
			slacklib.NewTextBlockObject(slacklib.PlainTextType, "Decline", false, false),
		)
		declineBtn.Style = slacklib.StyleDanger

		blks = append(blks,
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
				nil, nil,
			),
			slacklib.NewActionBlock("", approveBtn, declineBtn),
		)
	}
	return blks
}

// ResolvedRequestBlocks returns Block Kit blocks for a channel access request after it has
// been approved or declined. It preserves the original request section (requester + channel)
// and replaces the approve/decline buttons with the resolution status.
func ResolvedRequestBlocks(req ChannelRequestDisplay, resolvedText string) []slacklib.Block {
	elapsed := time.Since(req.RequestedAt).Round(time.Minute)
	text := fmt.Sprintf("*%s* wants to join *#%s*\n_%s ago_",
		req.RequesterName, req.SlackChannelName, formatDuration(elapsed))

	return []slacklib.Block{
		slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, text, false, false),
			nil, nil,
		),
		slacklib.NewContextBlock("", slacklib.NewTextBlockObject(slacklib.MarkdownType, resolvedText, false, false)),
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	h := int(d.Hours())
	if h == 1 {
		return "1 hour"
	}
	if h < 24 {
		return fmt.Sprintf("%d hours", h)
	}
	days := h / 24
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}
