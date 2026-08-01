package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/permissions"
)

type WebAdmin struct {
	ID                 string
	Email              string
	PasswordHash       string
	ForcePasswordReset bool
	UserID             *string
	CreatedAt          time.Time
}

// IsWebAdmin returns true if the user holds the admin.access permission
// (via their role group). This is the single source of truth for admin status.
func IsWebAdmin(ctx context.Context, pool *pgxpool.Pool, userID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM user_role_groups urg
			JOIN role_group_members rgm ON rgm.group_id = urg.group_id
			JOIN role_permissions rp ON rp.role_id = rgm.role_id
			JOIN permissions p ON p.id = rp.permission_id
			WHERE urg.user_id = $1 AND p.key = $2
		)
	`, userID, permissions.AdminAccess).Scan(&exists)
	return exists, err
}

// ListWebAdminUserIDs returns the IDs of all users holding the admin.access
// permission. Used to bulk-annotate user lists without a per-user query.
func ListWebAdminUserIDs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT urg.user_id
		FROM user_role_groups urg
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN role_permissions rp ON rp.role_id = rgm.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.key = $1
	`, permissions.AdminAccess)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountWebAdmins returns the total number of web admin accounts in the database.
func CountWebAdmins(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM web_admins`).Scan(&count)
	return count, err
}

// GetWebAdminByEmail looks up a web admin by email address. Returns ErrNotFound if absent.
func GetWebAdminByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*WebAdmin, error) {
	var a WebAdmin
	err := pool.QueryRow(ctx, `
		SELECT id, email, password_hash, force_password_reset, user_id, created_at FROM web_admins WHERE email = $1
	`, email).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.ForcePasswordReset, &a.UserID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetWebAdminByID looks up a web admin by their UUID. Returns ErrNotFound if absent.
func GetWebAdminByID(ctx context.Context, pool *pgxpool.Pool, id string) (*WebAdmin, error) {
	var a WebAdmin
	err := pool.QueryRow(ctx, `
		SELECT id, email, password_hash, force_password_reset, user_id, created_at FROM web_admins WHERE id = $1
	`, id).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.ForcePasswordReset, &a.UserID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListWebAdmins returns all web admins ordered by creation date.
func ListWebAdmins(ctx context.Context, pool *pgxpool.Pool) ([]WebAdmin, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, email, password_hash, force_password_reset, user_id, created_at FROM web_admins ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admins []WebAdmin
	for rows.Next() {
		var a WebAdmin
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.ForcePasswordReset, &a.UserID, &a.CreatedAt); err != nil {
			return nil, err
		}
		admins = append(admins, a)
	}
	return admins, rows.Err()
}

// ListWebAdminSlackIDs returns the Slack user IDs for all web admins
// that have a Slack identity.
func ListWebAdminSlackIDs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ui.external_id
		FROM user_role_groups urg
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN role_permissions rp ON rp.role_id = rgm.role_id
		JOIN permissions p ON p.id = rp.permission_id
		JOIN user_identities ui ON ui.user_id = urg.user_id
		WHERE p.key = $1 AND ui.provider = 'slack'
	`, permissions.AdminAccess)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateWebAdmin inserts a new web admin with a pre-hashed password.
// userID optionally links the admin to their Slack user record.
func CreateWebAdmin(ctx context.Context, pool *pgxpool.Pool, email, passwordHash string, forceReset bool, userID *string) (*WebAdmin, error) {
	var a WebAdmin
	err := pool.QueryRow(ctx, `
		INSERT INTO web_admins (email, password_hash, force_password_reset, user_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, email, password_hash, force_password_reset, user_id, created_at
	`, email, passwordHash, forceReset, userID).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.ForcePasswordReset, &a.UserID, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// UpdateAdminPassword sets a new bcrypt hash and clears force_password_reset.
func UpdateAdminPassword(ctx context.Context, pool *pgxpool.Pool, id, passwordHash string) error {
	_, err := pool.Exec(ctx, `
		UPDATE web_admins SET password_hash = $2, force_password_reset = FALSE WHERE id = $1
	`, id, passwordHash)
	return err
}

// DeleteWebAdmin removes a web admin by ID.
func DeleteWebAdmin(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM web_admins WHERE id = $1`, id)
	return err
}

// UpsertBootstrapAdmin ensures a web_admins row exists for the env-var bootstrap admin.
// Called at startup so the bootstrap admin always has a DB identity for audit attribution.
func UpsertBootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, username, passwordHash string) (*WebAdmin, error) {
	var a WebAdmin
	err := pool.QueryRow(ctx, `
		INSERT INTO web_admins (email, password_hash)
		VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash
		RETURNING id, email, password_hash, force_password_reset, user_id, created_at
	`, username, passwordHash).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.ForcePasswordReset, &a.UserID, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// PromoteUserToWebAdmin creates web credentials for a Slack-origin user and assigns the
// web_admin role. Creates a user_identities row (provider='web') if one doesn't exist yet.
// The caller is responsible for generating and hashing the temp password before calling this.
// The promoted user must change their password on first login.
func PromoteUserToWebAdmin(ctx context.Context, pool *pgxpool.Pool, userID, email, passwordHash string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Create web identity if missing.
	_, err = tx.Exec(ctx, `
		INSERT INTO user_identities (user_id, provider, external_id, username, display_name)
		SELECT $1, 'web', LOWER($2), $2, u.display_name FROM users u WHERE u.id = $1
		ON CONFLICT (provider, external_id) DO NOTHING
	`, userID, email)
	if err != nil {
		return err
	}

	// Upsert credentials with force reset.
	_, err = tx.Exec(ctx, `
		INSERT INTO user_credentials (user_id, password_hash, force_password_reset)
		VALUES ($1, $2, TRUE)
		ON CONFLICT (user_id) DO UPDATE
		    SET password_hash = EXCLUDED.password_hash,
		        force_password_reset = TRUE
	`, userID, passwordHash)
	if err != nil {
		return err
	}

	// Assign the administrators role group, which carries the admin.access
	// permission that IsWebAdmin and the login gate check.
	_, err = tx.Exec(ctx, `
		INSERT INTO user_role_groups (user_id, group_id)
		SELECT $1, g.id FROM role_groups g WHERE g.name = 'administrators'
		ON CONFLICT (user_id) DO UPDATE SET group_id = EXCLUDED.group_id
	`, userID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// UpsertBootstrapUser ensures a users-based credential exists for the env-var bootstrap admin.
// Creates a users row, user_identities row (provider='web'), user_credentials row,
// and assigns the admin role. Returns the users.id.
func UpsertBootstrapUser(ctx context.Context, pool *pgxpool.Pool, email, passwordHash string) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Resolve or create the users row via the web identity.
	var userID string
	err = tx.QueryRow(ctx, `
		SELECT user_id FROM user_identities WHERE provider = 'web' AND external_id = LOWER($1)
	`, email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Create users row then identity.
		err = tx.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ($1) RETURNING id`, email).Scan(&userID)
		if err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO user_identities (user_id, provider, external_id, username, display_name)
			VALUES ($1, 'web', LOWER($2), $2, $2)
			ON CONFLICT (provider, external_id) DO NOTHING
		`, userID, email)
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	// Upsert credentials (update password hash on re-bootstrap).
	_, err = tx.Exec(ctx, `
		INSERT INTO user_credentials (user_id, password_hash)
		VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash
	`, userID, passwordHash)
	if err != nil {
		return "", err
	}

	// Assign the administrators role group.
	_, err = tx.Exec(ctx, `
		INSERT INTO user_role_groups (user_id, group_id)
		SELECT $1, g.id FROM role_groups g WHERE g.name = 'administrators'
		ON CONFLICT (user_id) DO UPDATE SET group_id = EXCLUDED.group_id
	`, userID)
	if err != nil {
		return "", err
	}

	return userID, tx.Commit(ctx)
}

// HasAnyAdminCredentials returns true if at least one user has the admin.access
// permission and a user_credentials row. Used by main.go to decide whether to
// start the install wizard.
func HasAnyAdminCredentials(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM user_credentials uc
			JOIN user_role_groups urg ON urg.user_id = uc.user_id
			JOIN role_group_members rgm ON rgm.group_id = urg.group_id
			JOIN role_permissions rp ON rp.role_id = rgm.role_id
			JOIN permissions p ON p.id = rp.permission_id
			WHERE p.key = $1
		)
	`, permissions.AdminAccess).Scan(&exists)
	return exists, err
}
