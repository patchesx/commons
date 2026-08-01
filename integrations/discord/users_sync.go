package discord

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// SyncAllUsers fetches all guild members and upserts them as Discord-identity users.
// Members no longer in the guild have their identity status set to "deactivated".
func SyncAllUsers(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	s := getSession(ctx)
	if s == nil {
		log.Printf("discord/users_sync: bot not available")
		return
	}

	guildID, err := store.GetServiceConfig(ctx, pool, "discord", "guild_id", encKey)
	if err != nil || guildID == "" {
		log.Printf("discord/users_sync: guild_id not configured")
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
		log.Printf("discord/users_sync: create job: %v", err)
		return
	}

	log.Printf("discord/users_sync: starting job %s for guild %s", job.ID, guildID)

	// Paginate through guild members (max 1000 per request).
	upserted := 0
	after := ""
	for {
		members, err := s.GuildMembers(guildID, after, 1000)
		if err != nil {
			log.Printf("discord/users_sync: list members (after=%s): %v", after, err)
			_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("list members: %v", err))
			return
		}

		for _, m := range members {
			if m.User == nil || m.User.Bot {
				continue
			}
			displayName := m.User.GlobalName
			if displayName == "" {
				displayName = m.Nick
			}
			if displayName == "" {
				displayName = m.User.Username
			}
			if _, err := store.GetOrCreateUserByIdentity(ctx, pool, "discord", m.User.ID, m.User.Username, displayName); err != nil {
				log.Printf("discord/users_sync: upsert %s: %v", m.User.ID, err)
				continue
			}
			if err := store.UpdateIdentityStatus(ctx, pool, "discord", m.User.ID, "active"); err != nil {
				log.Printf("discord/users_sync: update status %s: %v", m.User.ID, err)
			}
			upserted++
		}

		if len(members) < 1000 {
			break
		}
		after = members[len(members)-1].User.ID
	}

	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("discord/users_sync: complete job: %v", err)
		return
	}

	log.Printf("discord/users_sync: job %s complete, upserted %d members", job.ID, upserted)
}
