package adminui

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
	admintempl "commons/web/templ"
	"commons/web"
)

const resourcesPerPage = 20

func (d Deps) ResourcesPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		resources, total, totalPages, cats, perms, err := loadResourcesPage(ctx, d.Pool, page)
		if err != nil {
			FragmentError(w, r, "failed to load resources")
			return
		}
		admintempl.ResourcesPage(resources, total, page, totalPages, cats, perms).Render(ctx, w)
	}
}

func (d Deps) ResourcesTable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		resources, total, totalPages, cats, _, err := loadResourcesPage(ctx, d.Pool, page)
		if err != nil {
			FragmentError(w, r, "failed to load resources")
			return
		}
		admintempl.ResourcesTable(resources, total, page, totalPages, cats).Render(ctx, w)
	}
}

func (d Deps) ResourceEditModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		res, err := store.GetResourceByID(ctx, d.Pool, r.PathValue("id"))
		if err != nil || res == nil {
			FragmentError(w, r, "not found")
			return
		}
		cats, _ := store.ListResourceCategories(ctx, d.Pool)
		admintempl.EditResourceModal(*res, cats, "").Render(ctx, w)
	}
}

func (d Deps) ResourcesCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		title := r.FormValue("title")
		url := r.FormValue("url")
		category := r.FormValue("category")
		description := r.FormValue("description")
		if title == "" || url == "" || category == "" {
			FragmentError(w, r, "title, url, and category are required")
			return
		}
		var desc *string
		if description != "" {
			desc = &description
		}
		res := &store.Resource{Title: title, URL: url, Category: category, Description: desc}
		if err := store.CreateResource(ctx, d.Pool, res); err != nil {
			FragmentError(w, r, "failed to create resource")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) ResourcesUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		title := r.FormValue("title")
		url := r.FormValue("url")
		category := r.FormValue("category")
		description := r.FormValue("description")
		var desc *string
		if description != "" {
			desc = &description
		}
		res := &store.Resource{ID: id, Title: title, URL: url, Category: category, Description: desc}
		if err := store.UpdateResource(ctx, d.Pool, res); err != nil {
			cats, _ := store.ListResourceCategories(ctx, d.Pool)
			admintempl.EditResourceModal(*res, cats, "Failed to update resource.").Render(ctx, w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/resources")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) ResourcesDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteResource(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) ResourceCategoriesCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.FormValue("name")
		if name == "" {
			FragmentError(w, r, "name required")
			return
		}
		if _, err := store.CreateResourceCategory(r.Context(), d.Pool, name); err != nil {
			FragmentError(w, r, "failed to create category")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) ResourceCategoriesUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		name := r.FormValue("name")
		perm := r.FormValue("permission")
		var permPtr *string
		if perm != "" {
			permPtr = &perm
		}
		if err := store.UpdateResourceCategory(ctx, d.Pool, id, name, permPtr); err != nil {
			FragmentError(w, r, "failed to update category")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) ResourceCategoriesDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteResourceCategory(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete category")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) WorkItemsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statusFilter := r.URL.Query().Get("status")
		typeFilter := r.URL.Query().Get("type")
		items, err := store.ListWorkItems(r.Context(), d.Pool, statusFilter, typeFilter)
		if err != nil {
			http.Error(w, "failed to load work items", http.StatusInternalServerError)
			return
		}
		admintempl.WorkItemsPage(items, statusFilter, typeFilter).Render(r.Context(), w)
	}
}

func (d Deps) WorkItemsTable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statusFilter := r.URL.Query().Get("status")
		typeFilter := r.URL.Query().Get("type")
		items, err := store.ListWorkItems(r.Context(), d.Pool, statusFilter, typeFilter)
		if err != nil {
			FragmentError(w, r, "failed to load work items")
			return
		}
		admintempl.WorkItemsTable(items).Render(r.Context(), w)
	}
}

func (d Deps) WorkItemDetailModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		item, err := store.GetWorkItem(r.Context(), d.Pool, r.PathValue("id"))
		if err != nil || item == nil {
			FragmentError(w, r, "work item not found")
			return
		}
		admintempl.WorkItemDetailModal(*item, "").Render(r.Context(), w)
	}
}

func (d Deps) WorkItemUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		status := r.FormValue("status")
		adminNotes := r.FormValue("admin_notes")
		validStatuses := map[string]bool{
			"open": true, "acknowledged": true, "in_progress": true,
			"resolved": true, "closed": true,
		}
		existing, err := store.GetWorkItem(ctx, d.Pool, id)
		if err != nil || existing == nil {
			FragmentError(w, r, "work item not found")
			return
		}
		if !validStatuses[status] {
			admintempl.WorkItemDetailModal(*existing, "Invalid status.").Render(ctx, w)
			return
		}
		oldStatus := existing.Status
		adminID := web.UserIDFromContext(ctx)
		var resolvedBy *string
		if (status == "resolved" || status == "closed") && adminID != "" {
			resolvedBy = &adminID
		}
		var notesPtr *string
		if adminNotes != "" {
			notesPtr = &adminNotes
		}
		if err := store.UpdateWorkItemStatus(ctx, d.Pool, id, status, notesPtr, resolvedBy); err != nil {
			admintempl.WorkItemDetailModal(*existing, "Failed to update work item.").Render(ctx, w)
			return
		}
		store.LogAction(ctx, d.Pool, &adminID, "work_item.updated", id, map[string]string{"status": status})
		updated := *existing
		updated.Status = status
		updated.AdminNotes = notesPtr
		if status != oldStatus {
			go func(item store.WorkItem) {
				typeLabel := map[string]string{"issue": "Bug Report", "feature_request": "Feature Request"}[item.Type]
				if typeLabel == "" {
					typeLabel = item.Type
				}
				statusLabels := map[string]string{
					"open": "Open", "acknowledged": "Acknowledged", "in_progress": "In Progress",
					"resolved": "Resolved", "closed": "Closed",
				}
				oldLabel, newLabel := statusLabels[oldStatus], statusLabels[status]
				title := fmt.Sprintf("%s status updated: %s -> %s", typeLabel, oldLabel, newLabel)
				body := fmt.Sprintf("Your %s has been updated.\n*%s*\nStatus: %s -> %s", typeLabel, item.Title, oldLabel, newLabel)
				if item.AdminNotes != nil && *item.AdminNotes != "" {
					body += "\n\n" + *item.AdminNotes
				}
				if err := store.NotifyUser(context.Background(), d.Pool, d.EncKey, item.RequesterID, "work_item_updated", title, body, "", d.Notifier); err != nil {
					log.Printf("adminui: notify work item status change for item %s: %v", item.ID, err)
				}
			}(updated)
		}
		admintempl.WorkItemDetailModal(updated, "").Render(ctx, w)
	}
}

func (d Deps) OpenWorkItemsCount() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := store.ListWorkItems(r.Context(), d.Pool, "open", "")
		w.Header().Set("Content-Type", "text/html")
		if err != nil || len(items) == 0 {
			w.Write([]byte(``))
			return
		}
		fmt.Fprintf(w, `<span class="tag is-primary is-rounded ml-1">%d</span>`, len(items))
	}
}

func loadResourcesPage(ctx context.Context, pool *pgxpool.Pool, page int) ([]store.Resource, int, int, []store.ResourceCategory, []store.Permission, error) {
	resources, err := store.ListResources(ctx, pool, page, resourcesPerPage)
	if err != nil {
		return nil, 0, 0, nil, nil, err
	}
	total, err := store.CountResources(ctx, pool)
	if err != nil {
		return nil, 0, 0, nil, nil, err
	}
	cats, err := store.ListResourceCategories(ctx, pool)
	if err != nil {
		return nil, 0, 0, nil, nil, err
	}
	perms, err := store.ListAllPermissions(ctx, pool)
	if err != nil {
		return nil, 0, 0, nil, nil, err
	}
	totalPages := (total + resourcesPerPage - 1) / resourcesPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	return resources, total, totalPages, cats, perms, nil
}
