package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WorkItem struct {
	ID             string
	RequesterID    string
	RequesterName  string
	Type           string
	Title          string
	Description    *string
	Status         string
	AdminNotes     *string
	ResolvedBy     *string
	ResolvedByName *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateWorkItem inserts a new work item and returns the created record.
func CreateWorkItem(ctx context.Context, pool *pgxpool.Pool, requesterID, itemType, title string, description *string) (*WorkItem, error) {
	w := &WorkItem{}
	err := pool.QueryRow(ctx, `
		INSERT INTO work_items (requester_id, type, title, description)
		VALUES ($1, $2, $3, $4)
		RETURNING id, requester_id, type, title, description, status, admin_notes,
		          resolved_by, created_at, updated_at
	`, requesterID, itemType, title, description).Scan(
		&w.ID, &w.RequesterID, &w.Type, &w.Title, &w.Description,
		&w.Status, &w.AdminNotes, &w.ResolvedBy, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// GetWorkItem returns a single work item by ID, joined with requester and resolver names.
// Returns ErrNotFound if absent.
func GetWorkItem(ctx context.Context, pool *pgxpool.Pool, id string) (*WorkItem, error) {
	w := &WorkItem{}
	err := pool.QueryRow(ctx, `
		SELECT wi.id, wi.requester_id,
		       COALESCE(u.display_name, u.email, '') AS requester_name,
		       wi.type, wi.title, wi.description, wi.status, wi.admin_notes,
		       wi.resolved_by,
		       COALESCE(ru.display_name, ru.email, '') AS resolved_by_name,
		       wi.created_at, wi.updated_at
		FROM work_items wi
		JOIN users u ON u.id = wi.requester_id
		LEFT JOIN users ru ON ru.id = wi.resolved_by
		WHERE wi.id = $1
	`, id).Scan(
		&w.ID, &w.RequesterID, &w.RequesterName,
		&w.Type, &w.Title, &w.Description, &w.Status, &w.AdminNotes,
		&w.ResolvedBy, &w.ResolvedByName,
		&w.CreatedAt, &w.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

// ListWorkItems returns work items, optionally filtered by status and/or type.
// Empty string means no filter for that field.
func ListWorkItems(ctx context.Context, pool *pgxpool.Pool, statusFilter, typeFilter string) ([]WorkItem, error) {
	args := []any{}
	where := "WHERE 1=1"
	if statusFilter != "" {
		args = append(args, statusFilter)
		where += " AND wi.status = $" + fmt.Sprintf("%d", len(args))
	}
	if typeFilter != "" {
		args = append(args, typeFilter)
		where += " AND wi.type = $" + fmt.Sprintf("%d", len(args))
	}

	rows, err := pool.Query(ctx, `
		SELECT wi.id, wi.requester_id,
		       COALESCE(u.display_name, u.email, '') AS requester_name,
		       wi.type, wi.title, wi.description, wi.status, wi.admin_notes,
		       wi.resolved_by,
		       COALESCE(ru.display_name, ru.email, '') AS resolved_by_name,
		       wi.created_at, wi.updated_at
		FROM work_items wi
		JOIN users u ON u.id = wi.requester_id
		LEFT JOIN users ru ON ru.id = wi.resolved_by
		`+where+`
		ORDER BY wi.created_at DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]WorkItem, 0)
	for rows.Next() {
		var w WorkItem
		if err := rows.Scan(
			&w.ID, &w.RequesterID, &w.RequesterName,
			&w.Type, &w.Title, &w.Description, &w.Status, &w.AdminNotes,
			&w.ResolvedBy, &w.ResolvedByName,
			&w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// ListWorkItemsByRequester returns work items submitted by the given user,
// optionally filtered by status and/or type.
func ListWorkItemsByRequester(ctx context.Context, pool *pgxpool.Pool, requesterID, statusFilter, typeFilter string) ([]WorkItem, error) {
	args := []any{requesterID}
	where := "WHERE wi.requester_id = $1"
	if statusFilter != "" {
		args = append(args, statusFilter)
		where += " AND wi.status = $" + fmt.Sprintf("%d", len(args))
	}
	if typeFilter != "" {
		args = append(args, typeFilter)
		where += " AND wi.type = $" + fmt.Sprintf("%d", len(args))
	}

	rows, err := pool.Query(ctx, `
		SELECT wi.id, wi.requester_id,
		       COALESCE(u.display_name, u.email, '') AS requester_name,
		       wi.type, wi.title, wi.description, wi.status, wi.admin_notes,
		       wi.resolved_by,
		       COALESCE(ru.display_name, ru.email, '') AS resolved_by_name,
		       wi.created_at, wi.updated_at
		FROM work_items wi
		JOIN users u ON u.id = wi.requester_id
		LEFT JOIN users ru ON ru.id = wi.resolved_by
		`+where+`
		ORDER BY wi.created_at DESC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]WorkItem, 0)
	for rows.Next() {
		var w WorkItem
		if err := rows.Scan(
			&w.ID, &w.RequesterID, &w.RequesterName,
			&w.Type, &w.Title, &w.Description, &w.Status, &w.AdminNotes,
			&w.ResolvedBy, &w.ResolvedByName,
			&w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// UpdateWorkItemStatus updates the status, admin notes, and resolver of a work item.
// Pass nil for resolvedBy to leave the field unchanged (or clear it by passing a pointer to "").
func UpdateWorkItemStatus(ctx context.Context, pool *pgxpool.Pool, id, status string, adminNotes *string, resolvedBy *string) error {
	_, err := pool.Exec(ctx, `
		UPDATE work_items
		SET status = $1, admin_notes = $2, resolved_by = $3, updated_at = NOW()
		WHERE id = $4
	`, status, adminNotes, resolvedBy, id)
	return err
}
