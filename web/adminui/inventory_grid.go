package adminui

import (
	"context"
	"encoding/csv"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"commons/store"
	admintempl "commons/web/templ"
)

const (
	inventoryGridPageSize = 20
	maxGridCellLen        = 2000
)

// gridSortKey accepts a sort key from the query string: a built-in column, or a custom field that is active.
func gridSortKey(sort string, fields []store.InventoryFieldDefinition) string {
	switch sort {
	case "name", "code", "location", "holder", "verified":
		return sort
	}
	if key, ok := strings.CutPrefix(sort, "field:"); ok {
		for _, f := range fields {
			if f.Key == key {
				return sort
			}
		}
	}
	return "name"
}

// gridQuery reads the grid's search, filters, and sort from the query string.
func gridQuery(r *http.Request, fields []store.InventoryFieldDefinition) store.InventoryGridQuery {
	v := r.URL.Query()
	return store.InventoryGridQuery{
		Search:          strings.TrimSpace(v.Get("q")),
		IncludeArchived: v.Get("archived") == "1",
		PendingOnly:     v.Get("pending") == "1",
		SortKey:         gridSortKey(v.Get("sort"), fields),
		Desc:            v.Get("dir") == "desc",
	}
}

func (d Deps) loadInventoryGrid(ctx context.Context, r *http.Request, canManage bool) (admintempl.InventoryGridData, error) {
	fields, err := store.ListInventoryFieldDefinitions(ctx, d.Pool, true)
	if err != nil {
		return admintempl.InventoryGridData{}, err
	}
	q := gridQuery(r, fields)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)

	q.Limit = inventoryGridPageSize
	q.Offset = (page - 1) * inventoryGridPageSize
	rows, total, err := store.ListInventoryGrid(ctx, d.Pool, q)
	if err != nil {
		return admintempl.InventoryGridData{}, err
	}
	pages := max(1, (total+inventoryGridPageSize-1)/inventoryGridPageSize)
	if page > pages {
		// The list shrank (a filter, or an item archived): show the last page instead of an empty one.
		page = pages
		q.Offset = (page - 1) * inventoryGridPageSize
		if rows, _, err = store.ListInventoryGrid(ctx, d.Pool, q); err != nil {
			return admintempl.InventoryGridData{}, err
		}
	}
	return admintempl.InventoryGridData{
		Rows: rows, Fields: fields, Total: total, Page: page, TotalPages: pages,
		Search: q.Search, IncludeArchived: q.IncludeArchived, PendingOnly: q.PendingOnly,
		Sort: q.SortKey, Desc: q.Desc, CanManage: canManage,
	}, nil
}

func (d Deps) InventoryGridPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.View {
			forbidden(w)
			return
		}
		data, err := d.loadInventoryGrid(r.Context(), r, acc.Manage)
		if err != nil {
			http.Error(w, "failed to load inventory", http.StatusInternalServerError)
			return
		}
		admintempl.InventoryGridPage(data).Render(r.Context(), w)
	}
}

// InventoryGridFragment re-renders just the table, for sorting, filtering, and paging.
func (d Deps) InventoryGridFragment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.View {
			forbidden(w)
			return
		}
		data, err := d.loadInventoryGrid(r.Context(), r, acc.Manage)
		if err != nil {
			FragmentError(w, r, "failed to load inventory")
			return
		}
		admintempl.InventoryGrid(data).Render(r.Context(), w)
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// csvSafe stops a spreadsheet from running a cell as a formula: a value starting with = + - @ or a control
// character gets a leading apostrophe.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// InventoryGridCSV exports every item matching the current search, sort, and filters (not just one page).
func (d Deps) InventoryGridCSV() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).View {
			forbidden(w)
			return
		}
		fields, err := store.ListInventoryFieldDefinitions(ctx, d.Pool, true)
		if err != nil {
			http.Error(w, "failed to load fields", http.StatusInternalServerError)
			return
		}
		rows, _, err := store.ListInventoryGrid(ctx, d.Pool, gridQuery(r, fields))
		if err != nil {
			http.Error(w, "failed to load inventory", http.StatusInternalServerError)
			return
		}
		ids := make([]string, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		accessories, err := store.ListInventoryAccessoriesFor(ctx, d.Pool, ids)
		if err != nil {
			http.Error(w, "failed to load accessories", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="inventory-`+time.Now().Format("2006-01-02")+`.csv"`)
		w.Header().Set("Cache-Control", "private, no-store")
		cw := csv.NewWriter(w)
		header := []string{"Name", "Code", "Location", "Description", "Holder", "Holder email", "Last verified", "Status", "Claim pending", "Accessories"}
		for _, f := range fields {
			header = append(header, f.Label)
		}
		cw.Write(header)
		for _, row := range rows {
			status, pending, verified := "Active", "", ""
			if row.ArchivedAt != nil {
				status = "Archived"
			}
			if row.ClaimPending {
				pending = "Yes"
			}
			if row.LastVerifiedAt != nil {
				verified = row.LastVerifiedAt.Format("2006-01-02")
			}
			record := []string{
				csvSafe(row.Name), store.FormatInventoryCode(row.Code), csvSafe(derefStr(row.Location)),
				csvSafe(derefStr(row.Description)), csvSafe(row.HolderName), csvSafe(row.HolderEmail), verified, status, pending,
				csvSafe(accessoryList(accessories[row.ID])),
			}
			for _, f := range fields {
				record = append(record, csvSafe(row.Metadata[f.Key]))
			}
			cw.Write(record)
		}
		cw.Flush()
	}
}

// ---- Inline cell editing ----

type gridCellRef struct {
	acc   inventoryAccess
	row   store.InventoryGridRow
	col   string
	label string
	field *store.InventoryFieldDefinition // the custom field, nil for a built-in column
}

// gridColumnLabel names an editable column: name, location, or an active custom field.
func gridColumnLabel(col string, fields []store.InventoryFieldDefinition) (string, bool) {
	switch col {
	case "name":
		return "Name", true
	case "location":
		return "Location", true
	}
	if key, ok := strings.CutPrefix(col, "field:"); ok {
		for _, f := range fields {
			if f.Key == key {
				return f.Label, true
			}
		}
	}
	return "", false
}

// gridCell checks permission, validates the column, and loads the item's row.
func (d Deps) gridCell(w http.ResponseWriter, r *http.Request, needManage bool) (gridCellRef, bool) {
	ctx := r.Context()
	acc := d.inventoryAccess(r)
	if !acc.View || (needManage && !acc.Manage) {
		forbidden(w)
		return gridCellRef{}, false
	}
	fields, err := store.ListInventoryFieldDefinitions(ctx, d.Pool, true)
	if err != nil {
		http.Error(w, "failed to load fields", http.StatusInternalServerError)
		return gridCellRef{}, false
	}
	col := r.PathValue("col")
	label, ok := gridColumnLabel(col, fields)
	if !ok {
		http.NotFound(w, r)
		return gridCellRef{}, false
	}
	row, ok := d.gridRow(ctx, w, r, r.PathValue("id"))
	if !ok {
		return gridCellRef{}, false
	}
	ref := gridCellRef{acc: acc, row: row, col: col, label: label}
	if key, ok := strings.CutPrefix(col, "field:"); ok {
		for i := range fields {
			if fields[i].Key == key {
				ref.field = &fields[i]
			}
		}
	}
	return ref, true
}

func (d Deps) gridRow(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) (store.InventoryGridRow, bool) {
	rows, _, err := store.ListInventoryGrid(ctx, d.Pool, store.InventoryGridQuery{ID: id, IncludeArchived: true})
	if err != nil {
		http.NotFound(w, r) // an ID that isn't a UUID is also "not found"
		return store.InventoryGridRow{}, false
	}
	if len(rows) == 0 {
		http.NotFound(w, r)
		return store.InventoryGridRow{}, false
	}
	return rows[0], true
}

// InventoryGridCell returns a cell's display form (the editor's Cancel, and Escape).
func (d Deps) InventoryGridCell() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := d.gridCell(w, r, false)
		if !ok {
			return
		}
		admintempl.InventoryGridCell(c.row, c.col, c.acc.Manage).Render(r.Context(), w)
	}
}

// InventoryGridCellEdit returns the cell's one-line editor. Archived items can't be edited, so they get the
// plain cell back.
func (d Deps) InventoryGridCellEdit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := d.gridCell(w, r, true)
		if !ok {
			return
		}
		if c.row.ArchivedAt != nil {
			admintempl.InventoryGridCell(c.row, c.col, true).Render(r.Context(), w)
			return
		}
		admintempl.InventoryGridCellEditor(c.row, c.col, c.label, admintempl.InventoryGridCellText(c.row, c.col), "", c.choices()).Render(r.Context(), w)
	}
}

// InventoryGridCellSave saves one cell. On success it returns the cell's display form; on a problem it returns
// the editor again with a message, and nothing is saved.
func (d Deps) InventoryGridCellSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, ok := d.gridCell(w, r, true)
		if !ok {
			return
		}
		value := strings.TrimSpace(r.FormValue("value"))
		editorWith := func(msg string) {
			admintempl.InventoryGridCellEditor(c.row, c.col, c.label, value, msg, c.choices()).Render(ctx, w)
		}

		switch {
		case c.row.ArchivedAt != nil:
			editorWith("Archived items can't be edited. Restore the item first.")
			return
		case utf8.RuneCountInString(value) > maxGridCellLen:
			editorWith("That's too long (2000 characters at most).")
			return
		}

		var err error
		if key, isField := strings.CutPrefix(c.col, "field:"); isField {
			err = store.SetInventoryItemMetadata(ctx, d.Pool, c.row.ID, map[string]string{key: value})
		} else {
			err = store.SetInventoryItemBuiltinField(ctx, d.Pool, c.row.ID, c.col, value)
		}
		switch {
		case errors.Is(err, store.ErrInventoryInvalidValue):
			editorWith("A name is required.")
			return
		case errors.Is(err, store.ErrInventoryNotAnOption):
			editorWith("Pick one of the choices in the list.")
			return
		case errors.Is(err, store.ErrInventoryUnknownField):
			editorWith("That field is no longer active. Reload the page.")
			return
		case errors.Is(err, store.ErrInventoryItemArchived):
			editorWith("Archived items can't be edited. Restore the item first.")
			return
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			log.Printf("inventory: save cell %s of item %s: %v", c.col, c.row.ID, err)
			editorWith("Couldn't save. Try again.")
			return
		}

		row, ok := d.gridRow(ctx, w, r, c.row.ID)
		if !ok {
			return
		}
		admintempl.InventoryGridCell(row, c.col, true).Render(ctx, w)
	}
}

// accessoryList joins accessory names into one comma-separated cell, the form the import reads back.
func accessoryList(list []store.InventoryAccessory) string {
	names := make([]string, len(list))
	for i, a := range list {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

// choices is the dropdown list for a dropdown field's cell, nil for a plain cell. A value the item already
// has that isn't in the list is kept as a choice so opening the editor doesn't lose it.
func (c gridCellRef) choices() []string {
	if c.field == nil || !c.field.IsSelect() {
		return nil
	}
	opts := c.field.Options
	if cur := c.row.Metadata[c.field.Key]; cur != "" {
		if _, err := c.field.CheckValue(cur); err != nil {
			opts = append(append([]string{}, opts...), cur)
		}
	}
	return opts
}
