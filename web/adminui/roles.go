package adminui

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
	admintempl "commons/web/templ"
)

func loadRolesPageData(ctx context.Context, pool *pgxpool.Pool) ([]admintempl.RoleDetailView, []store.Permission, []admintempl.RoleGroupView, error) {
	roles, err := store.ListRolesWithPermissions(ctx, pool)
	if err != nil {
		return nil, nil, nil, err
	}
	allPerms, err := store.ListAllPermissions(ctx, pool)
	if err != nil {
		return nil, nil, nil, err
	}
	members, err := store.ListRoleMembers(ctx, pool)
	if err != nil {
		return nil, nil, nil, err
	}
	membersByRole := map[string][]admintempl.RoleDetailMember{}
	for _, m := range members {
		email := ""
		if m.Email != nil {
			email = *m.Email
		}
		membersByRole[m.RoleName] = append(membersByRole[m.RoleName], admintempl.RoleDetailMember{
			DisplayName: m.DisplayName,
			Email:       email,
		})
	}

	var views []admintempl.RoleDetailView
	for _, r := range roles {
		desc := ""
		if r.Description != nil {
			desc = *r.Description
		}
		views = append(views, admintempl.RoleDetailView{
			ID:          r.ID,
			Name:        r.Name,
			DisplayName: r.DisplayName,
			Description: desc,
			SystemRole:  r.SystemRole,
			Permissions: r.Permissions,
			Members:     membersByRole[r.Name],
		})
	}

	groups, err := store.ListRoleGroupsWithRoles(ctx, pool)
	if err != nil {
		return nil, nil, nil, err
	}
	groupViews := make([]admintempl.RoleGroupView, len(groups))
	for i, g := range groups {
		desc := ""
		if g.Description != nil {
			desc = *g.Description
		}
		roleIDs := g.RoleIDs
		if roleIDs == nil {
			roleIDs = []string{}
		}
		groupViews[i] = admintempl.RoleGroupView{
			ID:          g.ID,
			Name:        g.Name,
			DisplayName: g.DisplayName,
			Description: desc,
			SystemGroup: g.SystemGroup,
			RoleIDs:     roleIDs,
		}
	}
	return views, allPerms, groupViews, nil
}

func (d Deps) RolesPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		views, allPerms, groups, err := loadRolesPageData(r.Context(), d.Pool)
		if err != nil {
			http.Error(w, "failed to load roles", http.StatusInternalServerError)
			return
		}
		selectedID := r.URL.Query().Get("role")
		admintempl.RolesPage(views, allPerms, groups, selectedID).Render(r.Context(), w)
	}
}

func (d Deps) RolesCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		if name == "" {
			FragmentError(w, r, "name required")
			return
		}
		desc := r.FormValue("description")
		role, err := store.CreateRole(ctx, d.Pool, name, desc)
		if err != nil {
			FragmentError(w, r, "failed to create role")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles?role="+role.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RolesUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		if name == "" {
			FragmentError(w, r, "name required")
			return
		}
		desc := r.FormValue("description")
		displayName := r.FormValue("display_name")
		if err := store.UpdateRole(ctx, d.Pool, roleID, name, displayName, desc); err != nil {
			FragmentError(w, r, "failed to update role")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles?role="+roleID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RolesDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")
		if err := store.DeleteRole(ctx, d.Pool, roleID); err != nil {
			FragmentError(w, r, "failed to delete role")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RolesDuplicate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roleID := r.PathValue("id")
		role, err := store.DuplicateRole(ctx, d.Pool, roleID)
		if err != nil {
			FragmentError(w, r, "failed to duplicate role")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles?role="+role.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RoleGroupsCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		if name == "" {
			FragmentError(w, r, "name required")
			return
		}
		displayName := r.FormValue("display_name")
		desc := r.FormValue("description")
		group, err := store.CreateRoleGroup(ctx, d.Pool, name, displayName, desc)
		if err != nil {
			FragmentError(w, r, "failed to create group")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles?group="+group.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RoleGroupsUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		groupID := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		if name == "" {
			FragmentError(w, r, "name required")
			return
		}
		displayName := r.FormValue("display_name")
		desc := r.FormValue("description")
		if err := store.UpdateRoleGroup(ctx, d.Pool, groupID, name, displayName, desc); err != nil {
			FragmentError(w, r, "failed to update group")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles?group="+groupID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RoleGroupsDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		groupID := r.PathValue("id")
		if err := store.DeleteRoleGroup(ctx, d.Pool, groupID); err != nil {
			FragmentError(w, r, "failed to delete group")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/roles")
		w.WriteHeader(http.StatusNoContent)
	}
}
