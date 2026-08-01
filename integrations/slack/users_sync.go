package slack

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/plugin"
	"commons/store"
)

// SyncAllUsers fetches all active, non-bot workspace members via Slack users.list
// and upserts them using the same logic as App Home open. Errors per-user are
// logged and do not abort the sync. The sync is wrapped in a member_sync job record.
func SyncAllUsers(ctx context.Context, pool *pgxpool.Pool) {
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/users_sync: bot_token not configured")
		return
	}

	// Get the Slack integration instance (there should be exactly one).
	integration, err := store.GetIntegrationByType(ctx, pool, "slack")
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("slack/users_sync: no enabled Slack integration found")
		return
	}
	if err != nil {
		log.Printf("slack/users_sync: get integration: %v", err)
		return
	}

	// Create the job record.
	job := &store.Job{
		Type:    store.JobTypeMemberSync,
		Feature: store.JobFeatureMemberPortal,
		Trigger: store.JobTriggerScheduled,
		Status:  store.JobStatusRunning,
	}
	if err := store.CreateJob(ctx, pool, job); err != nil {
		log.Printf("slack/users_sync: create job: %v", err)
		return
	}

	// Create initial member_sync_data row with zero counts.
	syncData := &MemberSyncData{
		JobID:          job.ID,
		IntegrationID:  integration.ID,
		MembersAdded:   0,
		MembersUpdated: 0,
	}
	if err := CreateMemberSyncData(ctx, pool, syncData); err != nil {
		log.Printf("slack/users_sync: create sync data: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("create sync data: %v", err))
		return
	}

	log.Printf("slack/users_sync: starting job %s", job.ID)

	users, err := client.GetUsersContext(ctx)
	if err != nil {
		log.Printf("slack/users_sync: get users: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("fetch users: %v", err))
		return
	}

	// Track added vs updated (simplified: GetOrCreateUserByIdentity doesn't differentiate).
	// We'll count each successful upsert as an "update" for now.
	upserted := 0
	for _, u := range users {
		if u.IsBot || u.ID == "USLACKBOT" {
			// Mark any existing users row as bot=true so it's excluded from ListUsers.
			if err := MarkUserIdentityBot(ctx, pool, u.ID); err != nil {
				log.Printf("slack/users_sync: mark bot %s: %v", u.ID, err)
			}
			continue
		}

		// Deleted users: only update status for existing identities — never create new rows.
		if u.Deleted {
			if err := store.UpdateIdentityStatus(ctx, pool, "slack", u.ID, "deactivated"); err != nil {
				log.Printf("slack/users_sync: update status %s: %v", u.ID, err)
			}
			continue
		}

		var platformStatus string
		switch {
		case u.IsRestricted || u.IsUltraRestricted:
			platformStatus = "invited"
		default:
			platformStatus = "active"
		}

		realName := u.Profile.RealName
		displayName := u.Profile.DisplayName
		if realName == "" {
			realName = displayName
		}
		if realName == "" {
			realName = u.ID
		}
		user, err := store.GetOrCreateUserByIdentityMergingEmail(ctx, pool, "slack", u.ID, realName, displayName, u.Profile.Email)
		if err != nil {
			log.Printf("slack/users_sync: upsert %s: %v", u.ID, err)
			continue
		}
		if err := store.UpdateIdentityStatus(ctx, pool, "slack", u.ID, platformStatus); err != nil {
			log.Printf("slack/users_sync: update status %s: %v", u.ID, err)
		}
		if u.Profile.Email != "" {
			if err := store.UpdateUserEmail(ctx, pool, user.ID, u.Profile.Email); err != nil {
				log.Printf("slack/users_sync: update email %s: %v", u.ID, err)
			}
		}
		if err := plugin.Fire(ctx, "slack.team_join", u.ID, map[string]any{
			"user_id":      u.ID,
			"user_name":    realName,
			"display_name": displayName,
		}); err != nil {
			log.Printf("slack/users_sync: team_join trigger %s: %v", u.ID, err)
		}
		upserted++
	}

	// Sync private channels for the channel-approvers feature.
	syncChannels(ctx, pool)

	// Update sync data with final counts.
	if err := UpdateMemberSyncCounts(ctx, pool, job.ID, 0, upserted); err != nil {
		log.Printf("slack/users_sync: update sync counts: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("update counts: %v", err))
		return
	}

	// Mark job complete.
	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("slack/users_sync: complete job: %v", err)
		return
	}

	log.Printf("slack/users_sync: job %s complete, upserted %d users", job.ID, upserted)
}

func syncChannels(ctx context.Context, pool *pgxpool.Pool) {
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/users_sync: syncChannels: bot_token not configured")
		return
	}
	params := &slacklib.GetConversationsParameters{
		Types:           []string{"private_channel"},
		Limit:           200,
		ExcludeArchived: false,
	}
	for {
		channels, cursor, err := client.GetConversationsContext(ctx, params)
		if err != nil {
			log.Printf("slack/users_sync: list channels: %v", err)
			return
		}
		for _, ch := range channels {
			if err := store.UpsertSlackChannel(ctx, pool, ch.ID, ch.Name, ch.IsArchived); err != nil {
				log.Printf("slack/users_sync: upsert channel %s: %v", ch.ID, err)
			}
		}
		if cursor == "" {
			return
		}
		params.Cursor = cursor
	}
}
