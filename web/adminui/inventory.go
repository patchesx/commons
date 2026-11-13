package adminui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"commons/permissions"
	"commons/store"
	"commons/web"
	admintempl "commons/web/templ"
)

const defaultInventoryGraceDays = 5

// inventoryAccess is what the signed-in user may do with inventory. /admin/* only requires a session,
// so every inventory handler checks this itself. Manage and Admin each imply View.
type inventoryAccess struct {
	View   bool
	Manage bool // create, edit, archive, restore, custom fields
	Admin  bool // per-item grace period
}

func (d Deps) inventoryAccess(r *http.Request) inventoryAccess {
	ctx := r.Context()
	perms, err := store.GetUserPermissions(ctx, d.Pool, web.UserIDFromContext(ctx))
	if err != nil {
		return inventoryAccess{}
	}
	var a inventoryAccess
	for _, p := range perms {
		switch p {
		case permissions.InventoryView:
			a.View = true
		case permissions.InventoryManage:
			a.Manage = true
		case permissions.InventoryAdmin:
			a.Admin = true
		}
	}
	a.View = a.View || a.Manage || a.Admin
	return a
}

func forbidden(w http.ResponseWriter) {
	http.Error(w, "forbidden", http.StatusForbidden)
}

// userNames resolves display names for the given user IDs. Empty and unknown IDs are skipped.
func (d Deps) userNames(ctx context.Context, ids ...*string) map[string]string {
	names := map[string]string{}
	for _, id := range ids {
		if id == nil || *id == "" {
			continue
		}
		if _, done := names[*id]; done {
			continue
		}
		if u, err := store.GetUserByID(ctx, d.Pool, *id); err == nil {
			names[*id] = u.DisplayName
		}
	}
	return names
}

func nameFor(names map[string]string, id *string) string {
	if id == nil {
		return ""
	}
	return names[*id]
}

func (d Deps) loadInventoryRows(ctx context.Context, includeArchived bool) ([]admintempl.InventoryRow, error) {
	items, err := store.ListInventoryItems(ctx, d.Pool, includeArchived)
	if err != nil {
		return nil, err
	}
	holders := make([]*string, 0, len(items))
	for i := range items {
		holders = append(holders, items[i].CurrentHolderUserID)
	}
	names := d.userNames(ctx, holders...)

	rows := make([]admintempl.InventoryRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, admintempl.InventoryRow{Item: it, HolderName: nameFor(names, it.CurrentHolderUserID)})
	}
	return rows, nil
}

func (d Deps) InventoryPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.View {
			forbidden(w)
			return
		}
		archived := r.URL.Query().Get("archived") == "1"
		rows, err := d.loadInventoryRows(r.Context(), archived)
		if err != nil {
			http.Error(w, "failed to load inventory", http.StatusInternalServerError)
			return
		}
		// Labels encode the site's root URL, so printing needs bot/admin_url configured.
		_, urlErr := store.BaseURL(r.Context(), d.Pool, d.EncKey)
		admintempl.InventoryPage(rows, archived, acc.Manage, urlErr == nil).Render(r.Context(), w)
	}
}

func (d Deps) InventoryTable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.View {
			forbidden(w)
			return
		}
		rows, err := d.loadInventoryRows(r.Context(), r.URL.Query().Get("archived") == "1")
		if err != nil {
			FragmentError(w, r, "failed to load inventory")
			return
		}
		admintempl.InventoryTable(rows, acc.Manage).Render(r.Context(), w)
	}
}

// inventoryForm is a parsed item form. Grace is only applied when the user has inventory.admin.
type inventoryForm struct {
	name, description, location string
	verifyEveryDays, graceDays  *int
}

func (f inventoryForm) input() store.InventoryItemInput {
	return store.InventoryItemInput{
		Name:            f.name,
		Description:     optionalString(f.description),
		Location:        optionalString(f.location),
		VerifyEveryDays: f.verifyEveryDays,
	}
}

// item is the form's values as an item, for re-rendering the modal after a validation error.
func (f inventoryForm) item(id string) store.InventoryItem {
	return store.InventoryItem{
		ID:              id,
		Name:            f.name,
		Description:     optionalString(f.description),
		Location:        optionalString(f.location),
		VerifyEveryDays: f.verifyEveryDays,
		VerifyGraceDays: f.graceDays,
	}
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseOptionalDays reads a whole number of days that is 1 or more. Blank means unset.
func parseOptionalDays(s string) (*int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return nil, false
	}
	return &n, true
}

func parseInventoryForm(r *http.Request) (inventoryForm, string) {
	f := inventoryForm{
		name:        strings.TrimSpace(r.FormValue("name")),
		description: strings.TrimSpace(r.FormValue("description")),
		location:    strings.TrimSpace(r.FormValue("location")),
	}
	var ok bool
	if f.verifyEveryDays, ok = parseOptionalDays(r.FormValue("verify_every_days")); !ok {
		return f, "Verify every must be a whole number of days, 1 or more, or blank."
	}
	if f.graceDays, ok = parseOptionalDays(r.FormValue("verify_grace_days")); !ok {
		return f, "Grace period must be a whole number of days, 1 or more, or blank."
	}
	if f.name == "" {
		return f, "Name is required."
	}
	return f, ""
}

func (d Deps) InventoryNewModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		days := 7
		admintempl.InventoryItemModal(store.InventoryItem{VerifyEveryDays: &days}, true, acc.Admin, "").Render(r.Context(), w)
	}
}

func (d Deps) InventoryEditModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		item, err := store.GetInventoryItem(r.Context(), d.Pool, r.PathValue("id"))
		if err != nil || item.ArchivedAt != nil {
			FragmentError(w, r, "item not found")
			return
		}
		admintempl.InventoryItemModal(*item, false, acc.Admin, "").Render(r.Context(), w)
	}
}

func (d Deps) InventoryCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		f, msg := parseInventoryForm(r)
		if msg != "" {
			admintempl.InventoryItemModal(f.item(""), true, acc.Admin, msg).Render(ctx, w)
			return
		}
		item, err := store.CreateInventoryItemWithNewCode(ctx, d.Pool, f.input())
		if err != nil {
			log.Printf("inventory: create item: %v", err)
			admintempl.InventoryItemModal(f.item(""), true, acc.Admin, "Failed to create item.").Render(ctx, w)
			return
		}
		if acc.Admin && f.graceDays != nil {
			if err := store.SetInventoryItemGrace(ctx, d.Pool, item.ID, f.graceDays); err != nil {
				log.Printf("inventory: set grace on new item %s: %v", item.ID, err)
			}
		}
		w.Header().Set("HX-Redirect", "/admin/inventory")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) InventoryUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		id := r.PathValue("id")
		f, msg := parseInventoryForm(r)
		if msg != "" {
			admintempl.InventoryItemModal(f.item(id), false, acc.Admin, msg).Render(ctx, w)
			return
		}
		existing, err := store.GetInventoryItem(ctx, d.Pool, id)
		if err != nil {
			admintempl.InventoryItemModal(f.item(id), false, acc.Admin, "Item not found.").Render(ctx, w)
			return
		}
		if existing.ArchivedAt != nil {
			admintempl.InventoryItemModal(f.item(id), false, acc.Admin, "Archived items can't be edited. Restore the item first.").Render(ctx, w)
			return
		}
		if err := store.UpdateInventoryItem(ctx, d.Pool, id, f.input()); err != nil {
			log.Printf("inventory: update item %s: %v", id, err)
			admintempl.InventoryItemModal(f.item(id), false, acc.Admin, "Failed to update item.").Render(ctx, w)
			return
		}
		if acc.Admin {
			if err := store.SetInventoryItemGrace(ctx, d.Pool, id, f.graceDays); err != nil {
				log.Printf("inventory: set grace on item %s: %v", id, err)
				admintempl.InventoryItemModal(f.item(id), false, acc.Admin, "Saved, but the grace period could not be updated.").Render(ctx, w)
				return
			}
		}
		w.Header().Set("HX-Redirect", "/admin/inventory")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) InventoryArchive() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		if err := store.ArchiveInventoryItem(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "item not found or already archived")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) InventoryRestore() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		if err := store.RestoreInventoryItem(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "item not found or not archived")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func inventoryEventLabel(e store.CustodyEvent) string {
	switch {
	case e.Status == "pending_unmatched":
		return "Claim pending (no matching member)"
	case e.Status == "disputed":
		return "Holder reported not having the item"
	case e.Source == "verify":
		return "Verified"
	case e.Source == "revert":
		return "Reverted"
	case e.Status == "linked":
		return "Claim linked to member"
	}
	switch e.Source {
	case "slack":
		return "Taken (Slack)"
	case "web_form":
		return "Taken (web form)"
	case "admin":
		return "Assigned by admin"
	}
	return "Taken"
}

func (d Deps) inventoryGraceDays(ctx context.Context, item *store.InventoryItem) (days int, isDefault bool) {
	if item.VerifyGraceDays != nil {
		return *item.VerifyGraceDays, false
	}
	if v, err := store.GetServiceConfig(ctx, d.Pool, "inventory", "default_grace_days", d.EncKey); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n, true
		}
	}
	return defaultInventoryGraceDays, true
}

func (d Deps) InventoryDetailPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		acc := d.inventoryAccess(r)
		if !acc.View {
			forbidden(w)
			return
		}
		item, err := store.GetInventoryItem(ctx, d.Pool, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "failed to load item", http.StatusInternalServerError)
			return
		}
		events, err := store.ListInventoryCustodyEvents(ctx, d.Pool, item.ID)
		if err != nil {
			http.Error(w, "failed to load item history", http.StatusInternalServerError)
			return
		}
		defs, err := store.ListInventoryFieldDefinitions(ctx, d.Pool, true)
		if err != nil {
			http.Error(w, "failed to load fields", http.StatusInternalServerError)
			return
		}

		ids := []*string{item.CurrentHolderUserID}
		for i := range events {
			ids = append(ids, events[i].UserID, events[i].PreviousHolderUserID)
		}
		names := d.userNames(ctx, ids...)

		fields := make([]admintempl.InventoryFieldValue, 0, len(defs))
		for _, def := range defs {
			if v := item.Metadata[def.Key]; v != "" {
				fields = append(fields, admintempl.InventoryFieldValue{Label: def.Label, Value: v})
			}
		}

		eventRows := make([]admintempl.InventoryEventRow, 0, len(events))
		for _, e := range events {
			holder := nameFor(names, e.UserID)
			if e.UserID == nil {
				holder = strings.TrimSpace(invRawIdentity(e))
			}
			eventRows = append(eventRows, admintempl.InventoryEventRow{
				Event:        e,
				Label:        inventoryEventLabel(e),
				HolderName:   holder,
				PreviousName: nameFor(names, e.PreviousHolderUserID),
			})
		}

		accessories, err := store.ListInventoryAccessories(ctx, d.Pool, item.ID)
		if err != nil {
			http.Error(w, "failed to load accessories", http.StatusInternalServerError)
			return
		}

		graceDays, graceIsDefault := d.inventoryGraceDays(ctx, item)
		admintempl.InventoryDetailPage(admintempl.InventoryDetailData{
			Item:           *item,
			HolderName:     nameFor(names, item.CurrentHolderUserID),
			GraceDays:      graceDays,
			GraceIsDefault: graceIsDefault,
			Fields:         fields,
			Accessories:    accessories,
			Events:         eventRows,
			CanManage:      acc.Manage,
		}).Render(ctx, w)
	}
}

// invRawIdentity formats the name and email a web-form claimant typed, for events with no matched member.
func invRawIdentity(e store.CustodyEvent) string {
	name, email := "", ""
	if e.RawName != nil {
		name = *e.RawName
	}
	if e.RawEmail != nil {
		email = *e.RawEmail
	}
	switch {
	case name != "" && email != "":
		return name + " <" + email + ">"
	case email != "":
		return email
	}
	return name
}

// ---- Custom fields ----

func (d Deps) InventoryFieldsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.View {
			forbidden(w)
			return
		}
		defs, err := store.ListInventoryFieldDefinitions(r.Context(), d.Pool, false)
		if err != nil {
			http.Error(w, "failed to load fields", http.StatusInternalServerError)
			return
		}
		suggested, err := d.inventoryFieldSuggestions(r.Context(), defs)
		if err != nil {
			http.Error(w, "failed to load fields", http.StatusInternalServerError)
			return
		}
		admintempl.InventoryFieldsPage(defs, suggested, acc.Manage).Render(r.Context(), w)
	}
}

func (d Deps) InventoryFieldsCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		label := strings.TrimSpace(r.FormValue("label"))
		key := store.InventoryFieldKey(label)
		if key == "" {
			inlineHelp(w, r, "danger", "Use letters or numbers in the field name.")
			return
		}
		kind, options, msg := fieldKindForm(r)
		if msg != "" {
			inlineHelp(w, r, "danger", msg)
			return
		}
		def, err := store.CreateInventoryFieldDefinition(r.Context(), d.Pool, key, label)
		if err != nil {
			if errors.Is(err, store.ErrInventoryFieldKeyTaken) {
				inlineHelp(w, r, "danger", "A field with that name already exists.")
				return
			}
			log.Printf("inventory: create field: %v", err)
			inlineHelp(w, r, "danger", "Failed to add field.")
			return
		}
		if kind == store.InventoryFieldSelect {
			if err := store.SetInventoryFieldKind(r.Context(), d.Pool, def.ID, kind, options); err != nil {
				log.Printf("inventory: set field kind: %v", err)
				inlineHelp(w, r, "danger", "The field was added as text, but its choices couldn't be saved. Edit it to try again.")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) InventoryFieldsUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		label := strings.TrimSpace(r.FormValue("label"))
		if label == "" {
			inlineHelp(w, r, "danger", "Field name is required.")
			return
		}
		active := r.FormValue("active") == "on"
		kind, options, msg := fieldKindForm(r)
		if msg != "" {
			inlineHelp(w, r, "danger", msg)
			return
		}
		id := r.PathValue("id")
		if err := store.UpdateInventoryFieldDefinition(r.Context(), d.Pool, id, label, active); err != nil {
			inlineHelp(w, r, "danger", "Failed to update field.")
			return
		}
		if err := store.SetInventoryFieldKind(r.Context(), d.Pool, id, kind, options); err != nil {
			log.Printf("inventory: set field kind: %v", err)
			inlineHelp(w, r, "danger", "Failed to update field.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) InventoryFieldsDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		if err := store.DeleteInventoryFieldDefinition(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete field")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// InventoryFieldsMove moves a field one place up or down (?dir=up|down).
func (d Deps) InventoryFieldsMove() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		dir := r.URL.Query().Get("dir")
		if dir != "up" && dir != "down" {
			FragmentError(w, r, "invalid direction")
			return
		}
		defs, err := store.ListInventoryFieldDefinitions(ctx, d.Pool, false)
		if err != nil {
			FragmentError(w, r, "failed to load fields")
			return
		}
		ids := make([]string, len(defs))
		pos := -1
		for i, def := range defs {
			ids[i] = def.ID
			if def.ID == r.PathValue("id") {
				pos = i
			}
		}
		if pos < 0 {
			FragmentError(w, r, "field not found")
			return
		}
		swap := pos - 1
		if dir == "down" {
			swap = pos + 1
		}
		if swap >= 0 && swap < len(ids) {
			ids[pos], ids[swap] = ids[swap], ids[pos]
			if err := store.ReorderInventoryFieldDefinitions(ctx, d.Pool, ids); err != nil {
				FragmentError(w, r, "failed to reorder fields")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- Accessories ----

// InventoryAccessoryAdd adds an accessory to an item. 204 on success; a problem comes back as inline help.
func (d Deps) InventoryAccessoryAdd() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		_, err := store.AddInventoryAccessory(r.Context(), d.Pool, r.PathValue("id"), r.FormValue("name"))
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrInventoryAccessoryInvalid):
			inlineHelp(w, r, "danger", fmt.Sprintf("Enter a name of up to %d characters.", store.MaxInventoryAccessoryNameLen))
		case errors.Is(err, store.ErrInventoryAccessoryExists):
			inlineHelp(w, r, "danger", "This item already has an accessory with that name.")
		case errors.Is(err, store.ErrInventoryTooManyAccessories):
			inlineHelp(w, r, "danger", fmt.Sprintf("An item can have at most %d accessories.", store.MaxInventoryAccessories))
		case errors.Is(err, store.ErrNotFound):
			inlineHelp(w, r, "danger", "Item not found or archived.")
		default:
			log.Printf("inventory: add accessory: %v", err)
			inlineHelp(w, r, "danger", "Failed to add accessory.")
		}
	}
}

func (d Deps) InventoryAccessoryDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		if err := store.DeleteInventoryAccessory(r.Context(), d.Pool, r.PathValue("id"), r.PathValue("accessoryID")); err != nil {
			FragmentError(w, r, "accessory not found")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// fieldKindForm reads a field form's type and, for a dropdown, its choices (one per line). msg is set when
// the form can't be used.
func fieldKindForm(r *http.Request) (kind string, options []string, msg string) {
	kind = r.FormValue("kind")
	switch kind {
	case "", store.InventoryFieldText:
		return store.InventoryFieldText, nil, ""
	case store.InventoryFieldSelect:
		opts, err := store.NormalizeInventoryFieldOptions(strings.Split(r.FormValue("options"), "\n"))
		if err != nil {
			return "", nil, fmt.Sprintf("Add at least one choice, one per line (up to %d choices of %d characters).",
				store.MaxInventoryFieldOptions, store.MaxInventoryFieldOptionLen)
		}
		return kind, opts, ""
	}
	return "", nil, "Unknown field type."
}

// inventoryFieldSuggestions lists, for each text field, the values already on items: what its choices start
// from when it becomes a dropdown.
func (d Deps) inventoryFieldSuggestions(ctx context.Context, defs []store.InventoryFieldDefinition) (map[string][]string, error) {
	var keys []string
	for _, def := range defs {
		if !def.IsSelect() {
			keys = append(keys, def.Key)
		}
	}
	return store.ListInventoryFieldValues(ctx, d.Pool, keys)
}
