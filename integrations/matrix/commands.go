package matrix

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"commons/permissions"
	"commons/store"
)

// HandleCommand parses and dispatches a prefixed text command received in a Matrix room.
// Called by the sync loop after prefix-matching.
func HandleCommand(ctx context.Context, pool *pgxpool.Pool, encKey []byte, c *mautrix.Client, roomID id.RoomID, sender id.UserID, body string) {
	prefix := commandPrefix(ctx)
	if !strings.HasPrefix(body, prefix) {
		return
	}
	rest := strings.TrimPrefix(body, prefix)
	rest = strings.TrimSpace(rest)

	parts := strings.SplitN(rest, " ", 2)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	// Resolve sender to an internal user.
	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "matrix", sender.String(), sender.Localpart(), sender.Localpart())
	if err != nil {
		log.Printf("matrix/commands: resolve sender %s: %v", sender, err)
		reply(ctx, c, roomID, "Error identifying your account. Please try again.")
		return
	}

	switch cmd {
	case "home":
		handleHome(ctx, pool, c, roomID, user)
	case "jobs":
		handleJobs(ctx, pool, c, roomID, user)
	case "resources":
		handleResources(ctx, pool, c, roomID, user)
	case "bills":
		handleBills(ctx, pool, c, roomID, user)
	case "role-request":
		handleRoleRequest(ctx, pool, c, roomID, user, arg)
	case "report":
		handleReport(ctx, pool, c, roomID, user, arg)
	default:
		reply(ctx, c, roomID, helpText(prefix))
	}
}

// handleHome sends a permission-filtered menu of available commands.
func handleHome(ctx context.Context, pool *pgxpool.Pool, c *mautrix.Client, roomID id.RoomID, user *store.User) {
	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil {
		log.Printf("matrix/commands: home: get permissions for %s: %v", user.ID, err)
		reply(ctx, c, roomID, "Error loading your permissions.")
		return
	}

	prefix := commandPrefix(ctx)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hello, %s! Available commands:\n", user.DisplayName)
	fmt.Fprintf(&sb, "  %shome — show this menu\n", prefix)
	if contains(perms, permissions.JobsView) {
		fmt.Fprintf(&sb, "  %sjobs — recent pipeline jobs\n", prefix)
	}
	if contains(perms, permissions.ResourcesView) {
		fmt.Fprintf(&sb, "  %sresources — resource library\n", prefix)
	}
	if contains(perms, permissions.LegislationView) {
		fmt.Fprintf(&sb, "  %sbills — tracked legislation\n", prefix)
	}
	if contains(perms, permissions.WorkItemsCreate) {
		fmt.Fprintf(&sb, "  %srole-request <reason> — request a role\n", prefix)
		fmt.Fprintf(&sb, "  %sreport <title> — report an issue\n", prefix)
	}
	reply(ctx, c, roomID, sb.String())
}

// handleJobs lists the most recent 5 jobs.
func handleJobs(ctx context.Context, pool *pgxpool.Pool, c *mautrix.Client, roomID id.RoomID, user *store.User) {
	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil || !contains(perms, permissions.JobsView) {
		reply(ctx, c, roomID, "You don't have permission to view jobs.")
		return
	}

	jobs, _, err := store.ListJobsPaginated(ctx, pool, 5, 0, store.JobFilter{})
	if err != nil {
		log.Printf("matrix/commands: jobs: list: %v", err)
		reply(ctx, c, roomID, "Error loading jobs.")
		return
	}
	if len(jobs) == 0 {
		reply(ctx, c, roomID, "No jobs found.")
		return
	}

	var sb strings.Builder
	sb.WriteString("Recent jobs:\n")
	for _, j := range jobs {
		topic := j.Feature
		if topic == "" {
			topic = j.Type
		}
		fmt.Fprintf(&sb, "  [%s] %s — %s\n",
			j.Status,
			topic,
			j.StartedAt.Format("Jan 2, 2006 15:04"),
		)
	}
	reply(ctx, c, roomID, sb.String())
}

// handleResources lists the first page of resources (up to 5).
func handleResources(ctx context.Context, pool *pgxpool.Pool, c *mautrix.Client, roomID id.RoomID, user *store.User) {
	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil || !contains(perms, permissions.ResourcesView) {
		reply(ctx, c, roomID, "You don't have permission to view resources.")
		return
	}

	resources, err := store.ListResources(ctx, pool, 1, 5)
	if err != nil {
		log.Printf("matrix/commands: resources: list: %v", err)
		reply(ctx, c, roomID, "Error loading resources.")
		return
	}
	if len(resources) == 0 {
		reply(ctx, c, roomID, "No resources found.")
		return
	}

	var sb strings.Builder
	sb.WriteString("Resources:\n")
	for _, r := range resources {
		fmt.Fprintf(&sb, "  • %s — %s\n", r.Title, r.URL)
	}
	reply(ctx, c, roomID, sb.String())
}

// handleBills lists up to 5 tracked bills.
func handleBills(ctx context.Context, pool *pgxpool.Pool, c *mautrix.Client, roomID id.RoomID, user *store.User) {
	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil || !contains(perms, permissions.LegislationView) {
		reply(ctx, c, roomID, "You don't have permission to view legislation.")
		return
	}

	bills, err := store.ListBills(ctx, pool)
	if err != nil {
		log.Printf("matrix/commands: bills: list: %v", err)
		reply(ctx, c, roomID, "Error loading bills.")
		return
	}
	if len(bills) == 0 {
		reply(ctx, c, roomID, "No tracked bills found.")
		return
	}

	if len(bills) > 5 {
		bills = bills[:5]
	}

	var sb strings.Builder
	sb.WriteString("Tracked bills:\n")
	for _, b := range bills {
		extID := b.Identifier
		if b.ExternalID != nil && *b.ExternalID != "" {
			extID = *b.ExternalID
		}
		status := "unknown"
		if b.Status != nil && *b.Status != "" {
			status = *b.Status
		}
		fmt.Fprintf(&sb, "  • %s — %s (%s)\n", extID, b.Title, status)
	}
	reply(ctx, c, roomID, sb.String())
}

// handleRoleRequest creates a role_request work item.
func handleRoleRequest(ctx context.Context, pool *pgxpool.Pool, c *mautrix.Client, roomID id.RoomID, user *store.User, reason string) {
	if reason == "" {
		reply(ctx, c, roomID, "Usage: role-request <reason>")
		return
	}
	title := fmt.Sprintf("Role request from %s", user.DisplayName)
	_, err := store.CreateWorkItem(ctx, pool, user.ID, "role_request", title, &reason)
	if err != nil {
		log.Printf("matrix/commands: role-request: create: %v", err)
		reply(ctx, c, roomID, "Error submitting role request.")
		return
	}
	reply(ctx, c, roomID, "Your role request has been submitted. An admin will review it shortly.")
}

// handleReport creates an issue work item.
func handleReport(ctx context.Context, pool *pgxpool.Pool, c *mautrix.Client, roomID id.RoomID, user *store.User, title string) {
	if title == "" {
		reply(ctx, c, roomID, "Usage: report <title>")
		return
	}
	_, err := store.CreateWorkItem(ctx, pool, user.ID, "issue", title, nil)
	if err != nil {
		log.Printf("matrix/commands: report: create: %v", err)
		reply(ctx, c, roomID, "Error submitting report.")
		return
	}
	reply(ctx, c, roomID, "Your report has been submitted. Thank you!")
}

// reply sends a plain text message to the room, logging any send error.
func reply(ctx context.Context, c *mautrix.Client, roomID id.RoomID, text string) {
	if _, err := c.SendText(ctx, roomID, text); err != nil {
		log.Printf("matrix/commands: send reply to %s: %v", roomID, err)
	}
}

// helpText returns an unknown-command message listing the basic command prefix.
func helpText(prefix string) string {
	return fmt.Sprintf(
		"Unknown command. Try:\n"+
			"  %shome — show available commands\n"+
			"  %sjobs — recent jobs\n"+
			"  %sresources — resource library\n"+
			"  %sbills — tracked legislation\n"+
			"  %srole-request <reason> — request a role\n"+
			"  %sreport <title> — report an issue",
		prefix, prefix, prefix, prefix, prefix, prefix,
	)
}

// contains reports whether p is present in the perms slice.
func contains(perms []string, p string) bool {
	return slices.Contains(perms, p)
}
