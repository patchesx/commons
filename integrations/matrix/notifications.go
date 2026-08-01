package matrix

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/permissions"
	"commons/platform"
	"commons/store"
)

// NotifyNewAdmin DMs the newly created admin their temp password and web UI link.
//
// Scaffold — not yet wired: the promote flow composes this DM itself and sends
// it through the composite notifier (web/adminui/helpers.go, api/users.go).
// Kept as a template for a Matrix-specific message if one is ever needed.
func NotifyNewAdmin(ctx context.Context, pool *pgxpool.Pool, matrixUserID, adminURL, tempPassword string) {
	var text string
	if adminURL != "" {
		text = fmt.Sprintf("You've been added as an admin.\nLogin at: %s\nTemporary password: %s\nYou'll be prompted to set a new password on first login.", adminURL, tempPassword)
	} else {
		text = fmt.Sprintf("You've been added as an admin.\nTemporary password: %s\nYou'll be prompted to set a new password on first login.", tempPassword)
	}
	if err := PostDirectMessage(ctx, matrixUserID, text); err != nil {
		log.Printf("matrix: NotifyNewAdmin to %s: %v", matrixUserID, err)
	}
}

// NotifyAdminsNewWorkItem DMs all users with admin.access permission when a
// new work item is submitted.
//
// Scaffold — not yet wired: the !report command creates the work item but does
// not notify admins. Slack's equivalent is called from its report flow
// (integrations/slack/interactions.go); wire this in the same way when building
// out the plugin.
func NotifyAdminsNewWorkItem(ctx context.Context, pool *pgxpool.Pool, item store.WorkItem) {
	admins, err := store.ListUsersWithPermission(ctx, pool, permissions.AdminAccess)
	if err != nil {
		log.Printf("matrix: NotifyAdminsNewWorkItem: list admins: %v", err)
		return
	}

	typeLabel := "Issue"
	if item.Type == "feature_request" {
		typeLabel = "Feature Request"
	}
	text := fmt.Sprintf("New %s from %s: %s", typeLabel, item.RequesterName, item.Title)
	if item.Description != nil && *item.Description != "" {
		excerpt := *item.Description
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "..."
		}
		text += "\n" + excerpt
	}
	text += "\nCheck the admin panel to triage."

	n := &Notifier{pool: pool}
	for _, admin := range admins {
		if err := n.NotifyUser(ctx, admin.ID, platform.Message{Text: text}); err != nil {
			log.Printf("matrix: NotifyAdminsNewWorkItem: notify %s: %v", admin.ID, err)
		}
	}
}
