package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserCredentials holds the web-login credential row for a user.
type UserCredentials struct {
	UserID             string
	PasswordHash       *string
	ForcePasswordReset bool
	CreatedAt          time.Time
}

// GetUserByWebCredentials looks up a user by their web login email.
// Returns the user and their credential row. Returns ErrNotFound if no web identity exists for that email.
func GetUserByWebCredentials(ctx context.Context, pool *pgxpool.Pool, email string) (*User, *UserCredentials, error) {
	var u User
	var c UserCredentials
	err := pool.QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at,
		       uc.user_id, uc.password_hash, uc.force_password_reset, uc.created_at
		FROM user_identities ui
		JOIN users u ON u.id = ui.user_id
		JOIN user_credentials uc ON uc.user_id = u.id
		WHERE ui.provider = 'web' AND ui.external_id = LOWER($1) AND ui.platform_status != 'deactivated'
	`, email).Scan(
		&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt,
		&c.UserID, &c.PasswordHash, &c.ForcePasswordReset, &c.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return &u, &c, nil
}

// GetUserCredentialsByID looks up credentials by users.id directly.
// Returns ErrNotFound if no credential row exists for this user.
func GetUserCredentialsByID(ctx context.Context, pool *pgxpool.Pool, userID string) (*UserCredentials, error) {
	var c UserCredentials
	err := pool.QueryRow(ctx, `
		SELECT user_id, password_hash, force_password_reset, created_at
		FROM user_credentials WHERE user_id = $1
	`, userID).Scan(&c.UserID, &c.PasswordHash, &c.ForcePasswordReset, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CountUserCredentials returns the number of user_credentials rows.
func CountUserCredentials(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_credentials`).Scan(&count)
	return count, err
}

// UpsertUserCredentials sets password_hash for a user. Creates the row if absent.
func UpsertUserCredentials(ctx context.Context, pool *pgxpool.Pool, userID, passwordHash string, forceReset bool) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO user_credentials (user_id, password_hash, force_password_reset)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		    SET password_hash = EXCLUDED.password_hash,
		        force_password_reset = EXCLUDED.force_password_reset
	`, userID, passwordHash, forceReset)
	return err
}

// SetUserPassword updates the password hash and clears force_password_reset.
// It also activates any web identity that was in 'invited' status.
func SetUserPassword(ctx context.Context, pool *pgxpool.Pool, userID, passwordHash string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE user_credentials
		SET password_hash = $2, force_password_reset = FALSE
		WHERE user_id = $1
	`, userID, passwordHash); err != nil {
		return err
	}
	// Activate web identity if it was pending
	if _, err := tx.Exec(ctx, `
		UPDATE user_identities
		SET platform_status = 'active'
		WHERE user_id = $1 AND provider = 'web' AND platform_status = 'invited'
	`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateMemberAccount sets up web login credentials for a member.
// If a user with this email already exists (e.g. a Slack-origin user), the web identity
// and credentials are added to that user rather than creating a duplicate users row.
// Returns ErrAlreadyExists if a web identity with this email already exists.
func CreateMemberAccount(ctx context.Context, pool *pgxpool.Pool, displayName, email string) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	// Check if web identity already exists.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_identities WHERE provider = 'web' AND external_id = LOWER($1))`,
		email).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return "", ErrAlreadyExists
	}

	// Link to an existing user with this email (e.g. a Slack-origin user), or create one.
	var userID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM users WHERE email = LOWER($1)`,
		email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx,
			`INSERT INTO users (display_name, email) VALUES ($1, LOWER($2)) RETURNING id`,
			displayName, email).Scan(&userID); err != nil {
			return "", fmt.Errorf("create user: %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("lookup user by email: %w", err)
	}

	// Create web identity.
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_identities (user_id, provider, external_id, username, display_name, platform_status)
		 VALUES ($1, 'web', LOWER($2), $3, $4, 'invited')`,
		userID, email, email, displayName); err != nil {
		return "", fmt.Errorf("create identity: %w", err)
	}

	// Create empty credentials (NULL password_hash = pending activation).
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_credentials (user_id, password_hash, force_password_reset)
		 VALUES ($1, NULL, FALSE)
		 ON CONFLICT (user_id) DO NOTHING`,
		userID); err != nil {
		return "", fmt.Errorf("create credentials: %w", err)
	}

	// Assign the default Members role group.
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_role_groups (user_id, group_id)
		 SELECT $1, id FROM role_groups WHERE name = 'members'
		 ON CONFLICT (user_id) DO NOTHING`,
		userID); err != nil {
		return "", fmt.Errorf("assign default group: %w", err)
	}

	return userID, tx.Commit(ctx)
}

// BulkCreateMemberAccounts creates member accounts from a list of (DisplayName, Email) pairs.
// Returns the count of created accounts and a list of skipped emails (already exist).
func BulkCreateMemberAccounts(ctx context.Context, pool *pgxpool.Pool, members []struct{ DisplayName, Email string }) (int, []string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)

	created := 0
	var skipped []string
	for _, m := range members {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM user_identities WHERE provider = 'web' AND external_id = LOWER($1))`,
			m.Email).Scan(&exists); err != nil {
			return created, skipped, fmt.Errorf("check email %s: %w", m.Email, err)
		}
		if exists {
			skipped = append(skipped, m.Email)
			continue
		}

		// Link to an existing user with this email, or create one.
		var userID string
		err := tx.QueryRow(ctx,
			`SELECT id FROM users WHERE email = LOWER($1)`,
			m.Email).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx,
				`INSERT INTO users (display_name, email) VALUES ($1, LOWER($2)) RETURNING id`,
				m.DisplayName, m.Email).Scan(&userID); err != nil {
				return created, skipped, fmt.Errorf("create user %s: %w", m.Email, err)
			}
		} else if err != nil {
			return created, skipped, fmt.Errorf("lookup user %s: %w", m.Email, err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO user_identities (user_id, provider, external_id, username, display_name, platform_status) VALUES ($1, 'web', LOWER($2), $3, $4, 'invited')`,
			userID, m.Email, m.Email, m.DisplayName); err != nil {
			return created, skipped, fmt.Errorf("create identity %s: %w", m.Email, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_credentials (user_id, password_hash, force_password_reset) VALUES ($1, NULL, FALSE) ON CONFLICT (user_id) DO NOTHING`,
			userID); err != nil {
			return created, skipped, fmt.Errorf("create credentials %s: %w", m.Email, err)
		}
		// Assign the default Members role group.
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_role_groups (user_id, group_id)
			 SELECT $1, id FROM role_groups WHERE name = 'members'
			 ON CONFLICT (user_id) DO NOTHING`,
			userID); err != nil {
			return created, skipped, fmt.Errorf("assign default group %s: %w", m.Email, err)
		}
		created++
	}
	return created, skipped, tx.Commit(ctx)
}
