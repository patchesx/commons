package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID                 string
	DisplayName        string
	Email              *string
	Bot                bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	PlatformStatus     string  // most active status across all identities; empty if no identities
	SlackID            string  // external_id from user_identities where provider='slack'; empty if none
	SelectedCalendarID *string // user's preferred calendar for App Home display; nil = default
}

type Role struct {
	ID          string
	Name        string
	DisplayName string
	Description *string
	SystemRole  bool
}

// GetZoomSentinelUserID returns the UUID of the Zoom bot user (display_name='Zoom', bot=true).
// Returns store.ErrNotFound if the sentinel row is missing (zoom sentinel user migration not applied).
func GetZoomSentinelUserID(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var id string
	err := pool.QueryRow(ctx,
		`SELECT id FROM users WHERE display_name = 'Zoom' AND bot = TRUE LIMIT 1`,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetOrCreateUserByIdentity upserts a provider identity and returns the linked user.
// On first-ever call for a (provider, externalID) pair a new users row is created.
// Concurrent first-time calls for the same pair are serialized by the UNIQUE constraint
// on user_identities; any orphan users row is cleaned up inside the transaction.
func GetOrCreateUserByIdentity(ctx context.Context, pool *pgxpool.Pool, provider, externalID, username, displayName string) (*User, error) {
	// Fast path: identity already exists.
	var userID string
	err := pool.QueryRow(ctx,
		`SELECT user_id FROM user_identities WHERE provider = $1 AND external_id = $2`,
		provider, externalID,
	).Scan(&userID)

	if err == nil {
		// Update cached names on the identity row and the user's canonical display_name.
		bestName := displayName
		if bestName == "" {
			bestName = username
		}
		if bestName != "" {
			pool.Exec(ctx,
				`UPDATE user_identities SET username = $1, display_name = $2 WHERE provider = $3 AND external_id = $4`,
				username, displayName, provider, externalID,
			)
			pool.Exec(ctx,
				`UPDATE users SET display_name = $1, updated_at = NOW() WHERE id = $2`,
				bestName, userID,
			)
		}
		return GetUserByID(ctx, pool, userID)
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Slow path: new identity — create user + identity in a transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	bestName := displayName
	if bestName == "" {
		bestName = username
	}
	if bestName == "" {
		bestName = externalID
	}

	var newUserID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (display_name) VALUES ($1) RETURNING id`,
		bestName,
	).Scan(&newUserID); err != nil {
		return nil, err
	}

	// ON CONFLICT handles the rare race where two goroutines create the same identity
	// simultaneously. The RETURNING clause gives us whichever user_id won.
	var finalUserID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO user_identities (user_id, provider, external_id, username, display_name)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider, external_id) DO UPDATE
		    SET username = EXCLUDED.username, display_name = EXCLUDED.display_name
		RETURNING user_id
	`, newUserID, provider, externalID, username, displayName).Scan(&finalUserID); err != nil {
		return nil, err
	}

	// If the conflict resolution kept a pre-existing user, delete the orphan we just created.
	if finalUserID != newUserID {
		tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, newUserID)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return GetUserByID(ctx, pool, finalUserID)
}

// GetOrCreateUserByIdentityMergingEmail is like GetOrCreateUserByIdentity but when
// no existing identity is found for (provider, externalID), it first checks whether
// a web identity already exists for the given email. If one does, the new provider
// identity is attached to that existing user instead of creating a duplicate users row.
// Pass email="" to skip the merge check and behave identically to GetOrCreateUserByIdentity.
func GetOrCreateUserByIdentityMergingEmail(ctx context.Context, pool *pgxpool.Pool, provider, externalID, username, displayName, email string) (*User, error) {
	// Fast path: identity already exists.
	var userID string
	err := pool.QueryRow(ctx,
		`SELECT user_id FROM user_identities WHERE provider = $1 AND external_id = $2`,
		provider, externalID,
	).Scan(&userID)

	if err == nil {
		bestName := displayName
		if bestName == "" {
			bestName = username
		}
		if bestName != "" {
			pool.Exec(ctx,
				`UPDATE user_identities SET username = $1, display_name = $2 WHERE provider = $3 AND external_id = $4`,
				username, displayName, provider, externalID,
			)
			pool.Exec(ctx,
				`UPDATE users SET display_name = $1, updated_at = NOW() WHERE id = $2`,
				bestName, userID,
			)
		}
		return GetUserByID(ctx, pool, userID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// If an email was provided, check for an existing web identity to merge with.
	if email != "" {
		var webUserID string
		mergeErr := pool.QueryRow(ctx,
			`SELECT user_id FROM user_identities WHERE provider = 'web' AND external_id = LOWER($1)`,
			email,
		).Scan(&webUserID)
		if mergeErr == nil {
			// Link the new provider identity to the existing web user.
			tx, err := pool.Begin(ctx)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback(ctx) //nolint:errcheck

			bestName := displayName
			if bestName == "" {
				bestName = username
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_identities (user_id, provider, external_id, username, display_name)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (provider, external_id) DO UPDATE
				    SET username = EXCLUDED.username, display_name = EXCLUDED.display_name
			`, webUserID, provider, externalID, username, displayName); err != nil {
				return nil, err
			}
			if bestName != "" {
				tx.Exec(ctx, `UPDATE users SET display_name = $1, updated_at = NOW() WHERE id = $2`, bestName, webUserID)
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return GetUserByID(ctx, pool, webUserID)
		} else if !errors.Is(mergeErr, pgx.ErrNoRows) {
			return nil, mergeErr
		}
	}

	// No existing identity and no email match — fall through to create a new user.
	return GetOrCreateUserByIdentity(ctx, pool, provider, externalID, username, displayName)
}

// GetUserByExternalID looks up a user by their provider identity.
// Returns ErrNotFound if absent.
func GetUserByExternalID(ctx context.Context, pool *pgxpool.Pool, provider, externalID string) (*User, error) {
	u := &User{}
	err := pool.QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM users u
		JOIN user_identities ui ON ui.user_id = u.id
		WHERE ui.provider = $1 AND ui.external_id = $2
	`, provider, externalID).Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserExternalID returns the provider-specific external ID for a user.
// Returns ErrNotFound if the user has no identity for the given provider.
func GetUserExternalID(ctx context.Context, pool *pgxpool.Pool, userID, provider string) (string, error) {
	var externalID string
	err := pool.QueryRow(ctx,
		`SELECT external_id FROM user_identities WHERE user_id = $1 AND provider = $2`,
		userID, provider,
	).Scan(&externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return externalID, err
}

// LinkIdentity attaches a provider identity to an existing user. Unlike
// GetOrCreateUserByIdentity it never creates a users row — it only inserts or refreshes an
// identity row for the given userID. Used to cache an external profile ID (e.g. the
// SolidarityTech user id) on a member that was already resolved via another identity.
// On conflict (provider, external_id) the external_name/external_email are refreshed but the
// user_id linkage is left untouched.
func LinkIdentity(ctx context.Context, pool *pgxpool.Pool, userID, provider, externalID, externalName, externalEmail string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO user_identities (user_id, provider, external_id, external_name, external_email)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (provider, external_id) DO UPDATE
		    SET external_name  = EXCLUDED.external_name,
		        external_email = EXCLUDED.external_email,
		        updated_at     = NOW()
	`, userID, provider, externalID, externalName, externalEmail)
	return err
}

// GetUserByID returns a user by their internal UUID. Returns ErrNotFound if absent.
func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id string) (*User, error) {
	u := &User{}
	err := pool.QueryRow(ctx, `
		SELECT id, display_name, email, bot, created_at, updated_at, selected_calendar_id
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt, &u.SelectedCalendarID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpdateUserEmail stores a lowercase email on the user record if it differs from the current value.
// No-ops if email is empty. Unique-violation conflicts (another user already has that email)
// are silently ignored — the first record wins.
func UpdateUserEmail(ctx context.Context, pool *pgxpool.Pool, userID, email string) error {
	if email == "" {
		return nil
	}
	lower := strings.ToLower(email)
	_, err := pool.Exec(ctx,
		`UPDATE users SET email = $1, updated_at = NOW()
		 WHERE id = $2 AND (email IS NULL OR email != $1)`,
		lower, userID,
	)
	if err != nil {
		// Unique violation: another user already owns this email — skip silently.
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			return nil
		}
		return err
	}
	return nil
}

// UpdateUserDisplayName sets the display name on the user record and keeps the
// matching 'web' identity in sync. The identity update is best-effort: if no web
// identity exists (e.g. a Slack-only user) it is a no-op, not an error.
func UpdateUserDisplayName(ctx context.Context, pool *pgxpool.Pool, userID, displayName string) error {
	if _, err := pool.Exec(ctx, `UPDATE users SET display_name = $1, updated_at = NOW() WHERE id = $2`,
		displayName, userID); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `UPDATE user_identities SET display_name = $1 WHERE user_id = $2 AND provider = 'web'`,
		displayName, userID)
	return err
}

// WebIdentityExists reports whether a 'web' identity exists for the given email
// (case-insensitive). Used to guard self-registration against duplicate emails.
func WebIdentityExists(ctx context.Context, pool *pgxpool.Pool, email string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_identities WHERE provider = 'web' AND external_id = LOWER($1))`,
		email).Scan(&exists)
	return exists, err
}

// ListWebIdentityUserIDs returns the set of users.id values that have a 'web' identity.
func ListWebIdentityUserIDs(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT user_id FROM user_identities WHERE provider = 'web'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		set[uid] = true
	}
	return set, rows.Err()
}

// GetUserByEmail looks up a user by their stored email address (case-insensitive).
// Returns ErrNotFound if absent.
func GetUserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*User, error) {
	u := &User{}
	err := pool.QueryRow(ctx, `
		SELECT id, display_name, email, bot, created_at, updated_at
		FROM users WHERE email = LOWER($1)
	`, email).Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpdateUserSelectedCalendar sets the user's preferred calendar for App Home display.
// Pass an empty string to clear the selection (revert to default).
func UpdateUserSelectedCalendar(ctx context.Context, pool *pgxpool.Pool, userID, calendarID string) error {
	var calIDParam *string
	if calendarID != "" {
		calIDParam = &calendarID
	}
	_, err := pool.Exec(ctx,
		`UPDATE users SET selected_calendar_id = $1, updated_at = NOW() WHERE id = $2`,
		calIDParam, userID,
	)
	return err
}

// GetUserPermissions returns the permission keys granted to a user through their
// role group (group -> roles -> permissions).
func GetUserPermissions(ctx context.Context, pool *pgxpool.Pool, userID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT p.key
		FROM user_role_groups urg
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN role_permissions rp ON rp.role_id = rgm.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE urg.user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// GetUserRoles returns the role names granted to a user through their role group.
func GetUserRoles(ctx context.Context, pool *pgxpool.Pool, userID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT r.name
		FROM user_role_groups urg
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN roles r ON r.id = rgm.role_id
		WHERE urg.user_id = $1
		ORDER BY r.name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// SearchUsers returns up to 20 non-bot users whose display_name matches the query.
func SearchUsers(ctx context.Context, pool *pgxpool.Pool, query string) ([]User, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM users u
		WHERE NOT u.bot
		  AND u.display_name ILIKE '%' || $1 || '%'
		ORDER BY u.display_name
		LIMIT 20
	`, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ListUsers returns all non-bot users ordered by creation time.
// PlatformStatus is set to the most active status across all identities
// (active > invited > deactivated > unknown).
func ListUsers(ctx context.Context, pool *pgxpool.Pool) ([]User, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at,
		    COALESCE(
		        (SELECT ui.platform_status
		         FROM user_identities ui
		         WHERE ui.user_id = u.id
		         ORDER BY CASE ui.platform_status
		             WHEN 'active'      THEN 4
		             WHEN 'invited'     THEN 3
		             WHEN 'deactivated' THEN 2
		             ELSE 1
		         END DESC
		         LIMIT 1),
		        'unknown'
		    ) AS platform_status,
		    COALESCE(
		        (SELECT external_id FROM user_identities
		         WHERE user_id = u.id AND provider = 'slack' LIMIT 1),
		        ''
		    ) AS slack_id
		FROM users u
		WHERE NOT u.bot
		ORDER BY u.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt, &u.PlatformStatus, &u.SlackID); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateIdentityStatus sets the platform_status for a specific provider identity.
func UpdateIdentityStatus(ctx context.Context, pool *pgxpool.Pool, provider, externalID, status string) error {
	_, err := pool.Exec(ctx,
		`UPDATE user_identities SET platform_status = $1 WHERE provider = $2 AND external_id = $3`,
		status, provider, externalID,
	)
	return err
}

// ListRoles returns all defined roles.
func ListRoles(ctx context.Context, pool *pgxpool.Pool) ([]Role, error) {
	rows, err := pool.Query(ctx, `SELECT id, name, display_name, description, system_role FROM roles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.DisplayName, &r.Description, &r.SystemRole); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

type Permission struct {
	Key         string  `json:"key"`
	Description *string `json:"description"`
}

// ListAllPermissions returns every permission defined in the system, ordered alphabetically.
func ListAllPermissions(ctx context.Context, pool *pgxpool.Pool) ([]Permission, error) {
	rows, err := pool.Query(ctx, `SELECT key, description FROM permissions ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []Permission
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Key, &p.Description); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

// ListUsersWithRoleName returns all users who hold a given role by name (e.g. "owner"),
// resolved through their role group.
func ListUsersWithRoleName(ctx context.Context, pool *pgxpool.Pool, roleName string) ([]User, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM users u
		JOIN user_role_groups urg ON urg.user_id = u.id
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN roles r ON r.id = rgm.role_id
		WHERE r.name = $1
		ORDER BY u.created_at ASC
	`, roleName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ListUsersWithPermission returns all users who hold a given permission key through
// their role group (group -> roles -> permissions).
func ListUsersWithPermission(ctx context.Context, pool *pgxpool.Pool, permissionKey string) ([]User, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM users u
		JOIN user_role_groups urg ON urg.user_id = u.id
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN role_permissions rp ON rp.role_id = rgm.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.key = $1
		ORDER BY u.created_at ASC
	`, permissionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
