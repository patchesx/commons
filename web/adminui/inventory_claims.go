package adminui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"commons/store"
	admintempl "commons/web/templ"
)

// InventoryClaimsPage lists web-form claims waiting for an admin to link them to a member. inventory.admin only.
func (d Deps) InventoryClaimsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).Admin {
			forbidden(w)
			return
		}
		claims, err := store.ListPendingUnmatched(ctx, d.Pool)
		if err != nil {
			http.Error(w, "failed to load claims", http.StatusInternalServerError)
			return
		}
		users, err := store.ListUsers(ctx, d.Pool)
		if err != nil {
			http.Error(w, "failed to load members", http.StatusInternalServerError)
			return
		}
		members := make([]admintempl.InventoryMemberOption, 0, len(users))
		for _, u := range users {
			if u.Bot {
				continue
			}
			label := u.DisplayName
			if u.Email != nil && *u.Email != "" {
				label += " (" + *u.Email + ")"
			}
			members = append(members, admintempl.InventoryMemberOption{ID: u.ID, Label: label})
		}
		sort.Slice(members, func(i, j int) bool {
			return strings.ToLower(members[i].Label) < strings.ToLower(members[j].Label)
		})
		admintempl.InventoryClaimsPage(claims, members).Render(ctx, w)
	}
}

// InventoryClaimLink links a pending claim to the chosen member, who then takes possession.
// Success is a 204; anything else is a one-line message shown next to the form.
func (d Deps) InventoryClaimLink() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).Admin {
			forbidden(w)
			return
		}
		userID := strings.TrimSpace(r.FormValue("user_id"))
		if userID == "" {
			inlineHelp(w, r, "danger", "Choose a member.")
			return
		}
		if _, err := store.GetUserByID(ctx, d.Pool, userID); err != nil {
			inlineHelp(w, r, "danger", "That member could not be found.")
			return
		}
		linked, err := store.LinkClaimToUser(ctx, d.Pool, r.PathValue("id"), userID)
		switch {
		case errors.Is(err, store.ErrInventoryClaimNotPending):
			inlineHelp(w, r, "warning", "That claim was already resolved. Reload the page.")
			return
		case errors.Is(err, store.ErrInventoryItemArchived):
			inlineHelp(w, r, "danger", "That item is archived. Restore it first.")
			return
		case errors.Is(err, store.ErrNotFound):
			inlineHelp(w, r, "danger", "Claim not found.")
			return
		case err != nil:
			log.Printf("inventory: link claim %s: %v", r.PathValue("id"), err)
			inlineHelp(w, r, "danger", "Failed to link the claim.")
			return
		}
		if d.NotifyClaimLinked != nil {
			go d.NotifyClaimLinked(context.Background(), linked.ItemID, userID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// InventoryClaimsCount renders the sidebar badge: the number of pending claims, shown only to inventory admins.
func (d Deps) InventoryClaimsCount() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if !d.inventoryAccess(r).Admin {
			return
		}
		n, err := store.CountPendingUnmatched(r.Context(), d.Pool)
		if err != nil || n == 0 {
			return
		}
		fmt.Fprintf(w, `<span class="tag is-primary is-rounded ml-1">%d</span>`, n)
	}
}
