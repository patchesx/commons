package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RoleGroup is a named bundle of roles assigned to users as a unit.
type RoleGroup struct {
	ID          string
	Name        string
	DisplayName string
	Description *string
	SystemGroup bool
	SortOrder   int
}

// RoleGroupWithRoles extends RoleGroup with its member role IDs.
type RoleGroupWithRoles struct {
	RoleGroup
	RoleIDs []string
}

var ErrSystemGroup = errors.New("cannot delete a system group")

// GetRoleGroup returns a single role group by ID. Returns ErrNotFound if absent.
func GetRoleGroup(ctx context.Context, pool *pgxpool.Pool, groupID string) (*RoleGroup, error) {
	g := &RoleGroup{}
	err := pool.QueryRow(ctx,
		`SELECT id, name, display_name, description, system_group, sort_order
		 FROM role_groups WHERE id = $1`,
		groupID,
	).Scan(&g.ID, &g.Name, &g.DisplayName, &g.Description, &g.SystemGroup, &g.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// CreateRoleGroup inserts a new custom role group and returns it.
func CreateRoleGroup(ctx context.Context, pool *pgxpool.Pool, name, displayName, description string) (*RoleGroup, error) {
	g := &RoleGroup{}
	var desc *string
	if description != "" {
		desc = &description
	}
	err := pool.QueryRow(ctx,
		`INSERT INTO role_groups (name, display_name, description, system_group)
		 VALUES ($1, $2, $3, FALSE)
		 RETURNING id, name, display_name, description, system_group, sort_order`,
		name, displayName, desc,
	).Scan(&g.ID, &g.Name, &g.DisplayName, &g.Description, &g.SystemGroup, &g.SortOrder)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// UpdateRoleGroup updates the name, display name, and description of a role group.
func UpdateRoleGroup(ctx context.Context, pool *pgxpool.Pool, groupID, name, displayName, description string) error {
	var desc *string
	if description != "" {
		desc = &description
	}
	_, err := pool.Exec(ctx,
		`UPDATE role_groups SET name = $1, display_name = $2, description = $3 WHERE id = $4`,
		name, displayName, desc, groupID,
	)
	return err
}

// DeleteRoleGroup deletes a custom role group. Returns ErrSystemGroup for system groups.
// Cascade removes role_group_members and user_role_groups via FK constraints.
func DeleteRoleGroup(ctx context.Context, pool *pgxpool.Pool, groupID string) error {
	var systemGroup bool
	err := pool.QueryRow(ctx,
		`SELECT system_group FROM role_groups WHERE id = $1`,
		groupID,
	).Scan(&systemGroup)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if systemGroup {
		return ErrSystemGroup
	}
	_, err = pool.Exec(ctx, `DELETE FROM role_groups WHERE id = $1`, groupID)
	return err
}

// ListRoleGroups returns all role groups ordered by sort_order then name.
func ListRoleGroups(ctx context.Context, pool *pgxpool.Pool) ([]RoleGroup, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, name, display_name, description, system_group, sort_order
		 FROM role_groups ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []RoleGroup
	for rows.Next() {
		var g RoleGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.DisplayName, &g.Description, &g.SystemGroup, &g.SortOrder); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// ListRoleGroupsWithRoles returns all role groups with their member role IDs.
func ListRoleGroupsWithRoles(ctx context.Context, pool *pgxpool.Pool) ([]RoleGroupWithRoles, error) {
	rows, err := pool.Query(ctx, `
		SELECT g.id, g.name, g.display_name, g.description, g.system_group, g.sort_order,
		       COALESCE(array_agg(rgm.role_id ORDER BY rgm.role_id) FILTER (WHERE rgm.role_id IS NOT NULL), '{}') AS role_ids
		FROM role_groups g
		LEFT JOIN role_group_members rgm ON rgm.group_id = g.id
		GROUP BY g.id
		ORDER BY g.sort_order, g.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []RoleGroupWithRoles
	for rows.Next() {
		var g RoleGroupWithRoles
		if err := rows.Scan(&g.ID, &g.Name, &g.DisplayName, &g.Description, &g.SystemGroup, &g.SortOrder, &g.RoleIDs); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// AddRoleToGroup adds a role to a group. No-ops if already a member.
func AddRoleToGroup(ctx context.Context, pool *pgxpool.Pool, groupID, roleID string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO role_group_members (group_id, role_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		groupID, roleID)
	return err
}

// RemoveRoleFromGroup removes a role from a group.
func RemoveRoleFromGroup(ctx context.Context, pool *pgxpool.Pool, groupID, roleID string) error {
	_, err := pool.Exec(ctx,
		`DELETE FROM role_group_members WHERE group_id = $1 AND role_id = $2`,
		groupID, roleID)
	return err
}

// AssignGroupToUser sets a user's role group (upsert — one group per user).
func AssignGroupToUser(ctx context.Context, pool *pgxpool.Pool, userID, groupID string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO user_role_groups (user_id, group_id) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET group_id = EXCLUDED.group_id`,
		userID, groupID)
	return err
}

// RemoveGroupFromUser removes a user's role group assignment.
func RemoveGroupFromUser(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM user_role_groups WHERE user_id = $1`, userID)
	return err
}

// GetUserGroup returns the role group assigned to a user, or ErrNotFound if none.
func GetUserGroup(ctx context.Context, pool *pgxpool.Pool, userID string) (*RoleGroup, error) {
	g := &RoleGroup{}
	err := pool.QueryRow(ctx, `
		SELECT g.id, g.name, g.display_name, g.description, g.system_group, g.sort_order
		FROM user_role_groups urg
		JOIN role_groups g ON g.id = urg.group_id
		WHERE urg.user_id = $1`,
		userID,
	).Scan(&g.ID, &g.Name, &g.DisplayName, &g.Description, &g.SystemGroup, &g.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}
