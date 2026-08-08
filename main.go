package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed IANA timezone database so time.LoadLocation works without system tzdata

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	_ "commons/integrations/discord"
	_ "commons/integrations/gdrive"
	_ "commons/integrations/google"
	_ "commons/integrations/matrix"
	_ "commons/integrations/nextcloud"
	_ "commons/integrations/s3storage"
	_ "commons/integrations/solidaritytech"
	_ "commons/integrations/vimeo"
	ytpkg "commons/integrations/youtube"
	_ "commons/integrations/zoom"

	"commons/api"
	"commons/config"
	"commons/db"
	"commons/events"
	"commons/install"
	"commons/jobs"
	"commons/legislation"
	"commons/plugin"
	"commons/store"
	"commons/web"
	"commons/web/adminui"
	htmxpkg "commons/web/htmx"
	admintempl "commons/web/templ"
	"commons/webhooks"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hashpw" {
		fmt.Print("Password: ")
		var pw string
		fmt.Scanln(&pw)
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
		if err != nil {
			log.Fatalf("main: hashpw: %v", err)
		}
		fmt.Println(string(hash))
		os.Exit(0)
	}

	loadDotEnv(".env")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("main: config: %v", err)
	}

	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("main: database: %v", err)
	}
	defer pool.Close()

	var pluginMigrations []db.PluginMigration
	for _, p := range plugin.All() {
		for _, m := range p.Migrations() {
			pluginMigrations = append(pluginMigrations, db.PluginMigration{
				Plugin:  p.Name(),
				Version: m.Version,
				SQL:     m.SQL,
			})
		}
	}
	if err := db.RunMigrations(ctx, pool, pluginMigrations); err != nil {
		log.Fatalf("main: migrations: %v", err)
	}

	// Check if setup wizard is needed.
	hasAdmin, err := store.HasAnyAdminCredentials(ctx, pool)
	if err != nil {
		log.Fatalf("main: install check: %v", err)
	}
	installMode := !hasAdmin || os.Getenv("INSTALL_MODE") == "true"
	if installMode {
		log.Println("main: install: no admin found, setup wizard will be served at /install")
	}

	if !installMode {
		if err := maybeBootstrapAdmin(ctx, pool, cfg); err != nil {
			log.Fatalf("main: bootstrap admin: %v", err)
		}

		if err := reencryptSensitiveConfigs(ctx, pool, cfg.EncryptionKey); err != nil {
			log.Fatalf("main: re-encrypt config: %v", err)
		}
	}

	web.SetSecureCookies(cfg.SecureCookies)

	webAuth := func(h http.Handler) http.Handler {
		return web.RequireWebAuth(cfg.SessionSecret, web.AppearanceMiddleware(pool, cfg.EncryptionKey, h))
	}
	apiAuth := func(h http.Handler) http.Handler { return web.RequireAPIAuth(cfg.SessionSecret, h) }

	mux := http.NewServeMux()

	// Mount embedded static assets for HTMX and the templ admin UI.
	htmxpkg.Mount(mux)
	admintempl.Mount(mux)

	// Initialize all registered plugins. Plugins register their own routes and
	// scheduled jobs during Init using the provided PluginContext.
	pctx := plugin.NewContext(pool, cfg.EncryptionKey, mux, apiAuth)
	if err := plugin.InitAll(pctx); err != nil {
		log.Fatalf("main: plugin init: %v", err)
	}
	plugin.RegisterActionType(webhooks.NewCreateResourceAction(pool))
	plugin.FinalizeActionTypes()
	plugin.SetDispatcher(events.NewRunner(pool, cfg.EncryptionKey))

	// Resume upload jobs left in "youtube_processing" state.
	go ytpkg.ResumeProcessingJobs(context.Background(), pool, cfg.EncryptionKey)

	// Register platform-agnostic scheduled jobs that fan out through all active notifiers.
	// These run after InitAll so the composite notifier is fully populated.
	pctx.RegisterScheduledJob(
		"meeting_reminders_enabled", "meeting_reminders_interval_minutes",
		30*time.Second, func() {
			jobs.SendMeetingReminders(context.Background(), pool, cfg.EncryptionKey)
		},
	)
	pctx.RegisterScheduledJob(
		"library_overdue_enabled", "library_overdue_interval_minutes",
		1*time.Minute, func() {
			if err := jobs.SendOverdueReminders(context.Background(), pool, pctx.Notifier()); err != nil {
				log.Printf("main: library overdue reminders: %v", err)
			}
		},
	)

	// Wire plugin-registered nav items into the templ sidebar.
	navRegs := pctx.ExtraNavItems()
	extraNav := make([]admintempl.SidebarItem, len(navRegs))
	for i, reg := range navRegs {
		extraNav[i] = admintempl.SidebarItem{Label: reg.Label, Path: reg.Path}
	}
	admintempl.SetExtraNavItems(extraNav)

	admin := adminui.Deps{
		Pool:          pool,
		EncKey:        cfg.EncryptionKey,
		SessionSecret: cfg.SessionSecret,
		Notifier:      pctx.Notifier(),
		Pctx:          pctx,
	}

	loginLimiter := web.NewLoginLimiter()

	// Generic configurable webhook handler — all webhooks are POST only.
	mux.HandleFunc("POST /webhook/{slug...}", webhooks.Handler(pool, cfg.EncryptionKey, pctx))

	// Web UI auth — username/password.
	mux.Handle("GET /admin", adminui.AdminRedirect())
	mux.Handle("GET /login", admin.LoginPage())
	mux.Handle("POST /login", loginLimiter.LimitLogin(web.LoginSubmit(cfg, pool)))
	mux.HandleFunc("POST /logout", web.Logout())
	mux.Handle("GET /reset-password", admin.ResetPasswordPage())
	mux.Handle("POST /reset-password", loginLimiter.LimitLogin(admin.ResetPasswordSubmit()))

	// Google OAuth flow (session-protected — admin must be logged in).
	mux.Handle("GET /auth/google/connect", webAuth(web.GoogleConnect(pool, cfg.EncryptionKey)))
	mux.Handle("GET /auth/google/callback", webAuth(web.GoogleCallback(pool, cfg.EncryptionKey)))

	// Templ fragments — small HTML snippets fetched by HTMX.
	mux.Handle("GET /admin/fragments/open-work-items-count", webAuth(admin.OpenWorkItemsCount()))

	// DocsPage — static in-app documentation.
	mux.Handle("GET /admin/docs", webAuth(admin.DocsPage()))

	// AuditPage — full page and HTMX rows fragment.
	mux.Handle("GET /admin/audit", webAuth(admin.AuditPage()))
	mux.Handle("GET /admin/fragments/audit-rows", webAuth(admin.AuditRows()))

	// SchedulerPage — full page, toggle fragment, interval fragment.
	mux.Handle("GET /admin/scheduler", webAuth(admin.SchedulerPage()))
	mux.Handle("PATCH /admin/fragments/scheduler/{id}/toggle", webAuth(admin.SchedulerToggle()))
	mux.Handle("PATCH /admin/fragments/scheduler/{id}/interval", webAuth(admin.SchedulerInterval()))

	// UsersPage — full page, HTMX table fragment, member CRUD, roles, promote, channel approvers.
	mux.Handle("GET /admin/users", webAuth(admin.UsersPage()))
	mux.Handle("GET /admin/fragments/users-table", webAuth(admin.UsersTable()))
	mux.Handle("POST /admin/members/create", webAuth(admin.CreateMember()))
	mux.Handle("POST /admin/members/import", webAuth(admin.ImportMembers()))
	mux.Handle("POST /admin/fragments/users/{id}/reset-link", webAuth(admin.ResetLink()))
	mux.Handle("POST /admin/fragments/users/{userId}/group", webAuth(admin.AssignGroup()))
	mux.Handle("DELETE /admin/fragments/users/{userId}/group", webAuth(admin.RemoveGroup()))
	mux.Handle("GET /admin/fragments/users/{userId}/promote-modal", webAuth(admin.PromoteModal()))
	mux.Handle("POST /admin/fragments/users/{userId}/promote", webAuth(admin.PromoteSubmit()))
	mux.Handle("GET /admin/fragments/users/{userId}/channel-approvers-modal", webAuth(admin.ChannelApproversModal()))
	mux.Handle("POST /admin/fragments/users/{userId}/channel-approvers", webAuth(admin.ChannelApproversSave()))

	// RolesPage — full page + HTMX fragments for create/update/delete.
	mux.Handle("GET /admin/roles", webAuth(admin.RolesPage()))
	mux.Handle("POST /admin/fragments/roles", webAuth(admin.RolesCreate()))
	mux.Handle("PATCH /admin/fragments/roles/{id}", webAuth(admin.RolesUpdate()))
	mux.Handle("DELETE /admin/fragments/roles/{id}", webAuth(admin.RolesDelete()))
	mux.Handle("POST /admin/fragments/roles/{id}/duplicate", webAuth(admin.RolesDuplicate()))

	// Role groups — HTMX fragments for create/update/delete.
	mux.Handle("POST /admin/fragments/role-groups", webAuth(admin.RoleGroupsCreate()))
	mux.Handle("PATCH /admin/fragments/role-groups/{id}", webAuth(admin.RoleGroupsUpdate()))
	mux.Handle("DELETE /admin/fragments/role-groups/{id}", webAuth(admin.RoleGroupsDelete()))

	// CalendarsPage — full page + HTMX fragments for CRUD and grid navigation.
	mux.Handle("GET /admin/calendars", webAuth(admin.CalendarsPage()))
	mux.Handle("POST /admin/fragments/calendars/create", webAuth(admin.CalendarsCreate()))
	mux.Handle("PATCH /admin/fragments/calendars/{id}", webAuth(admin.CalendarsUpdate()))
	mux.Handle("DELETE /admin/fragments/calendars/{id}", webAuth(admin.CalendarsDelete()))
	mux.Handle("POST /admin/fragments/calendars/{id}/convert-to-manual", webAuth(admin.CalendarsConvertToManual()))
	mux.Handle("GET /admin/fragments/calendars/{calId}/grid", webAuth(admin.CalendarsGrid()))
	mux.Handle("GET /admin/fragments/calendars/{calId}/event-form", webAuth(admin.CalendarsEventForm()))
	mux.Handle("GET /admin/fragments/calendars/{calId}/events/{eventId}/form", webAuth(admin.CalendarsEventEditForm()))
	mux.Handle("POST /admin/fragments/calendars/{calId}/events", webAuth(admin.CalendarsEventCreate()))
	mux.Handle("PATCH /admin/fragments/calendars/{calId}/events/{eventId}", webAuth(admin.CalendarsEventUpdate()))
	mux.Handle("DELETE /admin/fragments/calendars/{calId}/events/{eventId}", webAuth(admin.CalendarsEventDelete()))

	// Integrations — list, detail, add modal/config fields, save.
	mux.Handle("GET /admin/integrations", webAuth(admin.IntegrationsPage()))
	mux.Handle("GET /admin/integrations/{slug}", webAuth(admin.IntegrationDetail()))
	mux.Handle("GET /admin/fragments/integrations/add-modal", webAuth(admin.IntegrationsAddModal()))
	mux.Handle("GET /admin/fragments/config/{service}", webAuth(admin.ConfigFragment()))

	// Settings.
	mux.Handle("GET /admin/settings", webAuth(admin.SettingsPage()))
	mux.Handle("PATCH /admin/fragments/settings/default-calendar", webAuth(admin.SettingsDefaultCalendar()))

	// Triggers — unified page for webhooks + event trigger pipelines.
	mux.Handle("GET /admin/triggers", webAuth(admin.TriggersPage()))
	mux.Handle("GET /admin/triggers/{id}", webAuth(admin.TriggerDetailPage()))
	mux.Handle("GET /admin/fragments/triggers/webhook-form", webAuth(admin.TriggerWebhookForm()))
	mux.Handle("POST /admin/fragments/triggers/webhook", webAuth(admin.TriggerWebhookCreate()))
	mux.Handle("GET /admin/fragments/triggers/{id}/webhook-form", webAuth(admin.TriggerWebhookEditForm()))
	mux.Handle("PATCH /admin/fragments/triggers/{id}", webAuth(admin.TriggerWebhookUpdate()))
	mux.Handle("POST /admin/fragments/triggers/event-pipeline", webAuth(admin.TriggerCreateEventPipeline()))
	mux.Handle("DELETE /admin/fragments/triggers/{id}", webAuth(admin.TriggerDelete()))
	mux.Handle("GET /admin/fragments/triggers/{id}/action-add-form", webAuth(admin.TriggerActionAddForm()))
	mux.Handle("GET /admin/fragments/triggers/{id}/actions/{aid}/edit-form", webAuth(admin.TriggerActionEditForm()))
	mux.Handle("GET /admin/fragments/triggers/{id}/actions/{aid}/row", webAuth(admin.TriggerActionRow()))
	mux.Handle("GET /admin/fragments/triggers/{id}/action-params", webAuth(admin.TriggerActionParams()))
	mux.Handle("POST /admin/fragments/triggers/{id}/actions", webAuth(admin.TriggerActionCreate()))
	mux.Handle("PATCH /admin/fragments/triggers/{id}/actions/{aid}", webAuth(admin.TriggerActionUpdate()))
	mux.Handle("DELETE /admin/fragments/triggers/{id}/actions/{aid}", webAuth(admin.TriggerActionDelete()))
	mux.Handle("GET /admin/fragments/triggers/{id}/filter-form", webAuth(admin.TriggerFilterForm()))
	mux.Handle("POST /admin/fragments/triggers/{id}/filters", webAuth(admin.TriggerFilterCreate()))
	mux.Handle("GET /admin/fragments/triggers/{id}/filters/{fid}/row", webAuth(admin.TriggerFilterRow()))
	mux.Handle("PATCH /admin/fragments/triggers/{id}/filters/{fid}", webAuth(admin.TriggerFilterUpdate()))
	mux.Handle("DELETE /admin/fragments/triggers/{id}/filters/{fid}", webAuth(admin.TriggerFilterDelete()))
	mux.Handle("GET /admin/fragments/message-variant", webAuth(admin.TriggerMessageVariant()))
	mux.Handle("PATCH /api/triggers/{id}/enabled", apiAuth(admin.TriggerToggleEnabled()))

	// Meetings page
	mux.Handle("GET /admin/meetings", webAuth(admin.MeetingsPage()))
	mux.Handle("GET /admin/fragments/meetings/{id}/start-url", webAuth(admin.MeetingStartURL()))

	// Jobs page
	mux.Handle("GET /admin/jobs", webAuth(admin.JobsPage()))
	mux.Handle("GET /admin/fragments/jobs-table", webAuth(admin.JobsTable()))
	mux.Handle("GET /admin/fragments/jobs/{id}/detail", webAuth(admin.JobDetail()))
	mux.Handle("POST /admin/fragments/jobs/{id}/cancel", webAuth(admin.JobCancel()))

	// Resources page
	mux.Handle("GET /admin/resources", webAuth(admin.ResourcesPage()))
	mux.Handle("GET /admin/fragments/resources-table", webAuth(admin.ResourcesTable()))
	mux.Handle("GET /admin/fragments/resources/{id}/edit-modal", webAuth(admin.ResourceEditModal()))
	mux.Handle("POST /admin/fragments/resources", webAuth(admin.ResourcesCreate()))
	mux.Handle("PUT /admin/fragments/resources/{id}", webAuth(admin.ResourcesUpdate()))
	mux.Handle("DELETE /admin/fragments/resources/{id}", webAuth(admin.ResourcesDelete()))
	mux.Handle("POST /admin/fragments/resource-categories", webAuth(admin.ResourceCategoriesCreate()))
	mux.Handle("PATCH /admin/fragments/resource-categories/{id}", webAuth(admin.ResourceCategoriesUpdate()))
	mux.Handle("DELETE /admin/fragments/resource-categories/{id}", webAuth(admin.ResourceCategoriesDelete()))

	// WorkItemsPage — full page, table fragment, detail modal, and PATCH.
	mux.Handle("GET /admin/work-items", webAuth(admin.WorkItemsPage()))
	mux.Handle("GET /admin/fragments/work-items-table", webAuth(admin.WorkItemsTable()))
	mux.Handle("GET /admin/fragments/work-items/{id}/detail-modal", webAuth(admin.WorkItemDetailModal()))
	mux.Handle("PATCH /admin/fragments/work-items/{id}", webAuth(admin.WorkItemUpdate()))

	// LegislationPage — full page, bills table fragment, bill/body form modals, filters, tags, sync.
	mux.Handle("GET /admin/legislation", webAuth(admin.LegislationPage()))
	mux.Handle("GET /admin/legislation/browse/{id}", webAuth(admin.BrowseBillsPage()))
	mux.Handle("GET /admin/fragments/legislation-bills", webAuth(admin.LegislationBillsTable()))
	mux.Handle("GET /admin/fragments/legislation/add-bill-form", webAuth(admin.AddBillForm()))
	mux.Handle("POST /admin/fragments/legislation/bills", webAuth(admin.BillsCreate()))
	mux.Handle("GET /admin/fragments/legislation/bill-form", webAuth(admin.BillForm()))
	mux.Handle("PUT /admin/fragments/legislation/bills/{id}", webAuth(admin.BillsUpdate()))
	mux.Handle("DELETE /admin/fragments/legislation/bills/{id}", webAuth(admin.BillsDelete()))
	mux.Handle("GET /admin/fragments/legislation/body-form", webAuth(admin.BodyForm()))
	mux.Handle("POST /admin/fragments/legislation/bodies", webAuth(admin.BodiesCreate()))
	mux.Handle("PUT /admin/fragments/legislation/bodies/{id}", webAuth(admin.BodiesUpdate()))
	mux.Handle("POST /admin/fragments/legislation/bodies/{id}/filters", webAuth(admin.FiltersCreate()))
	mux.Handle("DELETE /admin/fragments/legislation/filters/{id}", webAuth(admin.FiltersDelete()))
	mux.Handle("GET /admin/fragments/legislation/bodies/{id}/matter-types", webAuth(admin.MatterTypes()))
	mux.Handle("POST /admin/fragments/legislation/bodies/{id}/subjects/refresh", webAuth(admin.RefreshBodySubjects()))
	mux.Handle("GET /admin/fragments/legislation/bodies/{id}/browse", webAuth(admin.BrowseBills()))
	mux.Handle("POST /admin/fragments/legislation/bodies/{id}/track", webAuth(admin.TrackBillFromBrowse()))
	mux.Handle("POST /admin/fragments/legislation/sync", webAuth(admin.LegislationSync()))
	mux.Handle("POST /admin/fragments/legislation/tags", webAuth(admin.TagsCreate()))
	mux.Handle("DELETE /admin/fragments/legislation/tags/{id}", webAuth(admin.TagsDelete()))

	// AppHomePage — bot display config, quick links, contacts.
	mux.Handle("GET /admin/app-home", webAuth(admin.AppHomePage()))
	mux.Handle("GET /admin/fragments/app-home/links-list", webAuth(admin.AppHomeLinksList()))
	mux.Handle("GET /admin/fragments/app-home/contacts-list", webAuth(admin.AppHomeContactsList()))

	// /change-password — accessible to all logged-in users (members + admins).
	mux.Handle("GET /change-password", webAuth(admin.ChangePasswordPage()))
	mux.Handle("POST /change-password", webAuth(admin.ChangePasswordSubmit()))

	// REST API (session-protected).
	mux.Handle("POST /api/auth/register", loginLimiter.LimitLogin(api.Register(pool, cfg.EncryptionKey)))
	mux.Handle("GET /api/auth/check", apiAuth(api.CheckAuth(pool)))
	mux.Handle("GET /api/jobs", apiAuth(api.ListJobs(pool)))
	mux.Handle("GET /api/jobs/{id}", apiAuth(api.GetJob(pool)))
	mux.Handle("POST /api/jobs/{id}/cancel", apiAuth(api.CancelJob(pool, pctx.CancelJob)))
	mux.Handle("GET /api/config/schema", apiAuth(api.ListConfigSchemaAll(pool)))
	mux.Handle("GET /api/config", apiAuth(api.ListConfig(pool, cfg.EncryptionKey)))
	mux.Handle("PUT /api/config/{service}/{key}", apiAuth(api.SetConfig(pool, cfg.EncryptionKey)))
	mux.Handle("GET /api/users/me", apiAuth(api.GetMe(pool)))
	mux.Handle("PATCH /api/users/me", apiAuth(api.PatchMe(pool)))
	mux.Handle("POST /api/users/me/password", apiAuth(api.ChangePasswordMember(pool)))
	mux.Handle("GET /api/users", apiAuth(api.ListUsers(pool)))
	mux.Handle("GET /api/users/search", apiAuth(api.SearchUsers(pool)))
	mux.Handle("GET /api/roles", apiAuth(api.ListRolesWithPerms(pool)))
	mux.Handle("POST /api/roles", apiAuth(api.CreateRole(pool)))
	mux.Handle("PATCH /api/roles/{id}", apiAuth(api.UpdateRole(pool)))
	mux.Handle("DELETE /api/roles/{id}", apiAuth(api.DeleteRole(pool)))
	mux.Handle("POST /api/roles/{id}/permissions", apiAuth(api.AddRolePermission(pool)))
	mux.Handle("DELETE /api/roles/{id}/permissions/{key}", apiAuth(api.RemoveRolePermission(pool)))
	mux.Handle("POST /api/role-groups/{id}/roles", apiAuth(api.AddRoleToGroup(pool)))
	mux.Handle("DELETE /api/role-groups/{id}/roles/{roleId}", apiAuth(api.RemoveRoleFromGroup(pool)))
	mux.Handle("PUT /api/users/{id}/group", apiAuth(api.AssignGroup(pool)))
	mux.Handle("DELETE /api/users/{id}/group", apiAuth(api.RemoveGroup(pool)))
	mux.Handle("POST /api/users/{id}/promote", apiAuth(api.PromoteUser(pool, cfg.EncryptionKey, pctx.Notifier())))
	mux.Handle("GET /api/resources", apiAuth(api.ListResources(pool)))
	mux.Handle("POST /api/resources", apiAuth(api.CreateResource(pool)))
	mux.Handle("PUT /api/resources/{id}", apiAuth(api.UpdateResource(pool)))
	mux.Handle("DELETE /api/resources/{id}", apiAuth(api.DeleteResource(pool)))
	mux.Handle("GET /api/permissions", apiAuth(api.ListPermissions(pool)))
	mux.Handle("GET /api/resource-categories", apiAuth(api.ListResourceCategories(pool)))
	mux.Handle("POST /api/resource-categories", apiAuth(api.CreateResourceCategory(pool)))
	mux.Handle("PUT /api/resource-categories/{id}", apiAuth(api.UpdateResourceCategory(pool)))
	mux.Handle("DELETE /api/resource-categories/{id}", apiAuth(api.DeleteResourceCategory(pool)))
	mux.Handle("GET /api/quick-links", apiAuth(api.ListQuickLinks(pool)))
	mux.Handle("POST /api/quick-links", apiAuth(api.CreateQuickLink(pool)))
	mux.Handle("PUT /api/quick-links/{id}", apiAuth(api.UpdateQuickLink(pool)))
	mux.Handle("DELETE /api/quick-links/{id}", apiAuth(api.DeleteQuickLink(pool)))
	mux.Handle("GET /api/contacts", apiAuth(api.ListContacts(pool)))
	mux.Handle("POST /api/contacts", apiAuth(api.CreateContact(pool)))
	mux.Handle("PUT /api/contacts/{id}", apiAuth(api.UpdateContact(pool)))
	mux.Handle("DELETE /api/contacts/{id}", apiAuth(api.DeleteContact(pool)))
	mux.Handle("GET /api/legislation/bodies", apiAuth(api.ListLegislativeBodies(pool)))
	mux.Handle("POST /api/legislation/bodies", apiAuth(api.CreateBody(pool)))
	mux.Handle("PUT /api/legislation/bodies/{id}", apiAuth(api.UpdateBody(pool)))
	mux.Handle("GET /api/legislation/bodies/{id}/filters", apiAuth(api.ListBodyFilters(pool)))
	mux.Handle("POST /api/legislation/bodies/{id}/filters", apiAuth(api.CreateBodyFilter(pool)))
	mux.Handle("DELETE /api/legislation/filters/{id}", apiAuth(api.DeleteBodyFilter(pool)))
	mux.Handle("GET /api/legislation/bills", apiAuth(api.ListBills(pool)))
	mux.Handle("GET /api/legislation/bills/{id}", apiAuth(api.GetBill(pool)))
	mux.Handle("POST /api/legislation/bills", apiAuth(api.CreateBill(pool)))
	mux.Handle("PUT /api/legislation/bills/{id}", apiAuth(api.UpdateBill(pool)))
	mux.Handle("DELETE /api/legislation/bills/{id}", apiAuth(api.DismissBill(pool)))
	mux.Handle("GET /api/legislation/tags", apiAuth(api.ListTags(pool)))
	mux.Handle("POST /api/legislation/tags", apiAuth(api.CreateTag(pool)))

	legislationSyncFn := func() {
		legislation.Sync(context.Background(), pool, cfg.EncryptionKey, pctx.Notifier())
	}
	mux.Handle("POST /api/legislation/sync", apiAuth(api.TriggerSync(legislationSyncFn)))
	mux.Handle("GET /api/legislation/subscriptions", apiAuth(api.ListMySubscriptions(pool)))
	mux.Handle("POST /api/legislation/bills/{id}/subscribe", apiAuth(api.SubscribeBill(pool)))
	mux.Handle("DELETE /api/legislation/bills/{id}/subscribe", apiAuth(api.UnsubscribeBill(pool)))
	mux.Handle("GET /api/notifications", apiAuth(api.ListNotifications(pool)))
	mux.Handle("GET /api/notifications/count", apiAuth(api.CountUnreadNotifications(pool)))
	mux.Handle("POST /api/notifications/read-all", apiAuth(api.MarkAllNotificationsRead(pool)))
	mux.Handle("PATCH /api/notifications/{id}/read", apiAuth(api.MarkNotificationRead(pool)))
	mux.Handle("GET /api/work-items", apiAuth(api.ListWorkItems(pool)))
	mux.Handle("GET /api/work-items/{id}", apiAuth(api.GetWorkItem(pool)))
	mux.Handle("PUT /api/work-items/{id}", apiAuth(api.UpdateWorkItem(pool, cfg.EncryptionKey, pctx.Notifier())))
	mux.Handle("GET /api/audit", apiAuth(api.ListAudit(pool)))
	mux.Handle("POST /api/admins/me/password", apiAuth(api.ChangePassword(pool)))
	mux.Handle("GET /api/calendar-events", apiAuth(api.ListUpcomingEvents(pool)))
	mux.Handle("GET /api/calendars", apiAuth(api.ListCalendars(pool)))
	mux.Handle("POST /api/calendars", apiAuth(api.CreateCalendar(pool, cfg.EncryptionKey)))
	mux.Handle("PUT /api/calendars/{id}", apiAuth(api.UpdateCalendar(pool)))
	mux.Handle("DELETE /api/calendars/{id}", apiAuth(api.DeleteCalendar(pool)))
	mux.Handle("GET /api/calendars/{id}/events", apiAuth(api.ListCalendarEvents(pool)))
	mux.Handle("POST /api/calendars/{id}/events", apiAuth(api.CreateCalendarEvent(pool)))
	mux.Handle("PUT /api/calendars/{id}/events/{eventId}", apiAuth(api.UpdateCalendarEvent(pool)))
	mux.Handle("DELETE /api/calendars/{id}/events/{eventId}", apiAuth(api.DeleteCalendarEvent(pool)))
	mux.Handle("POST /api/calendars/{id}/import", apiAuth(api.ImportCalendar(pool)))
	mux.Handle("GET /api/storage-locations", apiAuth(api.ListStorageLocations(pool)))
	mux.Handle("POST /api/storage-locations", apiAuth(api.CreateStorageLocation(pool)))
	mux.Handle("DELETE /api/storage-locations/{id}", apiAuth(api.DeleteStorageLocation(pool)))
	mux.Handle("GET /api/integrations", apiAuth(api.ListIntegrations(pool)))
	mux.Handle("GET /api/integrations/status", apiAuth(api.IntegrationStatus(pool)))
	mux.Handle("PATCH /api/integrations/{id}/enabled", apiAuth(api.SetIntegrationEnabled(pool)))
	mux.Handle("GET /api/channel-requests", apiAuth(api.ListMyChannelRequests(pool)))
	mux.Handle("POST /api/channel-requests", apiAuth(api.CreateChannelRequest(pool)))
	mux.Handle("POST /api/work-items", apiAuth(api.CreateWorkItemMember(pool)))
	mux.Handle("GET /api/plugins", apiAuth(api.ListPlugins()))
	mux.Handle("GET /api/webhook-processor-types", apiAuth(api.ListWebhookProcessorTypes()))
	mux.Handle("GET /api/webhooks/action-types", apiAuth(api.ListWebhookActionTypes()))
	mux.Handle("GET /api/webhooks", apiAuth(api.ListWebhooks(pool, cfg.EncryptionKey)))
	mux.Handle("POST /api/webhooks", apiAuth(api.CreateWebhook(pool, cfg.EncryptionKey)))
	mux.Handle("GET /api/webhooks/{id}", apiAuth(api.GetWebhook(pool, cfg.EncryptionKey)))
	mux.Handle("PUT /api/webhooks/{id}", apiAuth(api.UpdateWebhook(pool, cfg.EncryptionKey)))
	mux.Handle("DELETE /api/webhooks/{id}", apiAuth(api.DeleteWebhook(pool)))
	mux.Handle("POST /api/webhooks/{id}/actions", apiAuth(api.CreateWebhookAction(pool)))
	mux.Handle("PUT /api/webhooks/{id}/actions/{actionId}", apiAuth(api.UpdateWebhookAction(pool)))
	mux.Handle("DELETE /api/webhooks/{id}/actions/{actionId}", apiAuth(api.DeleteWebhookAction(pool)))
	mux.Handle("GET /api/webhooks/{id}/filters", apiAuth(api.ListWebhookFilters(pool)))
	mux.Handle("POST /api/webhooks/{id}/filters", apiAuth(api.CreateWebhookFilter(pool)))
	mux.Handle("PUT /api/webhooks/{id}/filters/{filterId}", apiAuth(api.UpdateWebhookFilter(pool)))
	mux.Handle("DELETE /api/webhooks/{id}/filters/{filterId}", apiAuth(api.DeleteWebhookFilter(pool)))

	// Library management
	mux.Handle("GET /api/library/isbn/{isbn}", apiAuth(api.LookupISBN(pool, cfg.EncryptionKey)))
	mux.Handle("POST /api/library/books/import", apiAuth(api.ImportLibraryBooks(pool)))
	mux.Handle("GET /api/library/books", apiAuth(api.ListLibraryBooks(pool)))
	mux.Handle("POST /api/library/books", apiAuth(api.CreateLibraryBook(pool, cfg.EncryptionKey)))
	mux.Handle("GET /api/library/books/{id}", apiAuth(api.GetLibraryBook(pool)))
	mux.Handle("PUT /api/library/books/{id}", apiAuth(api.UpdateLibraryBook(pool)))
	mux.Handle("DELETE /api/library/books/{id}", apiAuth(api.DeleteLibraryBook(pool)))
	mux.Handle("GET /api/library/books/{id}/copies", apiAuth(api.ListLibraryCopies(pool)))
	mux.Handle("POST /api/library/books/{id}/copies", apiAuth(api.CreateLibraryCopy(pool)))
	mux.Handle("PUT /api/library/copies/{id}", apiAuth(api.UpdateLibraryCopy(pool)))
	mux.Handle("DELETE /api/library/copies/{id}", apiAuth(api.DeactivateLibraryCopy(pool)))
	mux.Handle("GET /api/library/checkouts", apiAuth(api.ListLibraryCheckouts(pool)))
	mux.Handle("POST /api/library/checkouts", apiAuth(api.RequestLibraryCheckout(pool)))
	mux.Handle("PUT /api/library/checkouts/{id}/approve", apiAuth(api.ApproveLibraryCheckout(pool)))
	mux.Handle("PUT /api/library/checkouts/{id}/deny", apiAuth(api.DenyLibraryCheckout(pool)))
	mux.Handle("PUT /api/library/checkouts/{id}/return", apiAuth(api.ReturnLibraryCheckout(pool)))
	mux.Handle("PUT /api/library/checkouts/{id}/extend", apiAuth(api.ExtendLibraryCheckout(pool)))
	mux.Handle("GET /api/library/holds", apiAuth(api.ListLibraryHolds(pool)))
	mux.Handle("PUT /api/library/holds/{id}/cancel", apiAuth(api.CancelLibraryHold(pool)))
	mux.Handle("PUT /api/library/holds/{id}/notify", apiAuth(api.NotifyLibraryHold(pool)))

	// Slack event handlers — deprecated, fully migrated to trigger system.

	mux.HandleFunc("GET /cal/{slug}", api.ServeICS(pool))

	// Discourage indexing; silence browser favicon requests.
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "User-agent: *\nDisallow: /")
	})
	mux.HandleFunc("GET /favicon.ico", web.ServeFavicon(pool, cfg.EncryptionKey))

	// Portal — notifications, resources, legislation, report issue, home.
	mux.Handle("GET /fragments/notifications", webAuth(admin.NotificationsPanel()))
	mux.Handle("POST /api/notifications/read", webAuth(admin.NotificationsRead()))
	mux.Handle("GET /resources", webAuth(admin.MemberResourcesPage()))
	mux.Handle("GET /legislation", webAuth(admin.MemberLegislationPage()))
	mux.Handle("POST /api/legislation/subscribe", webAuth(admin.MemberBillSubscribe()))
	mux.Handle("GET /fragments/report-issue", webAuth(admin.ReportIssueForm()))
	mux.Handle("POST /fragments/report-issue", webAuth(admin.ReportIssueSubmit()))
	mux.Handle("GET /portal", webAuth(admin.PortalHome()))
	mux.Handle("GET /{$}", webAuth(admin.RootPage()))
	mux.Handle("GET /fragments/calendar-events", webAuth(admin.CalendarEventsFragment()))

	// Legislation sync — depends on pctx.Notifier() set by SlackPlugin.
	pctx.RegisterScheduledJob(
		"legislation_sync_enabled", "legislation_sync_interval_minutes",
		1*time.Minute, legislationSyncFn,
	)

	if installMode {
		devMode := os.Getenv("INSTALL_MODE") == "true"
		install.ServeInstall(mux, &install.State{
			Pool:          pool,
			EncryptionKey: cfg.EncryptionKeyHex(),
			DevMode:       devMode,
		})
	}

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	// Graceful shutdown on SIGTERM or SIGINT.
	// In-flight uploads are given up to 10 minutes to complete.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		log.Printf("main: server starting on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("main: server: %v", err)
		}
	}()

	<-stop
	log.Println("main: shutting down — waiting for in-flight uploads to complete...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("main: shutdown error: %v", err)
	}
	log.Println("main: shutdown complete")
}

// maybeBootstrapAdmin seeds the first web admin from env vars on a fresh deployment.
// Once credentials exist in the database this function is a no-op — the env vars are ignored.
func maybeBootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) error {
	count, err := store.CountUserCredentials(ctx, pool)
	if err != nil {
		return fmt.Errorf("count credentials: %w", err)
	}
	if count > 0 {
		return nil
	}
	// No credentials exist — this is a fresh deployment.
	if cfg.AdminUsername == "" || cfg.AdminPasswordHash == "" {
		return fmt.Errorf("no admin accounts exist and ADMIN_USERNAME/ADMIN_PASSWORD_HASH are not set — cannot start without a way to log in")
	}
	if _, err := store.UpsertBootstrapUser(ctx, pool, cfg.AdminUsername, cfg.AdminPasswordHash); err != nil {
		return fmt.Errorf("upsert bootstrap admin: %w", err)
	}
	log.Println("main: WARNING: bootstrap admin created from env vars — log in, create a permanent admin account, then remove ADMIN_USERNAME and ADMIN_PASSWORD_HASH from your environment")
	return nil
}

// reencryptSensitiveConfigs finds any sensitive config_store rows that are still stored as
// plaintext (no "enc:v1:" prefix) and re-encrypts them. This runs once on every startup
// and is a no-op once all rows have been encrypted.
func reencryptSensitiveConfigs(ctx context.Context, pool *pgxpool.Pool, encKey []byte) error {
	rows, err := pool.Query(ctx, `
		SELECT id, value FROM config_store
		WHERE sensitive = true AND value NOT LIKE 'enc:v1:%'
	`)
	if err != nil {
		return fmt.Errorf("query plaintext sensitive configs: %w", err)
	}
	defer rows.Close()

	type row struct {
		id    string
		value string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.value); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(pending) == 0 {
		log.Println("main: no plaintext sensitive config rows to re-encrypt")
		return nil
	}

	for _, r := range pending {
		encrypted, err := store.Encrypt(encKey, r.value)
		if err != nil {
			return fmt.Errorf("encrypt config row %s: %w", r.id, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE config_store SET value = $1 WHERE id = $2`, encrypted, r.id); err != nil {
			return fmt.Errorf("update config row %s: %w", r.id, err)
		}
	}
	log.Printf("main: re-encrypted %d plaintext sensitive config row(s)", len(pending))
	return nil
}

// loadDotEnv reads a .env file and sets environment variables.
// Only sets vars that aren't already present in the environment.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range splitLines(string(data)) {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		for i, c := range line {
			if c == '=' {
				key := line[:i]
				val := line[i+1:]
				if os.Getenv(key) == "" {
					os.Setenv(key, val)
				}
				break
			}
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
