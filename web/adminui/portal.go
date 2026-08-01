package adminui

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"commons/permissions"
	"commons/store"
	"commons/web"
	admintempl "commons/web/templ"
)

func (d Deps) NotificationsPanel() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		offset := 0
		if v := r.URL.Query().Get("offset"); v != "" {
			fmt.Sscanf(v, "%d", &offset)
		}
		limit := 20
		notifs, _ := store.ListNotifications(ctx, d.Pool, userID, false, limit+1, offset)
		hasMore := len(notifs) > limit
		if hasMore {
			notifs = notifs[:limit]
		}
		admintempl.NotificationsPanel(notifs, hasMore).Render(ctx, w)
	}
}

func (d Deps) NotificationsRead() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		store.MarkRead(ctx, d.Pool, id, userID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) MemberResourcesPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		perms, _ := store.GetUserPermissions(ctx, d.Pool, userID)
		hasResourcesView := false
		for _, p := range perms {
			if p == permissions.ResourcesView {
				hasResourcesView = true
				break
			}
		}
		if !hasResourcesView {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		displayName := ""
		if u, err := store.GetUserByID(ctx, d.Pool, userID); err == nil {
			displayName = u.DisplayName
		}
		unread, _ := store.CountUnread(ctx, d.Pool, userID)
		filter := r.URL.Query().Get("category")
		categories, _ := store.ListResourceCategories(ctx, d.Pool)
		var resources []store.Resource
		if filter != "" {
			resources, _ = store.ListResourcesByCategory(ctx, d.Pool, filter)
		} else {
			resources, _ = store.ListResources(ctx, d.Pool, 1, 10000)
		}
		admintempl.MemberResourcesPage(admintempl.MemberResourcesPageData{
			Categories:  categories,
			Resources:   resources,
			Filter:      filter,
			Permissions: perms,
			DisplayName: displayName,
			UnreadCount: unread,
		}).Render(ctx, w)
	}
}

func (d Deps) MemberLegislationPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		perms, _ := store.GetUserPermissions(ctx, d.Pool, userID)
		hasLegislationView := false
		for _, p := range perms {
			if p == permissions.LegislationView {
				hasLegislationView = true
				break
			}
		}
		if !hasLegislationView {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		displayName := ""
		if u, err := store.GetUserByID(ctx, d.Pool, userID); err == nil {
			displayName = u.DisplayName
		}
		unread, _ := store.CountUnread(ctx, d.Pool, userID)
		bodyFilter := r.URL.Query().Get("body")
		search := r.URL.Query().Get("search")

		bodies, _ := store.ListLegislativeBodies(ctx, d.Pool)
		billsByBody := make(map[string][]store.Bill)
		for _, body := range bodies {
			if bodyFilter != "" && body.ID != bodyFilter {
				continue
			}
			bills, _ := store.ListBillsByBody(ctx, d.Pool, body.ID)
			if search != "" {
				var filtered []store.Bill
				for _, b := range bills {
					if strings.Contains(strings.ToLower(b.Title), strings.ToLower(search)) ||
						strings.Contains(strings.ToLower(b.Identifier), strings.ToLower(search)) {
						filtered = append(filtered, b)
					}
				}
				bills = filtered
			}
			if len(bills) > 0 {
				billsByBody[body.ID] = bills
			}
		}

		subs, _ := store.GetUserSubscriptions(ctx, d.Pool, userID)
		subMap := make(map[string]bool, len(subs))
		for _, b := range subs {
			subMap[b.ID] = true
		}

		admintempl.MemberLegislationPage(admintempl.MemberLegislationPageData{
			Bodies:        bodies,
			BillsByBody:   billsByBody,
			Subscriptions: subMap,
			Filter:        bodyFilter,
			Search:        search,
			Permissions:   perms,
			DisplayName:   displayName,
			UnreadCount:   unread,
			UserID:        userID,
		}).Render(ctx, w)
	}
}

func (d Deps) MemberBillSubscribe() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		billID := r.URL.Query().Get("bill_id")
		if billID == "" {
			http.Error(w, "missing bill_id", http.StatusBadRequest)
			return
		}
		subscribed, _ := store.IsUserSubscribed(ctx, d.Pool, userID, billID)
		if subscribed {
			store.UnsubscribeUserFromBill(ctx, d.Pool, userID, billID)
			subscribed = false
		} else {
			store.SubscribeUserToBill(ctx, d.Pool, userID, billID)
			subscribed = true
		}
		admintempl.MemberSubscribeButton(billID, subscribed).Render(ctx, w)
	}
}

func (d Deps) ReportIssueForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admintempl.ReportIssueForm(false).Render(r.Context(), w)
	}
}

func (d Deps) ReportIssueSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		title := r.FormValue("title")
		itemType := r.FormValue("type")
		desc := r.FormValue("description")
		var descPtr *string
		if desc != "" {
			descPtr = &desc
		}
		if title != "" {
			store.CreateWorkItem(ctx, d.Pool, userID, itemType, title, descPtr)
		}
		admintempl.ReportIssueForm(true).Render(ctx, w)
	}
}

func (d Deps) PortalHome() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)
		displayName := ""
		if u, err := store.GetUserByID(ctx, d.Pool, userID); err == nil {
			displayName = u.DisplayName
		}
		links, _ := store.ListActiveQuickLinks(ctx, d.Pool)
		events, _ := store.ListUpcomingCalendarEvents(ctx, d.Pool, 3)
		perms, _ := store.GetUserPermissions(ctx, d.Pool, userID)
		unread, _ := store.CountUnread(ctx, d.Pool, userID)
		announcement, _ := store.GetServiceConfig(ctx, d.Pool, "bot", "announcement", d.EncKey)
		admintempl.HomePage(admintempl.HomePageData{
			DisplayName:  displayName,
			QuickLinks:   links,
			Events:       events,
			Announcement: announcement,
			Permissions:  perms,
			UnreadCount:  unread,
		}).Render(ctx, w)
	}
}

func (d Deps) RootPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		portalEnabled, _ := store.GetServiceConfig(ctx, d.Pool, "portal", "enabled", d.EncKey)
		if portalEnabled != "true" {
			http.Redirect(w, r, "/admin/docs", http.StatusSeeOther)
			return
		}
		userID := web.UserIDFromContext(ctx)
		displayName := ""
		if u, err := store.GetUserByID(ctx, d.Pool, userID); err == nil {
			displayName = u.DisplayName
		}
		links, _ := store.ListActiveQuickLinks(ctx, d.Pool)
		events, _ := store.ListUpcomingCalendarEvents(ctx, d.Pool, 3)
		perms, _ := store.GetUserPermissions(ctx, d.Pool, userID)
		unread, _ := store.CountUnread(ctx, d.Pool, userID)
		announcement, _ := store.GetServiceConfig(ctx, d.Pool, "bot", "announcement", d.EncKey)
		admintempl.HomePage(admintempl.HomePageData{
			DisplayName:  displayName,
			QuickLinks:   links,
			Events:       events,
			Announcement: announcement,
			Permissions:  perms,
			UnreadCount:  unread,
		}).Render(ctx, w)
	}
}

func (d Deps) CalendarEventsFragment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		all, _ := store.ListUpcomingCalendarEvents(ctx, d.Pool, 0)
		cutoff := time.Now().AddDate(0, 0, 14)
		var events []store.CalendarEvent
		for _, e := range all {
			if e.StartTime.Before(cutoff) {
				events = append(events, e)
			}
		}
		admintempl.CalendarEventsFragment(events).Render(ctx, w)
	}
}
