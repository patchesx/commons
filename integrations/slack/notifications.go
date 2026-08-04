package slack

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/integrations/slack/blocks"
	"commons/permissions"
	"commons/store"
)

// NotifyNewAdmin DMs the newly created admin their temp password and web UI link.
func NotifyNewAdmin(ctx context.Context, pool *pgxpool.Pool, slackExternalID, adminURL, tempPassword string) {
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack: NotifyNewAdmin: bot_token not configured")
		return
	}

	var text string
	if adminURL != "" {
		text = fmt.Sprintf("You've been added as an admin.\nLogin at: %s\nTemporary password: `%s`\nYou'll be prompted to set a new password on first login.", adminURL, tempPassword)
	} else {
		text = fmt.Sprintf("You've been added as an admin.\nTemporary password: `%s`\nYou'll be prompted to set a new password on first login.", tempPassword)
	}

	if _, _, err := SendDM(ctx, client, slackExternalID, text); err != nil {
		log.Printf("slack: DM new admin to %s: %v", slackExternalID, err)
	}
}

// NotifyChannelAccessRequest DMs designated channel approvers when a new access request is submitted.
// If no approvers are configured for the channel, falls back to users with the owner role.
// After notifying approvers, DMs the requester to confirm who received the request.
func NotifyChannelAccessRequest(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, req *store.ChannelRequest) {
	approvers, err := store.GetChannelApprovers(ctx, pool, req.SlackChannelID)
	if err != nil {
		log.Printf("slack: get channel approvers for %s: %v", req.SlackChannelID, err)
		return
	}
	if len(approvers) == 0 {
		approvers, err = store.ListUsersWithRoleName(ctx, pool, "owner")
		if err != nil {
			log.Printf("slack: list owner role users for fallback: %v", err)
			return
		}
	}

	requesterName := "Someone"
	var requesterSlackID string
	if req.RequesterID != nil {
		if u, err := store.GetUserByID(ctx, pool, *req.RequesterID); err == nil && u != nil {
			requesterName = u.DisplayName
			if sid, err := store.GetUserExternalID(ctx, pool, u.ID, "slack"); err == nil {
				requesterSlackID = sid
			}
		}
	}

	text := fmt.Sprintf(":wave: *%s* is requesting access to *#%s*", requesterName, req.SlackChannelName)

	display := []blocks.ChannelRequestDisplay{{
		ID:               req.ID,
		RequesterName:    requesterName,
		SlackChannelName: req.SlackChannelName,
		RequestedAt:      req.RequestedAt,
	}}
	requestBlocks := blocks.PendingRequestsBlocks(display)

	var notifiedNames []string
	for _, approver := range approvers {
		slackID, err := store.GetUserExternalID(ctx, pool, approver.ID, "slack")
		if err != nil {
			log.Printf("slack: DM channel request to %s: no Slack identity: %v", approver.ID, err)
			continue
		}
		dmChannelID, ts, err := SendDM(ctx, client, slackID, text, requestBlocks...)
		if err != nil {
			log.Printf("slack: DM channel request to %s: %v", approver.ID, err)
			continue
		}
		if err := store.RecordRequestNotification(ctx, pool, req.ID, slackID, dmChannelID, ts); err != nil {
			log.Printf("slack: record notification for request %s: %v", req.ID, err)
		}
		notifiedNames = append(notifiedNames, approver.DisplayName)
	}

	if requesterSlackID == "" {
		return
	}
	var confirmation string
	if len(notifiedNames) == 0 {
		confirmation = fmt.Sprintf("Your request to join *#%s* has been submitted.", req.SlackChannelName)
	} else {
		names := strings.Join(notifiedNames, ", ")
		confirmation = fmt.Sprintf("Your request to join *#%s* has been sent to %s. You'll be notified once it's reviewed.", req.SlackChannelName, names)
	}
	if _, _, err := SendDM(ctx, client, requesterSlackID, confirmation); err != nil {
		log.Printf("slack: DM confirmation to requester %s: %v", requesterSlackID, err)
	}
}

// UpdateRequestNotificationDMs edits all approver DM notifications for a request to show a resolved status.
// Called after approve or decline so every approver sees who handled it, regardless of which surface they used.
func UpdateRequestNotificationDMs(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, requestID, resolvedText string) {
	notifications, err := store.ListRequestNotifications(ctx, pool, requestID)
	if err != nil {
		log.Printf("slack: list notifications for request %s: %v", requestID, err)
		return
	}
	req, err := store.GetChannelRequest(ctx, pool, requestID)
	if err != nil || req == nil {
		log.Printf("slack: update DM notifications: request %s not found: %v", requestID, err)
		return
	}
	displays := buildRequestDisplays(ctx, pool, []store.ChannelRequest{*req})
	resolvedBlocks := blocks.ResolvedRequestBlocks(displays[0], resolvedText)
	for _, n := range notifications {
		if _, _, _, err := client.UpdateMessageContext(ctx, n.DMChannelID, n.MessageTS,
			slacklib.MsgOptionBlocks(resolvedBlocks...),
		); err != nil {
			log.Printf("slack: update DM notification %s: %v", n.ID, err)
		}
	}
}

// NotifyAdminsNewWorkItem DMs all users with admin.access permission when a
// new work item is submitted.
func NotifyAdminsNewWorkItem(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, item *store.WorkItem) {
	admins, err := store.ListUsersWithPermission(ctx, pool, permissions.AdminAccess)
	if err != nil {
		log.Printf("slack: NotifyAdminsNewWorkItem: list admins: %v", err)
		return
	}

	typeLabel := "Issue"
	if item.Type == "feature_request" {
		typeLabel = "Feature Request"
	}

	text := fmt.Sprintf(":memo: *New %s from %s:* %s", typeLabel, item.RequesterName, item.Title)
	if item.Description != nil && *item.Description != "" {
		excerpt := *item.Description
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "..."
		}
		text += "\n" + excerpt
	}
	text += "\n_Check the admin panel to triage._"

	for _, admin := range admins {
		slackID, err := store.GetUserExternalID(ctx, pool, admin.ID, "slack")
		if err != nil {
			log.Printf("slack: DM new work item to admin %s: no Slack identity: %v", admin.ID, err)
			continue
		}
		if _, _, err := SendDM(ctx, client, slackID, text); err != nil {
			log.Printf("slack: DM new work item to admin %s: %v", admin.ID, err)
		}
	}
}
