package adminui

import (
	"context"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
	"commons/web"
	admintempl "commons/web/templ"
)

// integInputTypeMap maps config keys to their UI input type.
// Valid values: "boolean", "channel", "timezone", "textarea", "readonly".
// Keys absent from the map default to "text" (single-line input).
var integInputTypeMap = map[string]string{
	"delete_after_upload": "boolean",
	"enabled":             "boolean",
	"connected_email":     "readonly",
}

func (d Deps) IntegrationsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, available, err := buildIntegrationListData(r.Context(), d.Pool, d.EncKey, d.Pctx)
		if err != nil {
			http.Error(w, "failed to load integrations", http.StatusInternalServerError)
			return
		}
		admintempl.IntegrationsPage(items, available).Render(r.Context(), w)
	}
}

func (d Deps) IntegrationDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		integ, err := store.GetIntegrationByTypeAny(r.Context(), d.Pool, slug)
		if err != nil {
			http.Error(w, "integration not found", http.StatusNotFound)
			return
		}
		entries, err := buildConfigEntries(r.Context(), d.Pool, integ.Type, d.EncKey, false)
		if err != nil {
			http.Error(w, "failed to load config entries", http.StatusInternalServerError)
			return
		}
		hasMissing, hasSensitive := false, false
		for _, e := range entries {
			if e.Required && !e.Configured {
				hasMissing = true
			}
			if e.Sensitive && e.Configured {
				hasSensitive = true
			}
		}
		admintempl.IntegrationDetailPage(admintempl.IntegrationDetailData{
			Integration:  *integ,
			Entries:      entries,
			HasMissing:   hasMissing,
			HasSensitive: hasSensitive,
			HasSetup:     plugin.HasSetup(slug),
		}).Render(r.Context(), w)
	}
}

func (d Deps) IntegrationsAddModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, available, err := buildIntegrationListData(r.Context(), d.Pool, d.EncKey, d.Pctx)
		if err != nil {
			FragmentError(w, r, "failed to load integrations")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.AddPluginModalContent(available).Render(r.Context(), w)
	}
}

func (d Deps) IntegrationsConfigFields() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		integID := r.URL.Query().Get("integration_id")
		if integID == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		integrations, err := store.ListIntegrations(r.Context(), d.Pool)
		if err != nil {
			FragmentError(w, r, "failed to load integrations")
			return
		}
		var found *store.Integration
		for i := range integrations {
			if integrations[i].ID == integID {
				found = &integrations[i]
				break
			}
		}
		if found == nil {
			FragmentError(w, r, "integration not found")
			return
		}
		entries, err := buildConfigEntries(r.Context(), d.Pool, found.Type, d.EncKey, false)
		if err != nil {
			FragmentError(w, r, "failed to load config schema")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.AddPluginConfigFields(*found, entries).Render(r.Context(), w)
	}
}

func (d Deps) IntegrationsAdd() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "invalid form data")
			return
		}
		integID := r.FormValue("integration_id")
		integType := r.FormValue("integration_type")
		if integID == "" || integType == "" {
			FragmentError(w, r, "missing integration_id or integration_type")
			return
		}
		schema, err := store.ListConfigSchema(r.Context(), d.Pool, integType)
		if err != nil {
			FragmentError(w, r, "failed to load config schema")
			return
		}
		sensitiveByKey := make(map[string]bool, len(schema))
		for _, s := range schema {
			sensitiveByKey[s.Key] = s.Sensitive
		}
		adminID := web.UserIDFromContext(r.Context())
		var adminIDPtr *string
		if adminID != "" {
			adminIDPtr = &adminID
		}
		for _, s := range schema {
			val := r.FormValue("cfg_" + s.Key)
			if val == "" {
				continue
			}
			if err := store.SetServiceConfig(r.Context(), d.Pool, integType, s.Key, val, s.Sensitive, adminIDPtr, d.EncKey); err != nil {
				log.Printf("adminui/integrations: add:failed to save config key %q for service %q: %v", s.Key, integType, err)
				FragmentError(w, r, "failed to save config")
				return
			}
		}
		if err := store.SetIntegrationEnabled(r.Context(), d.Pool, integID, true); err != nil {
			log.Printf("adminui/integrations: add:failed to enable integration %q: %v", integID, err)
			FragmentError(w, r, "failed to enable integration")
			return
		}
		w.Header().Set("HX-Redirect", "/admin/integrations")
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) ConfigFragment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := r.PathValue("service")
		reveal := r.URL.Query().Get("reveal") == "true"
		entries, err := buildConfigEntries(r.Context(), d.Pool, service, d.EncKey, reveal)
		if err != nil {
			FragmentError(w, r, "failed to load config")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.CredentialTableSectionFragment(service, entries, reveal).Render(r.Context(), w)
	}
}

func (d Deps) SettingsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := loadSettingsData(r.Context(), d.Pool, d.EncKey, d.Pctx)
		if err != nil {
			http.Error(w, "failed to load settings", http.StatusInternalServerError)
			return
		}
		admintempl.SettingsPage(data).Render(r.Context(), w)
	}
}

func (d Deps) SettingsDefaultCalendar() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		calID := r.FormValue("default_calendar_id")
		if err := store.SetServiceConfig(r.Context(), d.Pool, "bot", "default_calendar_id", calID, false, nil, d.EncKey); err != nil {
			FragmentError(w, r, "failed to save default calendar")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) AppHomePage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		botEntries, err := buildConfigEntries(ctx, d.Pool, "bot", d.EncKey, false)
		if err != nil {
			http.Error(w, "failed to load bot config", http.StatusInternalServerError)
			return
		}
		links, _ := store.ListQuickLinks(ctx, d.Pool)
		contacts, _ := store.ListContacts(ctx, d.Pool)
		users, _ := store.ListUsers(ctx, d.Pool)
		admintempl.AppHomePage(admintempl.AppHomePageData{
			BotEntries: botEntries,
			Links:      links,
			Contacts:   contacts,
			Users:      users,
		}).Render(ctx, w)
	}
}

func (d Deps) AppHomeLinksList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		links, _ := store.ListQuickLinks(r.Context(), d.Pool)
		admintempl.QuickLinksListFragment(links).Render(r.Context(), w)
	}
}

func (d Deps) AppHomeContactsList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contacts, _ := store.ListContacts(r.Context(), d.Pool)
		admintempl.ContactsListFragment(contacts).Render(r.Context(), w)
	}
}

// --- helper functions ---

func buildConfigEntries(ctx context.Context, pool *pgxpool.Pool, service string, encKey []byte, reveal bool) ([]admintempl.ConfigEntryView, error) {
	schema, err := store.ListConfigSchema(ctx, pool, service)
	if err != nil {
		return nil, err
	}
	configs, err := store.ListServiceConfigs(ctx, pool, service)
	if err != nil {
		return nil, err
	}
	valueByKey := make(map[string]store.ConfigEntry, len(configs))
	for _, c := range configs {
		valueByKey[c.Key] = c
	}
	result := make([]admintempl.ConfigEntryView, 0, len(schema))
	for _, s := range schema {
		if s.Key == "enabled" {
			continue
		}
		entry, hasValue := valueByKey[s.Key]
		var displayVal string
		if hasValue && entry.Value != "" {
			if s.Sensitive {
				if reveal {
					if plain, err := store.Decrypt(encKey, entry.Value); err == nil {
						displayVal = plain
					}
				} else {
					displayVal = "●●●●●●●●"
				}
			} else {
				displayVal = entry.Value
			}
		}
		desc := ""
		if s.Description != nil {
			desc = *s.Description
		}
		inputType := integInputTypeMap[s.Key]
		if inputType == "" {
			if s.Sensitive {
				inputType = "password"
			} else {
				inputType = "text"
			}
		}
		result = append(result, admintempl.ConfigEntryView{
			Key:         s.Key,
			Label:       s.Label,
			Description: desc,
			Sensitive:   s.Sensitive,
			Required:    s.Required,
			Configured:  hasValue && entry.Value != "",
			Value:       displayVal,
			InputType:   inputType,
		})
	}
	return result, nil
}

func buildIntegrationListData(ctx context.Context, pool *pgxpool.Pool, encKey []byte, pctx plugin.PluginContext) ([]admintempl.IntegrationListItemData, []store.Integration, error) {
	integrations, err := store.ListIntegrations(ctx, pool)
	if err != nil {
		return nil, nil, err
	}

	claimedBy := map[string]plugin.IntegrationCardSpec{}
	for _, spec := range pctx.ExtraIntegrationCards() {
		for _, t := range spec.ReplacesTypes {
			claimedBy[t] = spec
		}
	}

	emitted := map[string]bool{}
	var items []admintempl.IntegrationListItemData
	var available []store.Integration

	for _, integ := range integrations {
		if spec, claimed := claimedBy[integ.Type]; claimed {
			if emitted[spec.Name] {
				continue
			}
			emitted[spec.Name] = true
			entries, err := buildConfigEntries(ctx, pool, spec.ConfigService, encKey, false)
			if err != nil {
				return nil, nil, err
			}
			hasMissing, hasConfig := false, false
			for _, e := range entries {
				if e.Required && !e.Configured {
					hasMissing = true
				}
				if e.Configured {
					hasConfig = true
				}
			}
			_ = hasConfig
			items = append(items, admintempl.IntegrationListItemData{
				Integration: store.Integration{
					ID:      integ.ID,
					Type:    spec.ConfigService,
					Name:    spec.Name,
					Enabled: integ.Enabled,
				},
				HasMissing: hasMissing,
			})
			continue
		}

		entries, err := buildConfigEntries(ctx, pool, integ.Type, encKey, false)
		if err != nil {
			return nil, nil, err
		}
		hasMissing, hasConfig := false, false
		for _, e := range entries {
			if e.Required && !e.Configured {
				hasMissing = true
			}
			if e.Configured {
				hasConfig = true
			}
		}
		if integ.Enabled || hasConfig {
			items = append(items, admintempl.IntegrationListItemData{
				Integration: integ,
				HasMissing:  hasMissing,
			})
		} else {
			available = append(available, integ)
		}
	}
	return items, available, nil
}

func buildFeatureConfigEntries(ctx context.Context, pool *pgxpool.Pool, service string, encKey []byte) ([]admintempl.FeatureConfigEntry, error) {
	schema, err := store.ListConfigSchema(ctx, pool, service)
	if err != nil {
		return nil, err
	}
	configs, err := store.ListServiceConfigs(ctx, pool, service)
	if err != nil {
		return nil, err
	}
	valueByKey := make(map[string]store.ConfigEntry, len(configs))
	for _, c := range configs {
		valueByKey[c.Key] = c
	}
	result := make([]admintempl.FeatureConfigEntry, 0, len(schema))
	for _, s := range schema {
		entry, hasValue := valueByKey[s.Key]
		val := ""
		if hasValue && entry.Value != "" {
			if s.Sensitive {
				if plain, err := store.Decrypt(encKey, entry.Value); err == nil {
					val = plain
				}
			} else {
				val = entry.Value
			}
		}
		desc := ""
		if s.Description != nil {
			desc = *s.Description
		}
		result = append(result, admintempl.FeatureConfigEntry{
			Key:         s.Key,
			Label:       s.Label,
			Description: desc,
			Value:       val,
			Configured:  hasValue && entry.Value != "",
		})
	}
	return result, nil
}

func loadSettingsData(ctx context.Context, pool *pgxpool.Pool, encKey []byte, pctx plugin.PluginContext) (admintempl.SettingsPageData, error) {
	meetingEntries, err := buildFeatureConfigEntries(ctx, pool, "meetings", encKey)
	if err != nil {
		return admintempl.SettingsPageData{}, err
	}
	cals, err := store.ListCalendars(ctx, pool)
	if err != nil {
		return admintempl.SettingsPageData{}, err
	}
	defCalID, _ := store.GetServiceConfig(ctx, pool, "bot", "default_calendar_id", encKey)
	bills, err := store.ListBills(ctx, pool)
	if err != nil {
		return admintempl.SettingsPageData{}, err
	}
	recentUpdates, err := store.CountRecentUpdates(ctx, pool, 30)
	if err != nil {
		return admintempl.SettingsPageData{}, err
	}
	routing, _ := store.GetServiceConfig(ctx, pool, "notifications", "routing", encKey)
	if routing == "" {
		routing = "web_and_chat"
	}
	allowRegStr, _ := store.GetServiceConfig(ctx, pool, "auth", "allow_registration", encKey)
	defaultGroupID, _ := store.GetServiceConfig(ctx, pool, "auth", "default_group_id", encKey)
	portalEnabledStr, _ := store.GetServiceConfig(ctx, pool, "portal", "enabled", encKey)
	allGroups, err := store.ListRoleGroups(ctx, pool)
	if err != nil {
		return admintempl.SettingsPageData{}, err
	}
	groups := make([]admintempl.GroupRef, len(allGroups))
	for i, g := range allGroups {
		groups[i] = admintempl.GroupRef{ID: g.ID, Name: g.Name, DisplayName: g.DisplayName}
	}
	storageLocations, _ := store.ListStorageLocationsByType(ctx, pool, "gdrive")
	var storageIntegrationID string
	if gdriveInteg, err := store.GetIntegrationByType(ctx, pool, "gdrive"); err == nil && gdriveInteg != nil {
		storageIntegrationID = gdriveInteg.ID
	}
	var extraCardPaths []string
	for _, reg := range pctx.ExtraSettingsCards() {
		if reg.Group == "settings" {
			extraCardPaths = append(extraCardPaths, reg.Path)
		}
	}
	var baseURL admintempl.ConfigEntryView
	if schemaEntries, err := store.ListConfigSchema(ctx, pool, "app"); err == nil {
		configVals, _ := store.ListServiceConfigs(ctx, pool, "app")
		configMap := map[string]string{}
		for _, c := range configVals {
			configMap[c.Key] = c.Value
		}
		for _, s := range schemaEntries {
			if s.Key != "base_url" {
				continue
			}
			val := configMap[s.Key]
			desc := ""
			if s.Description != nil {
				desc = *s.Description
			}
			baseURL = admintempl.ConfigEntryView{
				Key:         s.Key,
				Label:       s.Label,
				Description: desc,
				Sensitive:   s.Sensitive,
				Required:    s.Required,
				Configured:  val != "",
				Value:       val,
			}
		}
	}
	return admintempl.SettingsPageData{
		BaseURL:              baseURL,
		MeetingEntries:       meetingEntries,
		CalendarCount:        len(cals),
		Calendars:            cals,
		DefaultCalendarID:    defCalID,
		TrackedBills:         len(bills),
		RecentBillUpdates:    recentUpdates,
		NotificationRouting:  routing,
		AllowRegistration:    allowRegStr == "true",
		DefaultGroupID:       defaultGroupID,
		Groups:               groups,
		HasZoom:              pctx.HasCapability("zoom.recordings"),
		StorageLocations:     storageLocations,
		StorageIntegrationID: storageIntegrationID,
		ExtraCardPaths:       extraCardPaths,
		PortalEnabled:        portalEnabledStr == "true",
	}, nil
}
