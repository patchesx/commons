package discord

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RoleRequest is a pending or resolved Discord role request.
type RoleRequest struct {
	ID         string
	UserID     string
	RoleID     string
	RoleName   string
	Reason     *string
	Status     string // pending, approved, denied
	ReviewedBy *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CreateRoleRequest inserts a new role request row.
func CreateRoleRequest(ctx context.Context, pool *pgxpool.Pool, userID, roleID, roleName string, reason *string) (*RoleRequest, error) {
	r := &RoleRequest{}
	err := pool.QueryRow(ctx, `
		INSERT INTO discord.role_requests (user_id, role_id, role_name, reason)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, role_id, role_name, reason, status, reviewed_by, created_at, updated_at
	`, userID, roleID, roleName, reason).Scan(
		&r.ID, &r.UserID, &r.RoleID, &r.RoleName,
		&r.Reason, &r.Status, &r.ReviewedBy, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

// GetPendingRoleRequests returns all role requests with status='pending'.
func GetPendingRoleRequests(ctx context.Context, pool *pgxpool.Pool) ([]RoleRequest, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, user_id, role_id, role_name, reason, status, reviewed_by, created_at, updated_at
		FROM discord.role_requests
		WHERE status = 'pending'
		ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoleRequests(rows)
}

// ListRoleRequests returns all role requests for a given user ID.
func ListRoleRequests(ctx context.Context, pool *pgxpool.Pool, userID string) ([]RoleRequest, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, user_id, role_id, role_name, reason, status, reviewed_by, created_at, updated_at
		FROM discord.role_requests
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoleRequests(rows)
}

// UpdateRoleRequestStatus sets the status and reviewer for a role request.
func UpdateRoleRequestStatus(ctx context.Context, pool *pgxpool.Pool, requestID, status string, reviewedBy *string) error {
	_, err := pool.Exec(ctx, `
		UPDATE discord.role_requests
		SET status = $1, reviewed_by = $2, updated_at = NOW()
		WHERE id = $3
	`, status, reviewedBy, requestID)
	return err
}

func scanRoleRequests(rows pgx.Rows) ([]RoleRequest, error) {
	var out []RoleRequest
	for rows.Next() {
		var r RoleRequest
		if err := rows.Scan(
			&r.ID, &r.UserID, &r.RoleID, &r.RoleName,
			&r.Reason, &r.Status, &r.ReviewedBy, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
