package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Contact struct {
	ID         string
	UserID     string
	ExternalID string // provider's opaque user ID (e.g. Slack UID) — from user_identities
	Username   string // provider's display name — from user_identities
	Title      string
	Position   int
	CreatedAt  time.Time
}

// ListContacts returns all contacts joined with Slack identity info, ordered by position.
func ListContacts(ctx context.Context, pool *pgxpool.Pool) ([]Contact, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.user_id, COALESCE(ui.external_id, ''), COALESCE(ui.username, u.display_name),
		       c.title, c.position, c.created_at
		FROM contacts c
		JOIN users u ON u.id = c.user_id
		LEFT JOIN user_identities ui ON ui.user_id = u.id AND ui.provider = 'slack'
		ORDER BY c.position, c.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.UserID, &c.ExternalID, &c.Username, &c.Title, &c.Position, &c.CreatedAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// CreateContact inserts a new contact and populates c.ID and c.CreatedAt.
func CreateContact(ctx context.Context, pool *pgxpool.Pool, c *Contact) error {
	return pool.QueryRow(ctx, `
		INSERT INTO contacts (user_id, title, position)
		VALUES ($1, $2, $3)
		RETURNING id, created_at
	`, c.UserID, c.Title, c.Position).Scan(&c.ID, &c.CreatedAt)
}

// UpdateContact updates the title and position for an existing contact.
func UpdateContact(ctx context.Context, pool *pgxpool.Pool, c *Contact) error {
	_, err := pool.Exec(ctx, `
		UPDATE contacts SET title = $1, position = $2 WHERE id = $3
	`, c.Title, c.Position, c.ID)
	return err
}

// DeleteContact removes a contact by ID.
func DeleteContact(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM contacts WHERE id = $1`, id)
	return err
}
