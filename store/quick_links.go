package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type QuickLink struct {
	ID        string
	Label     string
	URL       string
	Position  int
	Active    bool
	CreatedAt time.Time
}

// ListQuickLinks returns all quick links ordered by position — used by the admin page.
func ListQuickLinks(ctx context.Context, pool *pgxpool.Pool) ([]QuickLink, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, label, url, position, active, created_at
		FROM quick_links
		ORDER BY position, created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuickLinks(rows)
}

// ListActiveQuickLinks returns only active quick links — used by the Slack App Home.
func ListActiveQuickLinks(ctx context.Context, pool *pgxpool.Pool) ([]QuickLink, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, label, url, position, active, created_at
		FROM quick_links
		WHERE active = TRUE
		ORDER BY position, created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuickLinks(rows)
}

// CreateQuickLink inserts a new quick link and populates l.ID and l.CreatedAt.
func CreateQuickLink(ctx context.Context, pool *pgxpool.Pool, l *QuickLink) error {
	return pool.QueryRow(ctx, `
		INSERT INTO quick_links (label, url, position, active)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, l.Label, l.URL, l.Position, l.Active).Scan(&l.ID, &l.CreatedAt)
}

// UpdateQuickLink updates all mutable fields for a quick link.
func UpdateQuickLink(ctx context.Context, pool *pgxpool.Pool, l *QuickLink) error {
	_, err := pool.Exec(ctx, `
		UPDATE quick_links
		SET label = $1, url = $2, position = $3, active = $4
		WHERE id = $5
	`, l.Label, l.URL, l.Position, l.Active, l.ID)
	return err
}

// DeleteQuickLink removes a quick link by ID.
func DeleteQuickLink(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM quick_links WHERE id = $1`, id)
	return err
}

func scanQuickLinks(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]QuickLink, error) {
	var links []QuickLink
	for rows.Next() {
		var l QuickLink
		if err := rows.Scan(&l.ID, &l.Label, &l.URL, &l.Position, &l.Active, &l.CreatedAt); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}
