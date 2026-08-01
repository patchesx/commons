package adminui

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
	admintempl "commons/web/templ"
)

const usersPageLimit = 25

func loadUsersPageData(ctx context.Context, pool *pgxpool.Pool, statusFilter, search string, page int) ([]admintempl.UserViewRow, int, int, error) {
	total, err := store.CountUsersPage(ctx, pool, statusFilter, search)
	if err != nil {
		return nil, 0, 0, err
	}
	offset := (page - 1) * usersPageLimit
	users, err := store.ListUsersPage(ctx, pool, statusFilter, search, usersPageLimit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	totalPages := (total + usersPageLimit - 1) / usersPageLimit
	if totalPages < 1 {
		totalPages = 1
	}

	allGroups, err := store.ListRoleGroups(ctx, pool)
	if err != nil {
		return nil, 0, 0, err
	}
	groupRefs := make([]admintempl.GroupRef, len(allGroups))
	for i, g := range allGroups {
		groupRefs[i] = admintempl.GroupRef{ID: g.ID, Name: g.Name, DisplayName: g.DisplayName}
	}
	adminIDs, err := store.ListWebAdminUserIDs(ctx, pool)
	if err != nil {
		return nil, 0, 0, err
	}
	webAdminSet := map[string]bool{}
	for _, id := range adminIDs {
		webAdminSet[id] = true
	}

	webIdentitySet, err := store.ListWebIdentityUserIDs(ctx, pool)
	if err != nil {
		return nil, 0, 0, err
	}

	result := make([]admintempl.UserViewRow, 0, len(users))
	for _, u := range users {
		var groupRef *admintempl.GroupRef
		group, err := store.GetUserGroup(ctx, pool, u.ID)
		if err == nil && group != nil {
			groupRef = &admintempl.GroupRef{ID: group.ID, Name: group.Name, DisplayName: group.DisplayName}
		}
		email := ""
		if u.Email != nil {
			email = *u.Email
		}
		result = append(result, admintempl.UserViewRow{
			ID:             u.ID,
			DisplayName:    u.DisplayName,
			Email:          email,
			PlatformStatus: u.PlatformStatus,
			IsWebAdmin:     webAdminSet[u.ID],
			HasWebIdentity: webIdentitySet[u.ID],
			Group:          groupRef,
			AllGroups:      groupRefs,
		})
	}
	return result, total, totalPages, nil
}

func (d Deps) UsersPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statusFilter := r.URL.Query().Get("status")
		search := strings.TrimSpace(r.URL.Query().Get("q"))
		page := parseUsersPageParam(r)
		rows, total, totalPages, err := loadUsersPageData(r.Context(), d.Pool, statusFilter, search, page)
		if err != nil {
			http.Error(w, "failed to load users", http.StatusInternalServerError)
			return
		}
		admintempl.UsersPage(rows, statusFilter, search, page, totalPages, total).Render(r.Context(), w)
	}
}

func (d Deps) UsersTable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statusFilter := r.URL.Query().Get("status")
		search := strings.TrimSpace(r.URL.Query().Get("q"))
		page := parseUsersPageParam(r)
		rows, total, totalPages, err := loadUsersPageData(r.Context(), d.Pool, statusFilter, search, page)
		if err != nil {
			FragmentError(w, r, "failed to load users")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.UsersTable(rows, statusFilter, search, page, totalPages, total).Render(r.Context(), w)
	}
}

// parseUsersPageParam extracts the "page" query parameter, defaulting to 1 when
// absent or invalid.
func parseUsersPageParam(r *http.Request) int {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	return page
}

func (d Deps) CreateMember() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			inlineHelp(w, r, "danger", "Invalid form submission.")
			return
		}
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		email := strings.TrimSpace(r.FormValue("email"))
		if displayName == "" || email == "" {
			inlineHelp(w, r, "danger", "Name and email are required.")
			return
		}
		_, err := store.CreateMemberAccount(ctx, d.Pool, displayName, email)
		if err == store.ErrAlreadyExists {
			inlineHelp(w, r, "warning", email+" is already registered.")
			return
		}
		if err != nil {
			log.Printf("adminui/users: create member: %v", err)
			inlineHelp(w, r, "danger", "Failed to create member.")
			return
		}
		inlineHelp(w, r, "success", fmt.Sprintf("Member %s added.", displayName))
	}
}

func (d Deps) ImportMembers() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			inlineHelp(w, r, "danger", "File too large — the limit is 1 MB.")
			return
		}
		file, _, err := r.FormFile("csv")
		if err != nil {
			inlineHelp(w, r, "danger", "Choose a CSV file first.")
			return
		}
		defer file.Close()
		reader := csv.NewReader(file)
		records, err := reader.ReadAll()
		if err != nil {
			inlineHelp(w, r, "danger", "Invalid CSV file.")
			return
		}
		var members []struct{ DisplayName, Email string }
		seen := map[string]bool{}
		for _, rec := range records {
			if len(rec) < 2 {
				continue
			}
			name := strings.TrimSpace(rec[0])
			email := strings.ToLower(strings.TrimSpace(rec[1]))
			if name == "" || email == "" || seen[email] {
				continue
			}
			seen[email] = true
			members = append(members, struct{ DisplayName, Email string }{name, email})
		}
		if len(members) == 0 {
			inlineHelp(w, r, "warning", "No valid rows found in CSV.")
			return
		}
		if len(members) > 10000 {
			members = members[:10000]
		}
		created, skipped, err := store.BulkCreateMemberAccounts(ctx, d.Pool, members)
		if err != nil {
			log.Printf("adminui/users: bulk import: %v", err)
			inlineHelp(w, r, "danger", "Import failed: "+err.Error())
			return
		}
		if len(skipped) > 0 {
			inlineHelp(w, r, "success", fmt.Sprintf("Imported %d member(s). Skipped %d already registered: %s",
				created, len(skipped), strings.Join(skipped, ", ")))
		} else {
			inlineHelp(w, r, "success", fmt.Sprintf("Imported %d member(s).", created))
		}
	}
}

func (d Deps) ResetLink() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("id")
		token, err := store.CreatePasswordResetToken(ctx, d.Pool, userID)
		if err != nil {
			log.Printf("adminui/users: generate reset link for %s: %v", userID, err)
			inlineHelp(w, r, "danger", "Failed to generate reset link.")
			return
		}
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		link := fmt.Sprintf("%s://%s/reset-password?token=%s", scheme, r.Host, token)
		w.Header().Set("Content-Type", "text/html")
		admintempl.ResetLinkGenerated(link).Render(ctx, w)
	}
}

func (d Deps) AssignGroup() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		groupID := r.FormValue("groupId")
		if groupID == "" {
			FragmentError(w, r, "groupId required")
			return
		}
		if err := store.AssignGroupToUser(r.Context(), d.Pool, userID, groupID); err != nil {
			FragmentError(w, r, "failed to assign group")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/users")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) RemoveGroup() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		if err := store.RemoveGroupFromUser(r.Context(), d.Pool, userID); err != nil {
			FragmentError(w, r, "failed to remove group")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/users")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) PromoteModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userId")
		user, err := store.GetUserByID(r.Context(), d.Pool, userID)
		if err != nil || user == nil {
			FragmentError(w, r, "user not found")
			return
		}
		email := ""
		if user.Email != nil {
			email = *user.Email
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.PromoteModal(userID, user.DisplayName, email, "").Render(r.Context(), w)
	}
}

func (d Deps) PromoteSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("userId")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		email := r.FormValue("email")
		if email == "" {
			user, _ := store.GetUserByID(ctx, d.Pool, userID)
			name := ""
			if user != nil {
				name = user.DisplayName
				if user.Email != nil {
					email = *user.Email
				}
			}
			w.Header().Set("Content-Type", "text/html")
			admintempl.PromoteModal(userID, name, email, "Email is required").Render(ctx, w)
			return
		}
		user, err := store.GetUserByID(ctx, d.Pool, userID)
		if err != nil || user == nil {
			FragmentError(w, r, "user not found")
			return
		}
		already, err := store.IsWebAdmin(ctx, d.Pool, userID)
		if err != nil {
			FragmentError(w, r, "internal error")
			return
		}
		if already {
			w.Header().Set("Content-Type", "text/html")
			admintempl.PromoteModal(userID, user.DisplayName, email, "User is already a web admin.").Render(ctx, w)
			return
		}
		if err := promoteUserToAdmin(ctx, d.Pool, d.EncKey, d.Notifier, userID, email); err != nil {
			w.Header().Set("Content-Type", "text/html")
			admintempl.PromoteModal(userID, user.DisplayName, email, "Failed to promote user: "+err.Error()).Render(ctx, w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/users")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) ChannelApproversModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("userId")
		user, err := store.GetUserByID(ctx, d.Pool, userID)
		if err != nil || user == nil {
			FragmentError(w, r, "user not found")
			return
		}
		channels, err := store.ListSlackChannels(ctx, d.Pool)
		if err != nil {
			FragmentError(w, r, "failed to load channels")
			return
		}
		assignedIDs, err := store.GetUserChannelApprovals(ctx, d.Pool, userID)
		if err != nil {
			FragmentError(w, r, "failed to load approvals")
			return
		}
		assigned := make(map[string]bool, len(assignedIDs))
		for _, id := range assignedIDs {
			assigned[id] = true
		}
		refs := make([]admintempl.ChannelRef, len(channels))
		for i, ch := range channels {
			refs[i] = admintempl.ChannelRef{SlackChannelID: ch.SlackChannelID, Name: ch.Name}
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.ChannelApproversModal(userID, user.DisplayName, refs, assigned).Render(ctx, w)
	}
}

func (d Deps) ChannelApproversSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := r.PathValue("userId")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		submitted := r.Form["channel_ids"]
		if len(submitted) > 0 {
			channels, err := store.ListSlackChannels(ctx, d.Pool)
			if err != nil {
				FragmentError(w, r, "failed to validate channels")
				return
			}
			valid := make(map[string]bool, len(channels))
			for _, ch := range channels {
				valid[ch.SlackChannelID] = true
			}
			filtered := submitted[:0]
			for _, id := range submitted {
				if valid[id] {
					filtered = append(filtered, id)
				}
			}
			submitted = filtered
		}
		if err := store.SetUserChannelApprovals(ctx, d.Pool, userID, submitted); err != nil {
			FragmentError(w, r, "failed to save approvals")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.ChannelAssignmentsSaved().Render(ctx, w)
	}
}
