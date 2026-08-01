package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelRequestStatus values.
const (
	ChannelRequestPending  = "pending"
	ChannelRequestApproved = "approved"
	ChannelRequestDeclined = "declined"
)

type ChannelRequest struct {
	ID               string
	RequesterID      *string
	SlackChannelID   string
	SlackChannelName string
	Status           string
	ReviewedBy       *string
	ReviewedAt       *time.Time
	RequestedAt      time.Time
}

// CreateChannelRequest inserts a new access request and populates r.ID and r.RequestedAt.
func CreateChannelRequest(ctx context.Context, pool *pgxpool.Pool, r *ChannelRequest) error {
	return pool.QueryRow(ctx, `
		INSERT INTO channel_access_requests (requester_id, slack_channel_id, slack_channel_name)
		VALUES ($1, $2, $3)
		RETURNING id, requested_at
	`, r.RequesterID, r.SlackChannelID, r.SlackChannelName,
	).Scan(&r.ID, &r.RequestedAt)
}

// GetChannelRequest returns a single request by ID. Returns ErrNotFound if absent.
func GetChannelRequest(ctx context.Context, pool *pgxpool.Pool, id string) (*ChannelRequest, error) {
	r := &ChannelRequest{}
	err := pool.QueryRow(ctx, `
		SELECT id, requester_id, slack_channel_id, slack_channel_name,
		       status, reviewed_by, reviewed_at, requested_at
		FROM channel_access_requests WHERE id = $1
	`, id).Scan(
		&r.ID, &r.RequesterID, &r.SlackChannelID, &r.SlackChannelName,
		&r.Status, &r.ReviewedBy, &r.ReviewedAt, &r.RequestedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// UpdateRequestStatus sets the status and reviewer for a request.
func UpdateRequestStatus(ctx context.Context, pool *pgxpool.Pool, id, status, reviewedByID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE channel_access_requests
		SET status = $1, reviewed_by = $2, reviewed_at = NOW()
		WHERE id = $3
	`, status, reviewedByID, id)
	return err
}

// ListPendingRequests returns all pending access requests.
func ListPendingRequests(ctx context.Context, pool *pgxpool.Pool) ([]ChannelRequest, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, requester_id, slack_channel_id, slack_channel_name,
		       status, reviewed_by, reviewed_at, requested_at
		FROM channel_access_requests WHERE status = 'pending'
		ORDER BY requested_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannelRequests(rows)
}

// ListRequestsByRequester returns all requests from a given user.
func ListRequestsByRequester(ctx context.Context, pool *pgxpool.Pool, requesterID string) ([]ChannelRequest, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, requester_id, slack_channel_id, slack_channel_name,
		       status, reviewed_by, reviewed_at, requested_at
		FROM channel_access_requests WHERE requester_id = $1
		ORDER BY requested_at DESC
	`, requesterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannelRequests(rows)
}

// ListPendingRequestsForChannels returns pending requests only for the given Slack channel IDs.
// If slackChannelIDs is empty, returns an empty slice.
func ListPendingRequestsForChannels(ctx context.Context, pool *pgxpool.Pool, slackChannelIDs []string) ([]ChannelRequest, error) {
	if len(slackChannelIDs) == 0 {
		return []ChannelRequest{}, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT id, requester_id, slack_channel_id, slack_channel_name,
		       status, reviewed_by, reviewed_at, requested_at
		FROM channel_access_requests
		WHERE status = 'pending'
		  AND slack_channel_id = ANY($1)
		ORDER BY requested_at ASC
	`, slackChannelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChannelRequests(rows)
}

func scanChannelRequests(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]ChannelRequest, error) {
	var requests []ChannelRequest
	for rows.Next() {
		var r ChannelRequest
		if err := rows.Scan(
			&r.ID, &r.RequesterID, &r.SlackChannelID, &r.SlackChannelName,
			&r.Status, &r.ReviewedBy, &r.ReviewedAt, &r.RequestedAt,
		); err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}
