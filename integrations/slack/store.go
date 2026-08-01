package slack

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemberSyncData holds the results of a member_sync job for a given Slack workspace.
type MemberSyncData struct {
	JobID          string
	IntegrationID  string
	MembersAdded   int
	MembersUpdated int
}

// CreateMemberSyncData inserts a new member_sync_data row.
func CreateMemberSyncData(ctx context.Context, pool *pgxpool.Pool, d *MemberSyncData) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO slack.member_sync_data (job_id, integration_id, members_added, members_updated)
		VALUES ($1, $2, $3, $4)
	`, d.JobID, d.IntegrationID, d.MembersAdded, d.MembersUpdated)
	return err
}

// GetMemberSyncData returns the member sync results for a job. Returns nil, nil if not found.
func GetMemberSyncData(ctx context.Context, pool *pgxpool.Pool, jobID string) (*MemberSyncData, error) {
	d := &MemberSyncData{}
	err := pool.QueryRow(ctx, `
		SELECT job_id, integration_id, members_added, members_updated
		FROM slack.member_sync_data WHERE job_id = $1
	`, jobID).Scan(&d.JobID, &d.IntegrationID, &d.MembersAdded, &d.MembersUpdated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// UpdateMemberSyncCounts sets the final member counts for a sync job.
func UpdateMemberSyncCounts(ctx context.Context, pool *pgxpool.Pool, jobID string, added, updated int) error {
	_, err := pool.Exec(ctx, `
		UPDATE slack.member_sync_data
		SET members_added = $1, members_updated = $2
		WHERE job_id = $3
	`, added, updated, jobID)
	return err
}

// MarkUserIdentityBot flags the user linked to the given Slack external_id as a bot
// so it is excluded from member listings. No-op if no matching identity exists.
func MarkUserIdentityBot(ctx context.Context, pool *pgxpool.Pool, externalID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE users u SET bot = TRUE
		FROM user_identities ui
		WHERE ui.user_id = u.id AND ui.provider = 'slack' AND ui.external_id = $1
	`, externalID)
	return err
}
