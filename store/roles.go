package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RoleWithPermissions extends Role with its assigned permission keys.
type RoleWithPermissions struct {
	Role
	Permissions []string
}

var ErrSystemRole = errors.New("cannot delete a system role")

// GetRole returns a single role by ID. Returns ErrNotFound if absent.
func GetRole(ctx context.Context, pool *pgxpool.Pool, roleID string) (*Role, error) {
	r := &Role{}
	err := pool.QueryRow(ctx,
		`SELECT id, name, display_name, description, system_role FROM roles WHERE id = $1`,
		roleID,
	).Scan(&r.ID, &r.Name, &r.DisplayName, &r.Description, &r.SystemRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// CreateRole inserts a new custom role and returns it.
func CreateRole(ctx context.Context, pool *pgxpool.Pool, name, description string) (*Role, error) {
	r := &Role{}
	var desc *string
	if description != "" {
		desc = &description
	}
	err := pool.QueryRow(ctx,
		`INSERT INTO roles (name, description, system_role) VALUES ($1, $2, FALSE) RETURNING id, name, description, system_role`,
		name, desc,
	).Scan(&r.ID, &r.Name, &r.Description, &r.SystemRole)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// UpdateRole updates the name, display name, and description of a role.
func UpdateRole(ctx context.Context, pool *pgxpool.Pool, roleID, name, displayName, description string) error {
	var desc *string
	if description != "" {
		desc = &description
	}
	_, err := pool.Exec(ctx,
		`UPDATE roles SET name = $1, display_name = $2, description = $3 WHERE id = $4`,
		name, displayName, desc, roleID,
	)
	return err
}

// DeleteRole deletes a custom role. Returns ErrSystemRole if the role is a system role.
// Cascade deletes role_permissions and user_roles via FK constraints.
func DeleteRole(ctx context.Context, pool *pgxpool.Pool, roleID string) error {
	var systemRole bool
	err := pool.QueryRow(ctx,
		`SELECT system_role FROM roles WHERE id = $1`,
		roleID,
	).Scan(&systemRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if systemRole {
		return ErrSystemRole
	}
	_, err = pool.Exec(ctx, `DELETE FROM roles WHERE id = $1`, roleID)
	return err
}

// DuplicateRole creates a custom role that copies the name, display name,
// description, and permission set of the source role. The new role's machine
// name is "<source>_copy" and display name is "<source> (copy)".
func DuplicateRole(ctx context.Context, pool *pgxpool.Pool, sourceRoleID string) (*Role, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var srcName, srcDisplayName string
	var srcDesc *string
	err = tx.QueryRow(ctx,
		`SELECT name, display_name, description FROM roles WHERE id = $1`,
		sourceRoleID,
	).Scan(&srcName, &srcDisplayName, &srcDesc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	newName := srcName + "_copy"
	newDisplayName := srcDisplayName
	if newDisplayName != "" {
		newDisplayName += " (copy)"
	}

	r := &Role{}
	err = tx.QueryRow(ctx,
		`INSERT INTO roles (name, display_name, description, system_role)
		 VALUES ($1, $2, $3, FALSE)
		 RETURNING id, name, display_name, description, system_role`,
		newName, newDisplayName, srcDesc,
	).Scan(&r.ID, &r.Name, &r.DisplayName, &r.Description, &r.SystemRole)
	if err != nil {
		return nil, err
	}

	// Copy permissions from the source role.
	_, err = tx.Exec(ctx,
		`INSERT INTO role_permissions (role_id, permission_id)
		 SELECT $1, permission_id FROM role_permissions WHERE role_id = $2
		 ON CONFLICT DO NOTHING`,
		r.ID, sourceRoleID,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// ListRolesWithPermissions returns all roles with their assigned permission keys.
func ListRolesWithPermissions(ctx context.Context, pool *pgxpool.Pool) ([]RoleWithPermissions, error) {
	rows, err := pool.Query(ctx, `
		SELECT r.id, r.name, r.display_name, r.description, r.system_role,
		       COALESCE(array_agg(p.key ORDER BY p.key) FILTER (WHERE p.key IS NOT NULL), '{}') AS permissions
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		LEFT JOIN permissions p ON p.id = rp.permission_id
		GROUP BY r.id
		ORDER BY r.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []RoleWithPermissions
	for rows.Next() {
		var rp RoleWithPermissions
		if err := rows.Scan(&rp.ID, &rp.Name, &rp.DisplayName, &rp.Description, &rp.SystemRole, &rp.Permissions); err != nil {
			return nil, err
		}
		roles = append(roles, rp)
	}
	return roles, rows.Err()
}

// AddRolePermission grants a permission (by key) to a role. No-ops if already granted.
func AddRolePermission(ctx context.Context, pool *pgxpool.Pool, roleID, permKey string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id)
		SELECT $1, id FROM permissions WHERE key = $2
		ON CONFLICT DO NOTHING
	`, roleID, permKey)
	return err
}

// RemoveRolePermission revokes a permission (by key) from a role.
func RemoveRolePermission(ctx context.Context, pool *pgxpool.Pool, roleID, permKey string) error {
	_, err := pool.Exec(ctx, `
		DELETE FROM role_permissions
		WHERE role_id = $1
		  AND permission_id = (SELECT id FROM permissions WHERE key = $2)
	`, roleID, permKey)
	return err
}

// RoleMember is a user grouped under a role, for the roles admin view.
type RoleMember struct {
	UserID      string
	DisplayName string
	Email       *string
	RoleName    string
}

// ListRoleMembers returns all non-bot users paired with the role names they hold
// through their role group, ordered by role then display name. Used to populate the
// roles detail view.
func ListRoleMembers(ctx context.Context, pool *pgxpool.Pool) ([]RoleMember, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, r.name
		FROM user_role_groups urg
		JOIN users u ON u.id = urg.user_id
		JOIN role_group_members rgm ON rgm.group_id = urg.group_id
		JOIN roles r ON r.id = rgm.role_id
		WHERE NOT u.bot
		ORDER BY r.name, u.display_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []RoleMember
	for rows.Next() {
		var m RoleMember
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email, &m.RoleName); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
