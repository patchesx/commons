package matrix

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"maunium.net/go/mautrix/id"

	"commons/store"
)

// SyncAllUsers fetches all members of the configured home room and upserts them
// as Matrix-identity users. Members no longer in the room have their identity
// status set to "deactivated".
func SyncAllUsers(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	c := getClient(ctx)
	if c == nil {
		log.Printf("matrix/users_sync: bot not available")
		return
	}

	homeRoomID, err := store.GetServiceConfig(ctx, pool, "matrix", "home_room_id", encKey)
	if err != nil || homeRoomID == "" {
		log.Printf("matrix/users_sync: home_room_id not configured")
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
		log.Printf("matrix/users_sync: create job: %v", err)
		return
	}

	log.Printf("matrix/users_sync: starting job %s for room %s", job.ID, homeRoomID)

	members, err := c.JoinedMembers(ctx, id.RoomID(homeRoomID))
	if err != nil {
		log.Printf("matrix/users_sync: list members: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("list members: %v", err))
		return
	}

	upserted := 0
	for memberID, member := range members.Joined {
		if memberID == c.UserID {
			continue
		}
		username := memberID.Localpart()
		displayName := member.DisplayName
		if displayName == "" {
			displayName = username
		}
		if _, err := store.GetOrCreateUserByIdentity(ctx, pool, "matrix", memberID.String(), username, displayName); err != nil {
			log.Printf("matrix/users_sync: upsert %s: %v", memberID, err)
			continue
		}
		if err := store.UpdateIdentityStatus(ctx, pool, "matrix", memberID.String(), "active"); err != nil {
			log.Printf("matrix/users_sync: update status %s: %v", memberID, err)
		}
		upserted++
	}

	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("matrix/users_sync: complete job: %v", err)
		return
	}

	log.Printf("matrix/users_sync: job %s complete, upserted %d members", job.ID, upserted)
}
