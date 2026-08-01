package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resourceSelectSQL is the standard JOIN used by all resource read queries.
const resourceSelectSQL = `
	SELECT rl.id, rl.title, rl.url, rc.name, rl.description, rl.position, rl.created_by, rl.created_at
	FROM resource_library rl
	JOIN resource_categories rc ON rc.id = rl.category_id`

type Resource struct {
	ID          string
	Title       string
	URL         string
	Category    string // display name from resource_categories JOIN
	Description *string
	Position    int
	CreatedBy   *string
	CreatedAt   time.Time
}

type ResourceCategory struct {
	ID                 string
	Name               string
	Position           int
	CreatedAt          time.Time
	RequiredPermission *string // nil = no extra permission required beyond resources.view
}

// GetResourceByID returns a single resource by ID. Returns ErrNotFound if absent.
func GetResourceByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Resource, error) {
	var r Resource
	err := pool.QueryRow(ctx, resourceSelectSQL+` WHERE rl.id = $1`, id).
		Scan(&r.ID, &r.Title, &r.URL, &r.Category, &r.Description, &r.Position, &r.CreatedBy, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListResources returns a page of resources ordered by newest first.
func ListResources(ctx context.Context, pool *pgxpool.Pool, page, perPage int) ([]Resource, error) {
	offset := (page - 1) * perPage
	rows, err := pool.Query(ctx, resourceSelectSQL+` ORDER BY rl.created_at DESC, rl.position ASC LIMIT $1 OFFSET $2`, perPage, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResources(rows)
}

// CountResources returns the total number of resources.
func CountResources(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM resource_library`).Scan(&count)
	return count, err
}

// ListResourcesByCategory returns resources for a specific category ordered by position.
func ListResourcesByCategory(ctx context.Context, pool *pgxpool.Pool, category string) ([]Resource, error) {
	rows, err := pool.Query(ctx, resourceSelectSQL+` WHERE rc.name = $1 ORDER BY rl.position, rl.title`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResources(rows)
}

// ListCategories returns distinct category names that have at least one resource, ordered alphabetically.
// Deprecated: use ListCategoriesForUser in Slack bot paths to apply per-category permission filtering.
func ListCategories(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT rc.name
		FROM resource_categories rc
		JOIN resource_library rl ON rl.category_id = rc.id
		ORDER BY rc.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

// ListCategoriesForUser returns category names visible to a user with the given permissions.
// Categories with a required_permission are excluded unless that key is in perms.
// Used by the Slack bot to populate the category picker modal.
func ListCategoriesForUser(ctx context.Context, pool *pgxpool.Pool, perms []string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT rc.name
		FROM resource_categories rc
		JOIN resource_library rl ON rl.category_id = rc.id
		WHERE rc.required_permission IS NULL
		   OR rc.required_permission = ANY($1)
		ORDER BY rc.name
	`, perms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

// CreateResource inserts a new resource and populates r.ID and r.CreatedAt.
// r.Category must match an existing resource_categories.name.
func CreateResource(ctx context.Context, pool *pgxpool.Pool, r *Resource) error {
	return pool.QueryRow(ctx, `
		INSERT INTO resource_library (title, url, category_id, description, position, created_by)
		VALUES ($1, $2, (SELECT id FROM resource_categories WHERE name = $3), $4, $5, $6)
		RETURNING id, created_at
	`, r.Title, r.URL, r.Category, r.Description, r.Position, r.CreatedBy,
	).Scan(&r.ID, &r.CreatedAt)
}

// UpdateResource updates title, URL, category, description, and position for an existing resource.
// r.Category must match an existing resource_categories.name.
func UpdateResource(ctx context.Context, pool *pgxpool.Pool, r *Resource) error {
	_, err := pool.Exec(ctx, `
		UPDATE resource_library
		SET title = $1, url = $2,
		    category_id = (SELECT id FROM resource_categories WHERE name = $3),
		    description = $4, position = $5
		WHERE id = $6
	`, r.Title, r.URL, r.Category, r.Description, r.Position, r.ID)
	return err
}

// DeleteResource removes a resource by ID.
func DeleteResource(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM resource_library WHERE id = $1`, id)
	return err
}

// --- Resource category management ---

// ListResourceCategories returns all defined categories ordered by position then name.
func ListResourceCategories(ctx context.Context, pool *pgxpool.Pool) ([]ResourceCategory, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, position, created_at, required_permission
		FROM resource_categories
		ORDER BY position, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cats []ResourceCategory
	for rows.Next() {
		var c ResourceCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Position, &c.CreatedAt, &c.RequiredPermission); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

// CreateResourceCategory inserts a new category at the end of the ordered list.
func CreateResourceCategory(ctx context.Context, pool *pgxpool.Pool, name string) (*ResourceCategory, error) {
	c := &ResourceCategory{}
	err := pool.QueryRow(ctx, `
		INSERT INTO resource_categories (name, position)
		VALUES ($1, (SELECT COALESCE(MAX(position), -1) + 1 FROM resource_categories))
		RETURNING id, name, position, created_at
	`, name).Scan(&c.ID, &c.Name, &c.Position, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateResourceCategory sets the name and optional required_permission for a category atomically.
// Pass nil for requiredPermission to clear the permission gate (allow all resources.view users).
// Because resource_library stores category_id (not the name), renames are instantly reflected in all reads.
func UpdateResourceCategory(ctx context.Context, pool *pgxpool.Pool, id, name string, requiredPermission *string) error {
	_, err := pool.Exec(ctx, `
		UPDATE resource_categories SET name = $1, required_permission = $2 WHERE id = $3
	`, name, requiredPermission, id)
	return err
}

// DeleteResourceCategory removes a category. Returns a foreign key error if any
// resources are still assigned to it — callers should surface this as a 409.
func DeleteResourceCategory(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM resource_categories WHERE id = $1`, id)
	return err
}

func scanResources(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]Resource, error) {
	var resources []Resource
	for rows.Next() {
		var r Resource
		if err := rows.Scan(&r.ID, &r.Title, &r.URL, &r.Category, &r.Description, &r.Position, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	return resources, rows.Err()
}

// HasResources returns true if at least one resource_library row exists.
func HasResources(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resource_library LIMIT 1)`).Scan(&exists)
	return exists, err
}
